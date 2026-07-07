// Package state holds per-package on-disk state.
//
// We persist just enough to make future runs cheap and to give the user
// visible context via `freshy status`. The file lives at
// $XDG_DATA_HOME/freshy/state/<pkg>.json.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/poodbooq/freshy/internal/paths"
)

// State is the persisted state for one package.
type State struct {
	Name           string    `json:"name"`
	LastSHA        string    `json:"last_sha"`         // HEAD SHA we last installed
	LastSyncedAt   time.Time `json:"last_synced_at"`   // when deploy succeeded
	LastCheckedAt  time.Time `json:"last_checked_at"`  // when we last saw a matching HEAD
	LastError      string    `json:"last_error"`       // most recent error message ("" = healthy)
	LastErrorAt    time.Time `json:"last_error_at"`    // when the error occurred
	RepoLocalPath  string    `json:"repo_local_path"`  // resolved clone path
}

// cache prevents concurrent Load()/Save() of the same file within one
// run. A single freshy invocation handles each package once anyway,
// but it doesn't hurt to be correct.
var cache sync.Map // map[string]*State

// Load reads the state file for `pkg`. Returns an empty State seeded
// with `pkg` if the file does not yet exist.
func Load(pkg string) (*State, error) {
	path, err := paths.StateFile(pkg)
	if err != nil {
		return nil, err
	}
	if v, ok := cache.Load(path); ok {
		return v.(*State), nil
	}
	s := &State{Name: pkg}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cache.Store(path, s)
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	if s.Name == "" {
		s.Name = pkg
	}
	cache.Store(path, s)
	return s, nil
}

// Save atomically writes the state file.
func (s *State) Save() error {
	path, err := paths.StateFile(s.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state.*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
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

// MarkChecked updates LastCheckedAt without changing LastSHA.
// Used when we looked but found no new HEAD.
func (s *State) MarkChecked(sha string) {
	s.LastCheckedAt = time.Now().UTC()
	// We deliberately don't update LastSHA here; it represents
	// the SHA we last *successfully deployed*, not merely observed.
	_ = sha
}

// RecordInstallSuccess marks a successful sync.
func (s *State) RecordInstallSuccess(sha string) {
	now := time.Now().UTC()
	s.LastSHA = sha
	s.LastSyncedAt = now
	s.LastCheckedAt = now
	s.LastError = ""
	s.LastErrorAt = time.Time{}
}

// RecordError stores the latest error message and timestamp without
// touching LastSHA / LastSyncedAt.
func (s *State) RecordError(msg string) {
	now := time.Now().UTC()
	s.LastError = msg
	s.LastErrorAt = now
	s.LastCheckedAt = now
}
