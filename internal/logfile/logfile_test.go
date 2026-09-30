package logfile

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readLines returns the log's lines, without the trailing empty one.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func TestOpenCreatesLogInConfigDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "brick")
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if want := filepath.Join(dir, "brick.log"); w.Path() != want {
		t.Fatalf("path = %q, want %q", w.Path(), want)
	}
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, w.Path()); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("lines = %q", got)
	}
}

func TestWriteAppendsToExistingLog(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "brick.log"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, w.Path()); len(got) != 2 || got[0] != "old" || got[1] != "new" {
		t.Fatalf("lines = %q", got)
	}
}

// The cap: the file holds the newest MaxLines lines and the oldest are gone.
func TestWriteCapsAtMaxLines(t *testing.T) {
	w, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	const extra = 50
	for i := 1; i <= MaxLines+extra; i++ {
		if _, err := fmt.Fprintf(w, "line %d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	lines := readLines(t, w.Path())
	if len(lines) != MaxLines {
		t.Fatalf("len(lines) = %d, want %d", len(lines), MaxLines)
	}
	if want := fmt.Sprintf("line %d", extra+1); lines[0] != want {
		t.Errorf("first line = %q, want %q", lines[0], want)
	}
	if want := fmt.Sprintf("line %d", MaxLines+extra); lines[len(lines)-1] != want {
		t.Errorf("last line = %q, want %q", lines[len(lines)-1], want)
	}
}

// Trimming must not replace the file: brick-cli may be appending to it, and a
// rename would leave it writing to an unlinked file.
func TestTrimKeepsTheSameFile(t *testing.T) {
	w, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	before, err := os.Stat(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	// A second appender, standing in for a running `brick sync`.
	cli, err := os.OpenFile(w.Path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	for i := 0; i <= MaxLines; i++ {
		if _, err := fmt.Fprintf(w, "line %d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("brick.log was replaced by the trim; a concurrent brick-cli would lose its output")
	}
	if _, err := cli.WriteString("from the cli\n"); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, w.Path())
	if len(lines) == 0 || lines[len(lines)-1] != "from the cli" {
		t.Fatalf("the other writer's line didn't land in brick.log: last line = %q", lines[len(lines)-1])
	}
}

// The other direction: an older brick-cli trims by renaming a new file over
// brick.log, so the file we hold is unlinked. The next write must reopen.
func TestWriteReopensWhenTheLogIsReplaced(t *testing.T) {
	w, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("ours\n")); err != nil {
		t.Fatal(err)
	}

	tmp := w.Path() + ".tmp"
	if err := os.WriteFile(tmp, []byte("trimmed by the cli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, w.Path()); err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write([]byte("after the trim\n")); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, w.Path())
	if len(lines) != 2 || lines[0] != "trimmed by the cli" || lines[1] != "after the trim" {
		t.Fatalf("lines = %q", lines)
	}
}

func TestWriteReopensWhenTheLogIsDeleted(t *testing.T) {
	w, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("before\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(w.Path()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, w.Path()); len(got) != 1 || got[0] != "after" {
		t.Fatalf("lines = %q", got)
	}
}

// A line count inherited from an already-full log still caps immediately.
func TestOpenInheritsLineCount(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < MaxLines; i++ {
		fmt.Fprintf(&b, "old %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "brick.log"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("fresh\n")); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, w.Path())
	if len(lines) != MaxLines {
		t.Fatalf("len(lines) = %d, want %d", len(lines), MaxLines)
	}
	if lines[0] != "old 1" || lines[len(lines)-1] != "fresh" {
		t.Fatalf("first = %q, last = %q", lines[0], lines[len(lines)-1])
	}
}

func TestTimestampWriterStampsLinesWithDashedDate(t *testing.T) {
	var buf bytes.Buffer
	lg := log.New(NewTimestampWriter(&buf), "UI: ", 0)
	lg.Printf("▶ sync resumed")

	got := buf.String()
	if !strings.HasSuffix(got, "UI: ▶ sync resumed\n") {
		t.Errorf("line = %q, want it to end with the tagged message", got)
	}
	stamp, _, ok := strings.Cut(strings.TrimSuffix(got, "UI: ▶ sync resumed\n"), " UI")
	if !ok {
		stamp = strings.TrimSpace(strings.TrimSuffix(got, "UI: ▶ sync resumed\n"))
	}
	if _, err := time.Parse(TimestampLayout, strings.TrimSpace(stamp)); err != nil {
		t.Errorf("timestamp %q does not parse as %q: %v", stamp, TimestampLayout, err)
	}
	if strings.Contains(got, "/") {
		t.Errorf("line = %q, want a dashed date, not the log package's slashed default", got)
	}
}

// The writer must report the caller's own byte count, not its stamped copy's
// — log.Logger treats a short write as a failure.
func TestTimestampWriterReportsCallerLength(t *testing.T) {
	var buf bytes.Buffer
	w := NewTimestampWriter(&buf)
	p := []byte("hello\n")
	n, err := w.Write(p)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(p) {
		t.Errorf("n = %d, want %d (the caller's length)", n, len(p))
	}
}
