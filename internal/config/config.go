// Package config reads and writes freshy's TOML configuration.
//
// The on-disk schema:
//
//   config_version = 1
//
//   [settings]
//   schedule      = "6h"        # systemd-style duration, e.g. "30m", "6h", "1h30m"
//   parallel      = 4
//   log_path      = "~/.local/share/freshy/logs/freshy.log"
//   install_to    = "~/.local/bin"
//   prune_builds  = true
//
//   [[packages]]
//   name       = "coolapp"
//   repo       = "https://github.com/you/coolapp.git"   # or git@github.com:...
//   branch     = "main"
//   installer  = "~/.local/share/freshy/installers/coolapp.sh"   # local script
//   binaries   = ["coolapp"]   # relative to repo root, copied post-install
//
// Legacy field `install_script` (path inside the upstream repo) is
// auto-migrated on first load: the script is copied under
// $XDG_DATA_HOME/freshy/installers/<name>.sh, and the config file is
// rewritten with `installer` set. The legacy field stays valid for
// v1 only; future versions will reject it.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/poodbooq/freshy/internal/paths"
)

const CurrentVersion = 1

// Settings is the [settings] table.
type Settings struct {
	Schedule     string `toml:"schedule"`
	Parallel     int    `toml:"parallel"`
	LogPath      string `toml:"log_path"`
	InstallTo    string `toml:"install_to"`
	PruneBuilds  bool   `toml:"prune_builds"`
}

// Package is one [[packages]] entry.
//
// `Installer` points at a local shell script owned by the user. It is
// the authoritative post-pull step. The script runs with cwd = repo
// root, so it can read sources / run make / cargo install / etc.
//
// `InstallScript` is the legacy in-repo path (e.g. "scripts/install.sh")
// — kept only for v1 back-compat auto-migration. Set in Load() if the
// TOML had `install_script = "..."`. After migration it is empty.
type Package struct {
	Name          string   `toml:"name"`
	Repo          string   `toml:"repo"`
	Branch        string   `toml:"branch"`
	Installer     string   `toml:"installer"`
	InstallScript string   `toml:"install_script,omitempty"` // legacy
	Binaries      []string `toml:"binaries"`
}

// File is the top-level schema.
type File struct {
	ConfigVersion int       `toml:"config_version"`
	Settings      Settings  `toml:"settings"`
	Packages      []Package `toml:"packages"`
}

// Default returns a freshly initialised File with conservative defaults
// and no packages.
func Default() File {
	return File{
		ConfigVersion: CurrentVersion,
		Settings: Settings{
			Schedule:    "6h",
			Parallel:    4,
			LogPath:     "~/.local/share/freshy/logs/freshy.log",
			InstallTo:   "~/.local/bin",
			PruneBuilds: true,
		},
		Packages: []Package{},
	}
}

// Load reads and parses path. If the file does not exist it returns
// (Default(), os.IsNotExist=true). Other errors are real.
//
// On a successful parse, packages that still use the legacy
// `install_script` field are auto-migrated: the script is copied into
// $XDG_DATA_HOME/freshy/installers/<name>.sh and the config file is
// rewritten with `installer` set. If migration cannot be completed
// (e.g. the script can't be located because it isn't cloned yet), the
// package keeps the legacy field and an error is returned.
func Load(path string) (File, error) {
	var f File
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), os.ErrNotExist
	}
	if err != nil {
		return f, fmt.Errorf("read %s: %w", path, err)
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		return f, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.ConfigVersion == 0 {
		f.ConfigVersion = CurrentVersion
	}
	f.applyDefaults()
	if err := f.Validate(); err != nil {
		return f, fmt.Errorf("invalid config %s: %w", path, err)
	}
	if migrated, mErr := f.MigrateLegacy(path); mErr != nil {
		return f, mErr
	} else if migrated {
		if err := f.Save(path); err != nil {
			return f, fmt.Errorf("persist migrated config: %w", err)
		}
	}
	return f, nil
}

// MigrateLegacy copies any in-repo install scripts out to a
// user-controlled `~/.local/share/freshy/installers/<name>.sh`,
// rewrites the affected packages to set `installer`, and clears
// `install_script`. Returns (true, nil) if any migration happened.
//
// Migration is best-effort: the source script must already exist at
// `<repo>/<install_script>`. If it doesn't (because no clone yet),
// the migration is skipped with an explanatory error so the user can
// fix it manually after the first clone/pull.
func (f *File) MigrateLegacy(configPath string) (bool, error) {
	any := false
	for i := range f.Packages {
		p := &f.Packages[i]
		if p.Installer != "" || p.InstallScript == "" {
			continue
		}
		repoDir, err := paths.RepoDir(p.Name)
		if err != nil {
			return any, err
		}
		src := filepath.Join(repoDir, p.InstallScript)
		if _, err := os.Stat(src); err != nil {
			return any, fmt.Errorf(
				"package %q uses legacy `install_script = %q` "+
					"but the script does not exist at %s yet — "+
					"run `freshy sync %s` once to clone the repo, "+
					"then freshy will migrate it automatically",
				p.Name, p.InstallScript, src, p.Name,
			)
		}
		dst, err := pathInstallerFor(p.Name)
		if err != nil {
			return any, err
		}
		if err := copyFileAtomic(src, dst, 0o755); err != nil {
			return any, fmt.Errorf("copy installer for %s: %w", p.Name, err)
		}
		p.Installer = dst
		p.InstallScript = ""
		any = true
	}
	return any, nil
}

// pathInstallerFor returns the canonical local-installer path for pkg.
//
// Layout: $XDG_DATA_HOME/freshy/installers/<pkg>.sh.
func pathInstallerFor(pkg string) (string, error) {
	d, err := paths.InstallersDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(d, pkg+".sh"), nil
}

// copyFileAtomic copies src to dst via temp+rename.
func copyFileAtomic(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".installer.*.sh")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, dst)
}

// applyDefaults fills missing settings values with their defaults.
func (f *File) applyDefaults() {
	d := Default().Settings
	if f.Settings.Schedule == "" {
		f.Settings.Schedule = d.Schedule
	}
	if f.Settings.Parallel <= 0 {
		f.Settings.Parallel = d.Parallel
	}
	if f.Settings.LogPath == "" {
		f.Settings.LogPath = d.LogPath
	}
	if f.Settings.InstallTo == "" {
		f.Settings.InstallTo = d.InstallTo
	}
	for i := range f.Packages {
		if f.Packages[i].Branch == "" {
			f.Packages[i].Branch = "main"
		}
	}
}

// Validate checks the configuration for syntactic / semantic errors.
func (f *File) Validate() error {
	if f.ConfigVersion != CurrentVersion {
		return fmt.Errorf("config_version=%d, expected %d", f.ConfigVersion, CurrentVersion)
	}
	if _, err := time.ParseDuration(f.Settings.Schedule); err != nil {
		return fmt.Errorf("settings.schedule %q: %w", f.Settings.Schedule, err)
	}
	if f.Settings.Parallel < 1 {
		return fmt.Errorf("settings.parallel must be >= 1 (got %d)", f.Settings.Parallel)
	}
	if f.Settings.LogPath == "" {
		return errors.New("settings.log_path is empty")
	}
	seen := map[string]bool{}
	for i, p := range f.Packages {
		if p.Name == "" {
			return fmt.Errorf("packages[%d].name is empty", i)
		}
		if seen[p.Name] {
			return fmt.Errorf("packages[%d].name %q duplicated", i, p.Name)
		}
		seen[p.Name] = true
		if p.Repo == "" {
			return fmt.Errorf("packages[%s].repo is empty", p.Name)
		}
		// Require either the new `installer` field or the legacy
		// `install_script` field (which Load() will migrate).
		if p.Installer == "" && p.InstallScript == "" {
			return fmt.Errorf("packages[%s]: installer (or legacy install_script) is required", p.Name)
		}
		if len(p.Binaries) == 0 {
			return fmt.Errorf("packages[%s].binaries is empty", p.Name)
		}
	}
	return nil
}

// Find returns the package with the given name and a "found" flag.
func (f *File) Find(name string) (Package, bool) {
	for _, p := range f.Packages {
		if p.Name == name {
			return p, true
		}
	}
	return Package{}, false
}

// SortedPackages returns packages sorted by name, so iteration order
// is deterministic in logs.
func (f *File) SortedPackages() []Package {
	out := make([]Package, len(f.Packages))
	copy(out, f.Packages)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SelectedPackages returns the subset of SortedPackages whose name is
// in `only`. If `only` is empty, all packages are returned.
func (f *File) SelectedPackages(only []string) []Package {
	if len(only) == 0 {
		return f.SortedPackages()
	}
	wanted := make(map[string]bool, len(only))
	for _, n := range only {
		wanted[n] = true
	}
	var out []Package
	for _, p := range f.SortedPackages() {
		if wanted[p.Name] {
			out = append(out, p)
		}
	}
	return out
}

// Save writes the config atomically (via temp file + rename).
func (f *File) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config.toml.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(f); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// DefaultPath returns the canonical location of config.toml.
func DefaultPath() (string, error) {
	return paths.ConfigFile()
}
