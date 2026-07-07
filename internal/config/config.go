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
//   name           = "coolapp"
//   repo           = "https://github.com/you/coolapp.git"   # or git@github.com:...
//   branch         = "main"
//   install_script = "scripts/install.sh"
//   binaries       = ["coolapp"]  # relative to repo root
package config

import (
	"errors"
	"fmt"
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
type Package struct {
	Name          string   `toml:"name"`
	Repo          string   `toml:"repo"`
	Branch        string   `toml:"branch"`
	InstallScript string   `toml:"install_script"`
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
	return f, nil
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
		if p.InstallScript == "" {
			return fmt.Errorf("packages[%s].install_script is empty", p.Name)
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
