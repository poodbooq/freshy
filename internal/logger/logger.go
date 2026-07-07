// Package logger is a minimal text-file logger for freshy.
//
// One log file per process is fine: we append, with timestamps. The
// log level is implicit via method name (Infof / Warnf / Errorf).
// The output is also mirrored to stderr so systemd captures context
// in `journalctl --user -u freshy.service`.
package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger writes timestamped lines to a file and optionally a second
// sink (typically stderr). Safe for concurrent use.
type Logger struct {
	mu    sync.Mutex
	out   io.WriteCloser
	mirror io.Writer
}

// New opens (or creates/appends) the file at path. The parent
// directory must already exist.
func New(path string) (*Logger, error) {
	if path == "" {
		return nil, fmt.Errorf("logger: empty path")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", path, err)
	}
	return &Logger{out: f, mirror: os.Stderr}, nil
}

// NewNull discards everything; useful in tests and `--quiet` modes.
func NewNull() *Logger {
	return &Logger{out: nopCloser{}, mirror: io.Discard}
}

type nopCloser struct{}

func (nopCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopCloser) Close() error               { return nil }

// Close the underlying file. Errors are swallowed (best-effort).
func (l *Logger) Close() error {
	if l == nil || l.out == nil {
		return nil
	}
	return l.out.Close()
}

// writef is the internal helper. Format is: "2006-01-02 15:04:05.000 LEVEL msg".
func (l *Logger) writef(level, format string, args ...any) {
	if l == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	line := fmt.Sprintf("%s %s %s\n", ts, level, msg)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.out != nil {
		_, _ = l.out.Write([]byte(line))
	}
	if l.mirror != nil {
		_, _ = l.mirror.Write([]byte(line))
	}
}

// Infof logs an informational message.
func (l *Logger) Infof(format string, args ...any) { l.writef("INFO ", format, args...) }

// Warnf logs a warning.
func (l *Logger) Warnf(format string, args ...any) { l.writef("WARN ", format, args...) }

// Errorf logs an error.
func (l *Logger) Errorf(format string, args ...any) {
	l.writef("ERROR", format, args...)
}

// EnsureDir is a small helper that creates the parent of a log file.
// Used by callers when they construct the path themselves.
func EnsureDir(p string) error {
	return os.MkdirAll(filepath.Dir(p), 0o755)
}
