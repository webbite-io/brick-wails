package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/webbite-io/brick-wails/internal/logfile"
)

// Every line openLog writes to brick.log carries a dashed "YYYY-MM-DD
// HH:ii:ss" stamp and the "UI: " tag — the same shape brick-cli stamps its
// own lines with, since both apps append to this one file. The log package's
// slashed default ("2026/09/30 17:37:08") must not appear.
func TestOpenLogWritesDashedTimestamps(t *testing.T) {
	dir := t.TempDir()
	lg := openLog(dir, false)
	lg.Printf("▶ sync resumed")
	lg.Printf("syncing %s (account %s)", "/home/someone/Brick", "acct-1")

	data, err := os.ReadFile(filepath.Join(dir, logfile.Name))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)

	want := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} UI: ▶ sync resumed\n` +
		`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} UI: syncing /home/someone/Brick \(account acct-1\)\n$`)
	if !want.MatchString(got) {
		t.Errorf("brick.log =\n%q\nwant lines matching %v", got, want)
	}
	if regexp.MustCompile(`\d{4}/\d{2}/\d{2}`).MatchString(got) {
		t.Errorf("brick.log =\n%q\nstill contains the log package's slashed date", got)
	}
}

// A log file that can't be opened must not take the app down with it: the
// logger still works, it just has nowhere to put the lines.
func TestOpenLogSurvivesAnUnwritableDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	lg := openLog(filepath.Join(dir, "nested"), false)
	lg.Printf("this has nowhere to go") // must not panic
}
