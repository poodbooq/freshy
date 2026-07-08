// Command freshy keeps GitHub-sourced tools fresh: it clones/pulls a list
// of repositories, runs each package's install script on change, and
// atomically drops the resulting binaries into your PATH.
//
// Usage:
//
//   freshy init                 # create config skeleton, dirs, enable timer
//   freshy sync [pkg...]       # sync zero or more packages (default: all)
//   freshy status              # show each package's last SHA / error
//   freshy add <repo-url>      # interactively add a package
//   freshy remove <pkg>        # uninstall + remove from config
//   freshy uninstall <pkg>     # alias for remove
//   freshy logs [-f] [-n N]    # show last N log lines (default 50, -f follows)
//   freshy doctor              # sanity-check config + paths + units
//   freshy version
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/poodbooq/freshy/internal/config"
	"github.com/poodbooq/freshy/internal/editor"
	"github.com/poodbooq/freshy/internal/installer_template"
	"github.com/poodbooq/freshy/internal/logger"
	"github.com/poodbooq/freshy/internal/paths"
	"github.com/poodbooq/freshy/internal/state"
	syncpkg "github.com/poodbooq/freshy/internal/sync"
)

// version is overridden via -ldflags "-X main.version=..." at build.
var version = "dev"

const (
	defaultConfigTemplate = `# Freshy configuration — see ` + "`freshy doctor`" + ` or README for details.
config_version = 1

[settings]
schedule     = "6h"            # systemd duration: 30m, 6h, 1h30m, ...
parallel     = 4
log_path     = "~/.local/share/freshy/logs/freshy.log"
install_to   = "~/.local/bin"
prune_builds = true

# [[packages]]
# name       = "example"
# repo       = "https://github.com/you/example.git"  # or git@github.com:...
# branch     = "main"
# installer  = "~/.local/share/freshy/installers/example.sh"  # local shell script
# binaries   = ["example"]
`
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "config":
		err = cmdConfig(args)
	case "init":
		err = cmdInit()
	case "sync":
		err = cmdSync(args)
	case "status":
		err = cmdStatus(args)
	case "add":
		err = cmdAdd(args)
	case "remove", "uninstall":
		err = cmdRemove(args)
	case "logs":
		err = cmdLogs(args)
	case "doctor":
		err = cmdDoctor(args)
	case "version", "--version", "-v":
		fmt.Printf("freshy %s\n", version)
		return
	case "help", "--help", "-h":
		usage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "freshy: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "freshy %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `freshy — keep your GitHub-sourced binaries fresh

Usage:
  freshy <command> [flags]

Commands:
  init                 Create config skeleton, dirs, enable systemd timer
  sync [pkg ...]       Sync zero or more packages (default: all)
  status               Per-package last SHA, last sync, last error
  add <repo-url>       Interactively add a package to the config
  remove <pkg>         Uninstall a package + drop it from config
  uninstall <pkg>      Alias for remove
  config edit          Open the config file in $EDITOR
  logs [-f] [-n N]     Show the log (default: last 50 lines; -f to follow)
  doctor               Sanity-check config, paths, systemd units
  version              Print version

Run 'freshy <command> -h' for command-specific help.`)
}

// ─────────────────────────── config ────────────────────────────

func cmdConfig(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(os.Stderr, `Usage: freshy config <subcommand>

Subcommands:
  edit    Open the config file in $EDITOR

Run 'freshy config <subcommand> -h' for subcommand-specific help.`)
		if len(args) == 0 {
			return errors.New("missing subcommand")
		}
		return nil
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "edit":
		return cmdConfigEdit(subArgs)
	default:
		return fmt.Errorf("unknown config subcommand %q", sub)
	}
}

func cmdConfigEdit(_ []string) error {
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfgPath); err != nil {
		return fmt.Errorf("config not found at %s; run `freshy init` first", cfgPath)
	}
	fmt.Fprintf(os.Stderr, "opening %s …\n", cfgPath)
	return editor.Open(cfgPath)
}

// ─────────────────────────── init ───────────────────────────

func cmdInit() error {
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	dirs := []struct {
		name string
		fn   func() (string, error)
	}{
		{"config dir", paths.ConfigDir},
		{"data dir", paths.DataDir},
		{"state dir", paths.StateDir},
		{"repos dir", paths.ReposDir},
		{"builds dir", paths.BuildsDir},
		{"logs dir", paths.LogsDir},
	}
	for _, d := range dirs {
		abs, err := d.fn()
		if err != nil {
			return fmt.Errorf("%s: %w", d.name, err)
		}
		if _, err := paths.EnsureDir(abs); err != nil {
			return fmt.Errorf("%s: %w", d.name, err)
		}
		fmt.Printf("✓ %s: %s\n", d.name, abs)
	}

	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(cfgPath, []byte(defaultConfigTemplate), 0o644); err != nil {
			return err
		}
		fmt.Printf("✓ created config skeleton: %s\n", cfgPath)
	} else if err != nil {
		return err
	} else {
		fmt.Printf("• config already exists: %s\n", cfgPath)
	}

	if err := installSystemdUnits(); err != nil {
		// Not fatal: systemd may not be available (e.g. macOS, WSL old).
		fmt.Fprintf(os.Stderr, "warn: systemd units not installed: %v\n", err)
	} else {
		if err := enableTimer(); err != nil {
			fmt.Fprintf(os.Stderr, "warn: enable timer: %v\n", err)
		} else {
			fmt.Println("✓ systemd timer enabled (freshy.timer)")
		}
	}
	fmt.Println("Done. Edit the config and run `freshy sync`.")
	return nil
}

func installSystemdUnits() error {
	home, err := paths.Home()
	if err != nil {
		return err
	}
	srcDir := findSystemdSrc(home)
	if srcDir == "" {
		return errors.New("could not locate systemd/ source dir relative to this binary")
	}
	dstDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	// Render the timer with the user's configured schedule.
	cfgPath, err := config.DefaultPath()
	schedule := "6h"
	if err == nil {
		if cfg, lerr := config.Load(cfgPath); lerr == nil && cfg.Settings.Schedule != "" {
			schedule = cfg.Settings.Schedule
		}
	}
	for _, name := range []string{"freshy.service", "freshy.timer"} {
		src := filepath.Join(srcDir, name)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			// fall back to timer template
			src = filepath.Join(srcDir, name+".template")
		}
		dst := filepath.Join(dstDir, name)
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read %s: %w", src, err)
		}
		var rendered string
		if strings.HasSuffix(src, ".template") {
			t, err := template.New("unit").Parse(string(data))
			if err != nil {
				return fmt.Errorf("parse template %s: %w", src, err)
			}
			var sb strings.Builder
			if err := t.Execute(&sb, map[string]string{"Schedule": schedule}); err != nil {
				return fmt.Errorf("render template %s: %w", src, err)
			}
			rendered = sb.String()
			rendered = renderUnit(name, rendered, home)
		} else {
			rendered = renderUnit(name, string(data), home)
		}
		if err := os.WriteFile(dst, []byte(rendered), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
		fmt.Printf("✓ installed %s\n", dst)
	}
	return nil
}

func renderUnit(name, body, home string) string {
	// Path templating is intentionally empty: units should already
	// carry either `%h`-prefixed paths (preferred; we substitute them
	// below) or no paths at all. Historically we hardcoded
	// `%h/bin/freshy` for the binary path; that has since moved to
	// `%h/.local/bin/freshy` in the units and is rendered by the
	// generic %h substitution.
	body = strings.ReplaceAll(body, "%h", home)
	return body
}

// findSystemdSrc locates the systemd/ directory shipped alongside the
// binary. We try (in order):
//  1. $FRESHY_SOURCE/lib/systemd  (set by the install script)
//  2. ./systemd/ relative to cwd (development)
//  3. ../lib/systemd/systemd/ relative to the executable (dev tree)
func findSystemdSrc(home string) string {
	if d := os.Getenv("FRESHY_SOURCE"); d != "" {
		cand := filepath.Join(d, "lib", "systemd")
		if isDir(cand) {
			return cand
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		cand := filepath.Join(cwd, "lib", "systemd")
		if isDir(cand) {
			return cand
		}
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		// Try ../lib/systemd/systemd
		cand := filepath.Join(dir, "..", "lib", "systemd")
		if isDir(cand) {
			if abs, err := filepath.Abs(cand); err == nil {
				return abs
			}
			return cand
		}
	}
	return ""
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func enableTimer() error {
	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", "--now", "freshy.timer"},
	} {
		cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s: %v: %s",
				strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// ─────────────────────────── sync ───────────────────────────

func cmdSync(args []string) error {
	only := args
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no config at %s; run `freshy init` first", cfgPath)
	}
	if err != nil {
		return err
	}
	logPath, err := paths.Expand(cfg.Settings.LogPath)
	if err != nil {
		return err
	}
	if err := logger.EnsureDir(logPath); err != nil {
		return err
	}
	log, err := logger.New(logPath)
	if err != nil {
		return err
	}
	defer log.Close()

	pkgs := cfg.SelectedPackages(only)
	if len(pkgs) == 0 && len(cfg.Packages) > 0 && len(only) > 0 {
		return fmt.Errorf("no packages matched: %s", strings.Join(only, ", "))
	}
	if len(pkgs) == 0 {
		log.Infof("no packages configured — nothing to do")
		fmt.Println("freshy: no packages configured")
		return nil
	}

	runner := syncpkg.NewRunner(cfg, log)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	results := runner.RunAll(ctx, pkgs)

	// Pretty print summary
	w := tabWriter(os.Stdout)
	fmt.Fprintln(w, "PACKAGE\tSTATUS\tSHA\tINFO")
	for _, r := range results {
		_, sha, info := describePackage(cfg, log, r)
		row := r.Package + "\t" + r.Status.String() + "\t" + short(sha, 7) + "\t" + info
		fmt.Fprintln(w, row)
	}
	w.Flush()

	for _, r := range results {
		if r.Status == syncpkg.Failed {
			return fmt.Errorf("one or more packages failed (see log: %s)", logPath)
		}
	}
	return nil
}

func describePackage(cfg config.File, log *logger.Logger, r syncpkg.Result) (pkg config.Package, sha, info string) {
	if p, ok := cfg.Find(r.Package); ok {
		pkg = p
	}
	st, err := state.Load(r.Package)
	if err == nil {
		sha = st.LastSHA
		switch r.Status {
		case syncpkg.OK:
			info = fmt.Sprintf("deployed @ %s", st.LastSyncedAt.Format(time.RFC3339))
		case syncpkg.NoOp:
			info = fmt.Sprintf("up-to-date @ %s", st.LastCheckedAt.Format(time.RFC3339))
		case syncpkg.Failed, syncpkg.Skipped:
			if st.LastError != "" {
				info = st.LastError
			}
		}
	}
	_ = log
	return
}

// ─────────────────────────── status ───────────────────────────

func cmdStatus(_ []string) error {
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no config at %s; run `freshy init` first", cfgPath)
	}
	if err != nil {
		return err
	}
	w := tabWriter(os.Stdout)
	fmt.Fprintln(w, "NAME\tREPO\tBRANCH\tLAST SHA\tLAST SYNC\tLAST CHECK\tSTATUS\tLAST ERROR")
	for _, p := range cfg.SortedPackages() {
		st, _ := state.Load(p.Name)
		lastSHA := "-"
		lastSync := "-"
		lastCheck := "-"
		lastErr := ""
		status := "never synced"
		if st != nil {
			if st.LastSHA != "" {
				lastSHA = short(st.LastSHA, 7)
			}
			if !st.LastSyncedAt.IsZero() {
				lastSync = st.LastSyncedAt.Local().Format("2006-01-02 15:04")
			}
			if !st.LastCheckedAt.IsZero() {
				lastCheck = st.LastCheckedAt.Local().Format("2006-01-02 15:04")
			}
			if st.LastError != "" {
				status = "error"
				lastErr = st.LastError
			} else if st.LastSHA != "" {
				status = "ok"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			p.Name, p.Repo, p.Branch, lastSHA, lastSync, lastCheck, status, lastErr)
	}
	w.Flush()
	return nil
}

// ─────────────────────────── add ───────────────────────────

func cmdAdd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: freshy add <repo-url>")
	}
	repo := args[0]
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		cfg = config.Default()
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// Derive a name from the repo URL.
	name := deriveName(repo)
	name = promptOrDefault("package name", name)

	// Prevent dupes.
	if _, ok := cfg.Find(name); ok {
		return fmt.Errorf("package %q already in config", name)
	}

	defaultBranch := detectDefaultBranch(repo)
	branch := promptOrDefault("branch", defaultBranch)
	bins := promptOrDefault("binary names (comma-separated within repo root)", name)
	binaryList := splitCSV(bins)
	if len(binaryList) == 0 {
		return fmt.Errorf("at least one binary name is required")
	}

	// Resolve the canonical installer path. If it already exists,
	// the user is probably re-running add to refresh an existing
	// package — open it in $EDITOR instead of generating a fresh
	// template. We surface that choice when the path exists AND
	// the user is non-interactive — but here we assume a TTY.
	instPath, err := paths.InstallerFile(name)
	if err != nil {
		return err
	}

	reuseExisting := false
	if _, err := os.Stat(instPath); err == nil {
		fmt.Fprintf(os.Stderr, "installer already exists at %s; opening for editing (Ctrl-C to abort)\n", instPath)
		reuseExisting = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// Materialize the file (template or path-only).
	if !reuseExisting {
		rendered, err := installer_template.Render(installer_template.Data{
			Pkg:           name,
			Repo:          repo,
			Branch:        branch,
			Binaries:      binaryList,
			InstallerPath: instPath,
		})
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(instPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(instPath, []byte(rendered), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote template to %s\n", instPath)
	}

	// Hand off to the user's editor.
	if err := editor.Open(instPath); err != nil {
		return fmt.Errorf("editor exited: %w", err)
	}

	// Sanity: the file must exist and be non-empty after editing.
	st, err := os.Stat(instPath)
	if err != nil {
		return err
	}
	if st.Size() == 0 {
		return fmt.Errorf("installer is empty (%s); nothing to do", instPath)
	}
	if err := os.Chmod(instPath, 0o755); err != nil {
		return err
	}

	cfg.Packages = append(cfg.Packages, config.Package{
		Name:      name,
		Repo:      repo,
		Branch:    branch,
		Installer: instPath,
		Binaries:  binaryList,
	})
	if err := cfg.Save(cfgPath); err != nil {
		return err
	}
	fmt.Printf("✓ added %q to %s\n", name, cfgPath)
	fmt.Printf("  installer: %s\n", instPath)
	fmt.Printf("  next:      freshy sync %s\n", name)
	return nil
}

func deriveName(repo string) string {
	base := filepath.Base(strings.TrimSuffix(repo, ".git"))
	if idx := strings.Index(base, "@"); idx >= 0 {
		base = base[idx+1:]
	}
	if idx := strings.LastIndex(base, ":"); idx >= 0 {
		base = base[idx+1:]
	}
	return sanitize(base)
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			out = append(out, r)
		}
	}
	res := strings.TrimSpace(string(out))
	if res == "" {
		return "package"
	}
	return res
}

func promptOrDefault(prompt, def string) string {
	fmt.Fprintf(os.Stderr, "%s [%s]: ", prompt, def)
	rdr := bufio.NewReader(os.Stdin)
	line, _ := rdr.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ─────────────────────────── remove / uninstall ───────────────────────────

func cmdRemove(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: freshy remove <pkg>")
	}
	name := args[0]
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	pkg, ok := cfg.Find(name)
	if !ok {
		return fmt.Errorf("package %q not found in config", name)
	}

	// Remove staged binaries from PATH.
	binDir, _ := paths.BinDir(cfg.Settings.InstallTo)
	for _, b := range pkg.Binaries {
		target := filepath.Join(binDir, b)
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "warn: could not remove %s: %v\n", target, err)
		} else {
			fmt.Printf("✓ removed %s\n", target)
		}
	}

	// Drop the package from config.
	filtered := cfg.Packages[:0]
	for _, p := range cfg.Packages {
		if p.Name != name {
			filtered = append(filtered, p)
		}
	}
	cfg.Packages = filtered
	if err := cfg.Save(cfgPath); err != nil {
		return err
	}

	// Remove state and clones (best-effort).
	for _, fn := range []func(string) (string, error){paths.StateFile, paths.RepoDir, paths.PackageBuildsDir} {
		p, err := fn(name)
		if err != nil {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			fmt.Fprintf(os.Stderr, "warn: remove %s: %v\n", p, err)
		}
	}
	fmt.Printf("✓ %q uninstalled\n", name)
	return nil
}

// ─────────────────────────── logs ───────────────────────────

func cmdLogs(args []string) error {
	follow := false
	n := 50
	for _, a := range args {
		switch {
		case a == "-f":
			follow = true
		case a == "-h" || a == "--help":
			fmt.Println("usage: freshy logs [-f] [-n N]")
			return nil
		case strings.HasPrefix(a, "-n"):
			val := strings.TrimPrefix(a, "-n")
			if val == "" && len(args) > 1 {
				val = args[1]
			}
			if _, err := fmt.Sscanf(val, "%d", &n); err != nil || n < 1 {
				return fmt.Errorf("invalid -n %q", val)
			}
		}
	}
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	logPath := ""
	if cfg.ConfigVersion > 0 {
		logPath, _ = paths.Expand(cfg.Settings.LogPath)
	}
	if logPath == "" {
		logPath = filepath.Join(mustDataHome(), "logs", "freshy.log")
	}
	if _, err := os.Stat(logPath); err != nil {
		return fmt.Errorf("no log file at %s", logPath)
	}
	if follow {
		return followFile(logPath)
	}
	return tailFile(logPath, n)
}

func mustDataHome() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return x
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "freshy")
}

func tailFile(path string, n int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	lines, err := readReverseLines(f, n)
	if err != nil {
		return err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		fmt.Println(strings.TrimRight(lines[i], "\n"))
	}
	return nil
}

func readReverseLines(r io.Reader, n int) ([]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	str := string(data)
	if str == "" {
		return nil, nil
	}
	all := strings.Split(strings.TrimRight(str, "\n"), "\n")
	if len(all) <= n {
		return all, nil
	}
	return all[len(all)-n:], nil
}

func followFile(path string) error {
	cmd := exec.Command("tail", "-F", "-n", "50", path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// ─────────────────────────── doctor ───────────────────────────

func cmdDoctor(_ []string) error {
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	checks := []struct {
		name string
		fn   func() error
	}{
		{"config path parent dir", func() error { return mustDir(filepath.Dir(cfgPath)) }},
		{"config file present", func() error {
			_, err := os.Stat(cfgPath)
			return err
		}},
		{"config parses", func() error {
			_, err := config.Load(cfgPath)
			return err
		}},
		{"git available", func() error {
			_, err := exec.LookPath("git")
			return err
		}},
		{"bash available", func() error {
			_, err := exec.LookPath("bash")
			return err
		}},
		{"data dirs exist", func() error {
			dd, err := paths.DataDir()
			if err != nil {
				return err
			}
			return mustDir(dd)
		}},
		{"systemctl available", func() error {
			_, err := exec.LookPath("systemctl")
			return err
		}},
	}
	allOK := true
	for _, c := range checks {
		if err := c.fn(); err != nil {
			fmt.Printf("✗ %s: %v\n", c.name, err)
			allOK = false
		} else {
			fmt.Printf("✓ %s\n", c.name)
		}
	}
	if !allOK {
		return errors.New("one or more checks failed")
	}
	return nil
}

func mustDir(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s exists but is not a directory", p)
	}
	return nil
}

// ─────────────────────────── helpers ───────────────────────────

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// tabWriter returns a minimal tab-aligned writer.
func tabWriter(w io.Writer) *miniTab {
	return &miniTab{w: bufio.NewWriter(w)}
}

type miniTab struct {
	w *bufio.Writer
}

func (m *miniTab) Write(p []byte) (int, error) {
	// Replace tabs with two spaces for prettier CLI output.
	s := strings.ReplaceAll(string(p), "\t", "  ")
	return m.w.WriteString(s)
}
func (m *miniTab) Flush() error { return m.w.Flush() }

// Compile-time check that Result encoding works (used in tests
// elsewhere).
var _ = json.Marshal

// stub for sort import to keep linters happy even if we move code.
var _ = sort.Strings

// detectDefaultBranch queries the remote repo's HEAD ref to find its
// default branch (e.g. "main" or "master"). If detection fails (no git,
// network error, non-Git URL), it silently returns "main" as a sensible
// fallback so the prompt still has a default.
func detectDefaultBranch(repo string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// git ls-remote --symref <repo> HEAD outputs something like:
	//   ref: refs/heads/main	HEAD
	//   <sha>	HEAD
	out, err := exec.CommandContext(ctx, "git",
		"ls-remote", "--symref", repo, "HEAD",
	).Output()
	if err != nil {
		return "main"
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "ref: refs/heads/") {
			return strings.TrimPrefix(strings.Fields(line)[1], "refs/heads/")
		}
	}
	return "main"
}
