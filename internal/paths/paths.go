// Package paths resolves filesystem locations used by freshy.
//
// All paths are user-space (never /etc or /var/lib for the binary itself).
// The package honors $XDG_CONFIG_HOME and $XDG_DATA_HOME when set and
// falls back to the standard defaults (~/.config, ~/.local/share).
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Expand resolves a leading "~" or "~/" to the current user's home
// directory. Other paths are returned unchanged (but cleaned).
func Expand(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	// Allow $HOME-prefixed strings used by systemd unit specifiers
	// (we never see them here, but be defensive).
	if strings.HasPrefix(p, "$HOME/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[len("$HOME/"):]), nil
	}
	return filepath.Clean(p), nil
}

func home() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home, nil
}

// Home returns the user's home directory.
func Home() (string, error) { return home() }

// ConfigDir returns the directory holding freshy's config.
// Honors $XDG_CONFIG_HOME, defaulting to ~/.config/freshy.
func ConfigDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "freshy"), nil
	}
	h, err := home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "freshy"), nil
}

// ConfigFile returns the absolute path to config.toml.
func ConfigFile() (string, error) {
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.toml"), nil
}

// DataDir returns ~/.local/share/freshy (or $XDG_DATA_HOME/freshy).
func DataDir() (string, error) {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "freshy"), nil
	}
	h, err := home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".local", "share", "freshy"), nil
}

// StateDir returns the per-package state directory.
func StateDir() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "state"), nil
}

// ReposDir returns the directory holding git clones.
func ReposDir() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "repos"), nil
}

// BuildsDir returns the directory holding staged binaries pre-deploy.
func BuildsDir() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "builds"), nil
}

// LogsDir returns the log directory.
func LogsDir() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "logs"), nil
}

// RepoDir returns the path to the cloned repo for one package.
func RepoDir(pkg string) (string, error) {
	d, err := ReposDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, pkg), nil
}

// StateFile returns the per-package state file path.
func StateFile(pkg string) (string, error) {
	d, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, pkg+".json"), nil
}

// PackageBuildsDir returns builds/<pkg>/.
func PackageBuildsDir(pkg string) (string, error) {
	d, err := BuildsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, pkg), nil
}

// EnsureDir creates dir (and parents) if missing. Returns the resolved
// absolute path.
func EnsureDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir, nil
	}
	return abs, nil
}

// BinDir returns the resolved target directory where binaries are placed.
// Defaults to ~/.local/bin, but may be overridden via the install_to setting.
func BinDir(installTo string) (string, error) {
	if installTo == "" {
		installTo = "~/.local/bin"
	}
	return Expand(installTo)
}
