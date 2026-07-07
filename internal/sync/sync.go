// Package sync is the core of freshy: it pulls one package, runs its
// install script, and (on success) atomically swaps the resulting
// binaries into the target bin directory.
//
// The flow for a single package is:
//
//   1. Clone if missing, else fast-forward pull.
//   2. Compare HEAD SHA to state.LastSHA.
//      - Same => no-op (update LastCheckedAt).
//      - Different => continue.
//   3. Run the package's install_script with cwd=repo root.
//      - Non-zero exit => record error, leave PATH untouched.
//   4. Stage binaries under builds/<pkg>/<binary>.<sha>, verify them.
//   5. Atomic rename into <install_to>/<binary>.
//   6. Optionally prune stale staged files.
//   7. Persist state.
//
// Concurrency: callers can run multiple packages in parallel; the
// file-level mutations are confined to per-package paths so races
// only happen on the target bin dir. We serialize writes to
// install_to per binary name.
package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/poodbooq/freshy/internal/config"
	"github.com/poodbooq/freshy/internal/logger"
	"github.com/poodbooq/freshy/internal/paths"
	"github.com/poodbooq/freshy/internal/state"
)

// Result is what Sync returns per package.
type Result struct {
	Package string
	Status  Status
	Err     error // nil for Status==OK / Status==NoOp
}

// Status enumerates the outcomes for one package.
type Status int

const (
	OK      Status = iota // new SHA, install + deploy succeeded
	NoOp                  // HEAD SHA matched state, nothing to do
	Failed                // git or install error; PATH untouched
	Skipped               // user asked to skip or git pull rejected
)

func (s Status) String() string {
	switch s {
	case OK:
		return "ok"
	case NoOp:
		return "noop"
	case Failed:
		return "failed"
	case Skipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// Runner orchestrates one or more package syncs.
type Runner struct {
	Cfg      config.File
	Log      *logger.Logger
	Parallel int // override for tests; <=0 means use Cfg.Settings.Parallel
}

// NewRunner builds a Runner from the parsed config.
func NewRunner(cfg config.File, log *logger.Logger) *Runner {
	return &Runner{Cfg: cfg, Log: log, Parallel: cfg.Settings.Parallel}
}

// RunAll syncs the given packages concurrently. The slice may be empty
// to mean "sync everything in cfg". A package not found in cfg yields
// a Failed Result.
func (r *Runner) RunAll(ctx context.Context, pkgs []config.Package) []Result {
	if len(pkgs) == 0 {
		pkgs = r.Cfg.Packages
	}
	if r.Parallel <= 0 {
		r.Parallel = r.Cfg.Settings.Parallel
	}
	results := make([]Result, len(pkgs))

	// Per-binary-name mutex map (lazy-init).
	var (
		binaryMu  sync.Mutex
		binaryLks = map[string]*sync.Mutex{}
	)
	lockFor := func(name string) *sync.Mutex {
		binaryMu.Lock()
		defer binaryMu.Unlock()
		if lk, ok := binaryLks[name]; ok {
			return lk
		}
		lk := &sync.Mutex{}
		binaryLks[name] = lk
		return lk
	}

	sem := make(chan struct{}, r.Parallel)
	var wg sync.WaitGroup
	for i, p := range pkgs {
		i, p := i, p
		wg.Add(1)
		select {
		case <-ctx.Done():
			results[i] = Result{Package: p.Name, Status: Skipped, Err: ctx.Err()}
			wg.Done()
			continue
		case sem <- struct{}{}:
		}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = r.runOne(ctx, p, lockFor)
		}()
	}
	wg.Wait()
	return results
}

func (r *Runner) runOne(ctx context.Context, p config.Package, lockFor func(string) *sync.Mutex) Result {
	log := r.Log
	if log == nil {
		log = logger.NewNull()
	}

	st, err := state.Load(p.Name)
	if err != nil {
		log.Errorf("[%s] state load: %v", p.Name, err)
		return Result{Package: p.Name, Status: Failed, Err: err}
	}

	repoDir, err := paths.RepoDir(p.Name)
	if err != nil {
		log.Errorf("[%s] repo dir: %v", p.Name, err)
		return Result{Package: p.Name, Status: Failed, Err: err}
	}

	// Step 1+2: ensure clone, then pull.
	headSHA, err := ensureCloneAndPull(ctx, log, p, repoDir)
	if err != nil {
		st.RecordError(err.Error())
		_ = st.Save()
		log.Errorf("[%s] pull failed: %v", p.Name, err)
		return Result{Package: p.Name, Status: Failed, Err: err}
	}

	if headSHA == "" {
		// git pull was rejected (non-ff); skip but log.
		msg := "git pull rejected (non fast-forward); leaving repo as-is"
		log.Warnf("[%s] %s", p.Name, msg)
		st.RecordError(msg)
		_ = st.Save()
		return Result{Package: p.Name, Status: Skipped, Err: errors.New(msg)}
	}

	// Step 3: nothing to do?
	if st.LastSHA == headSHA && st.LastSHA != "" {
		log.Infof("[%s] up-to-date at %s", p.Name, shortSHA(headSHA))
		st.MarkChecked(headSHA)
		_ = st.Save()
		return Result{Package: p.Name, Status: NoOp}
	}

	// Step 4: install.
	if err := runInstaller(ctx, log, p, repoDir); err != nil {
		log.Errorf("[%s] install: %v", p.Name, err)
		st.RecordError(err.Error())
		_ = st.Save()
		return Result{Package: p.Name, Status: Failed, Err: err}
	}

	// Step 5: stage + deploy.
	binDir, err := paths.BinDir(r.Cfg.Settings.InstallTo)
	if err != nil {
		log.Errorf("[%s] bin dir resolve: %v", p.Name, err)
		st.RecordError(err.Error())
		_ = st.Save()
		return Result{Package: p.Name, Status: Failed, Err: err}
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		log.Errorf("[%s] mkdir bin dir: %v", p.Name, err)
		st.RecordError(err.Error())
		_ = st.Save()
		return Result{Package: p.Name, Status: Failed, Err: err}
	}

	buildsDir, err := paths.PackageBuildsDir(p.Name)
	if err != nil {
		log.Errorf("[%s] builds dir: %v", p.Name, err)
		st.RecordError(err.Error())
		_ = st.Save()
		return Result{Package: p.Name, Status: Failed, Err: err}
	}
	if err := os.MkdirAll(buildsDir, 0o755); err != nil {
		log.Errorf("[%s] mkdir builds dir: %v", p.Name, err)
		st.RecordError(err.Error())
		_ = st.Save()
		return Result{Package: p.Name, Status: Failed, Err: err}
	}

	for _, bin := range p.Binaries {
		// Per-binary lock to prevent parallel swaps of the same name.
		lk := lockFor(bin)
		lk.Lock()
		err := stageAndDeploy(log, p, bin, headSHA, repoDir, buildsDir, binDir, r.Cfg.Settings.PruneBuilds)
		lk.Unlock()
		if err != nil {
			log.Errorf("[%s] deploy %s: %v", p.Name, bin, err)
			st.RecordError(err.Error())
			_ = st.Save()
			return Result{Package: p.Name, Status: Failed, Err: err}
		}
		log.Infof("[%s] deployed %s (%s)", p.Name, bin, shortSHA(headSHA))
	}

	st.RecordInstallSuccess(headSHA)
	st.RepoLocalPath = repoDir
	if err := st.Save(); err != nil {
		log.Warnf("[%s] state save: %v", p.Name, err)
	}
	return Result{Package: p.Name, Status: OK}
}

// ensureCloneAndPull clones the repo on first run, pulls on subsequent
// runs, and returns the HEAD SHA after the operation. An empty SHA
// (with err==nil) signals a non-fast-forward that we refused to follow.
func ensureCloneAndPull(ctx context.Context, log *logger.Logger, p config.Package, repoDir string) (string, error) {
	gitDir := filepath.Join(repoDir, ".git")
	if _, err := os.Stat(gitDir); errors.Is(err, os.ErrNotExist) {
		log.Infof("[%s] cloning %s", p.Name, p.Repo)
		cmd := exec.CommandContext(ctx, "git",
			"clone", "--depth=1", "-b", p.Branch, p.Repo, repoDir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git clone failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
	} else {
		log.Infof("[%s] pulling", p.Name)
		cmd := exec.CommandContext(ctx, "git",
			"-C", repoDir, "fetch", "--depth=1", "origin", p.Branch)
		out, err := cmd.CombinedOutput()
		if err != nil {
			// If fetch fails (e.g. transient network), we still try to
			// operate on whatever we have, but warn loudly.
			log.Warnf("[%s] git fetch failed (%v): %s -- continuing with local refs",
				p.Name, err, strings.TrimSpace(string(out)))
		}
		// Merge ff-only. If rejected, signal via empty SHA + nil err.
		merge := exec.CommandContext(ctx, "git",
			"-C", repoDir, "merge", "--ff-only",
			"FETCH_HEAD")
		if out, err := merge.CombinedOutput(); err != nil {
			// Branch might not have changed: treat FETCH_HEAD missing as no-op.
			if isAlreadyUpToDate(out) {
				// fall through to HEAD read below
			} else {
				log.Warnf("[%s] non-fast-forward: %s", p.Name, strings.TrimSpace(string(out)))
				return "", nil
			}
		}
	}

	// Read HEAD SHA.
	out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// isAlreadyUpToDate detects the benign "Already up to date." case.
func isAlreadyUpToDate(out []byte) bool {
	return strings.Contains(string(out), "Already up to date")
}

// runInstaller resolves the package's installer script and executes
// it with cwd = repo root. The script path is user-controlled (set in
// `settings.installer` per package); it may live anywhere on disk.
func runInstaller(ctx context.Context, log *logger.Logger, p config.Package, repoDir string) error {
	script := p.Installer
	if script == "" {
		return fmt.Errorf("no installer configured for %s", p.Name)
	}
	// Resolve `~` for ergonomics.
	if strings.HasPrefix(script, "~") {
		h, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		script = filepath.Join(h, strings.TrimPrefix(script, "~/"))
	}
	absScript, err := filepath.Abs(script)
	if err != nil {
		return err
	}
	if _, err := os.Stat(absScript); err != nil {
		return fmt.Errorf("installer %s: %w", absScript, err)
	}
	log.Infof("[%s] running installer %s", p.Name, absScript)
	cmd := exec.CommandContext(ctx, "bash", absScript)
	cmd.Dir = repoDir
	// Capture output to the log file so failures are diagnosable.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				log.Infof("[%s] %s", p.Name, strings.TrimRight(string(buf[:n]), "\n"))
			}
			if err != nil {
				return
			}
		}
	}()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("installer exited with error: %w", err)
	}
	return nil
}

// stageAndDeploy:
//   1. resolve source path within repo (post-install), copy to builds/<pkg>/<bin>.<sha>
//   2. verify staged (non-empty, has exec bit)
//   3. atomically rename staged -> install_to/<bin> (via temp + rename)
func stageAndDeploy(
	log *logger.Logger,
	p config.Package,
	bin, sha, repoDir, buildsDir, binDir string,
	prune bool,
) error {
	src := filepath.Join(repoDir, bin)
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("source %s not found after install: %w", bin, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source %s is not a regular file", bin)
	}
	if info.Size() == 0 {
		return fmt.Errorf("source %s is empty", bin)
	}

	staged := filepath.Join(buildsDir, fmt.Sprintf("%s.%s", bin, sha[:7]))
	if err := copyFile(src, staged, info.Mode()); err != nil {
		return fmt.Errorf("copy to stage: %w", err)
	}
	// Double-check staged.
	si, err := os.Stat(staged)
	if err != nil {
		return err
	}
	if si.Size() == 0 {
		os.Remove(staged)
		return fmt.Errorf("staged %s is empty", staged)
	}
	// Ensure exec bit.
	if err := os.Chmod(staged, 0o755); err != nil {
		os.Remove(staged)
		return fmt.Errorf("chmod stage: %w", err)
	}

	// Atomic deploy: write to .new, fsync (best-effort), rename.
	target := filepath.Join(binDir, bin)
	tmp := filepath.Join(binDir, fmt.Sprintf(".%s.%s.new", bin, sha[:7]))
	if err := copyFile(staged, tmp, 0o755); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("copy to target tmp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename into place: %w", err)
	}
	_ = log
	if prune {
		pruneBuilds(buildsDir, bin, sha[:7])
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

// pruneBuilds removes staged files for `bin` other than the keep version.
func pruneBuilds(buildsDir, bin, keepSuffix string) {
	entries, err := os.ReadDir(buildsDir)
	if err != nil {
		return
	}
	prefix := bin + "."
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// keepSuffix is e.g. "abc1234"; we keep "binary.abc1234".
		if name == bin+"."+keepSuffix {
			continue
		}
		_ = os.Remove(filepath.Join(buildsDir, name))
	}
}

func shortSHA(s string) string {
	if len(s) >= 7 {
		return s[:7]
	}
	return s
}
