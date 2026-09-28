// Package logfile is where this app's log output goes: <configDir>/brick.log,
// the very same rolling file brick-cli writes, capped at the same 10,000
// lines — one log per user, whichever app produced the lines.
//
// Ported from brick-cli cmd/brick/synclog.go @ ea5ac8a. Differences from the
// CLI, both deliberate, and both because this app is long-lived (a tray app
// running while a `brick sync` may come and go) where the CLI's writer only
// lives for the length of one sync:
//   - Trimming rewrites the file in place rather than renaming a fresh file
//     over it, so a trim here never leaves a running brick-cli appending to
//     an unlinked file.
//   - Each write first checks that the file we hold is still the one at
//     brick.log, reopening it if not, so a trim (or a delete) by brick-cli
//     doesn't silently swallow the rest of this run's output.
package logfile

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Name is the log file's name inside the config directory. Shared with
// brick-cli's syncLogFileName.
const Name = "brick.log"

// MaxLines is the most lines brick.log is allowed to hold — it's a rolling
// record of recent activity, not an ever-growing file, so once full the
// oldest lines are dropped to make room for new ones. Same cap as
// brick-cli's syncLogMaxLines.
const MaxLines = 10000

// Writer appends to <configDir>/brick.log, keeping it capped at roughly
// MaxLines lines. Safe for concurrent use.
type Writer struct {
	mu    sync.Mutex
	path  string
	f     *os.File
	lines int
}

// Open opens (creating if necessary) <configDir>/brick.log for appending,
// creating the config directory too if it isn't there yet.
func Open(dir string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("could not create config directory: %w", err)
	}
	path := filepath.Join(dir, Name)
	f, err := openAppend(path)
	if err != nil {
		return nil, err
	}
	lines, err := countLines(path)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Writer{path: path, f: f, lines: lines}, nil
}

// Path returns the file being written.
func (w *Writer) Path() string { return w.path }

// Write appends p, trimming the file back down to MaxLines lines whenever it
// grows past the cap.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.reopenIfReplacedLocked(); err != nil {
		return 0, err
	}
	n, err := w.f.Write(p)
	if err != nil {
		return n, err
	}
	w.lines += bytes.Count(p, []byte{'\n'})

	if w.lines > MaxLines {
		if trimErr := w.trimLocked(); trimErr != nil {
			return n, trimErr
		}
	}
	return n, nil
}

// Close closes the underlying file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// openAppend opens path for appending, creating it with 0600 if absent. The
// O_APPEND is what lets brick-cli write the same file at the same time: every
// write lands at the end of the file as it is right then, not at an offset we
// remember.
func openAppend(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("could not open %s: %w", path, err)
	}
	return f, nil
}

// reopenIfReplacedLocked reopens brick.log when the file we hold is no longer
// the file at that path — an older brick-cli trimming by rename, or someone
// deleting the log — so output doesn't disappear into an unlinked file for
// the rest of this run. Callers must hold w.mu.
func (w *Writer) reopenIfReplacedLocked() error {
	onDisk, statErr := os.Stat(w.path)
	if statErr == nil {
		if ours, err := w.f.Stat(); err == nil && os.SameFile(onDisk, ours) {
			return nil
		}
	}
	f, err := openAppend(w.path)
	if err != nil {
		return err
	}
	lines, err := countLines(w.path)
	if err != nil {
		f.Close()
		return err
	}
	w.f.Close()
	w.f, w.lines = f, lines
	return nil
}

// trimLocked rewrites the log file to keep only its last MaxLines lines. The
// rewrite is in place, on the same inode, so a brick-cli appending to this
// file keeps appending to the file that's still at brick.log. Callers must
// hold w.mu.
func (w *Writer) trimLocked() error {
	data, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) <= MaxLines {
		// Someone else (a running brick-cli) already trimmed it.
		w.lines = len(lines)
		return nil
	}
	lines = lines[len(lines)-MaxLines:]

	var buf bytes.Buffer
	for _, line := range lines {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}

	// A separate handle: WriteAt refuses to work on an O_APPEND file, and
	// w.f has to stay O_APPEND for the sake of concurrent writers.
	f, err := os.OpenFile(w.path, os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("could not open %s: %w", w.path, err)
	}
	defer f.Close()
	if _, err := f.WriteAt(buf.Bytes(), 0); err != nil {
		return err
	}
	if err := f.Truncate(int64(buf.Len())); err != nil {
		return err
	}
	w.lines = len(lines)
	return nil
}

// countLines returns the number of newline-terminated lines in path.
func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		count++
	}
	return count, scanner.Err()
}
