// Package editor resolves which editor to use and invokes it on a
// given file. It honours $VISUAL first, then $EDITOR, then falls
// back to common system defaults (vi, nano — whichever exists).
//
// The intent is "best effort": if the user has $EDITOR set, use it;
// otherwise pick something. We never refuse to launch an editor
// because we couldn't guess one.
package editor

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Open asks the user-chosen editor to edit path. The editor runs in
// the foreground and inherits stdin/stdout/stderr.
//
// Returns nil if the editor exited 0. Returns an error otherwise.
// We do not check that the file was actually modified — that is up
// to freshy's caller to decide (e.g. by comparing mtime/inode).
func Open(path string) error {
	cmd, err := resolve()
	if err != nil {
		return err
	}
	cmd.Args = append(cmd.Args, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// resolve returns the editor command to invoke. Honours $VISUAL,
// then $EDITOR, then tries each of the fallbacks in order.
func resolve() (*exec.Cmd, error) {
	if v := strings.TrimSpace(os.Getenv("VISUAL")); v != "" {
		return exec.Command(v), nil
	}
	if e := strings.TrimSpace(os.Getenv("EDITOR")); e != "" {
		return exec.Command(e), nil
	}
	// Common fallbacks.
	for _, candidate := range []string{"vi", "nano", "vim", "micro"} {
		if p, err := exec.LookPath(candidate); err == nil {
			return exec.Command(p), nil
		}
	}
	return nil, fmt.Errorf("no editor found: set $EDITOR or install one of vi/nano/vim/micro")
}
