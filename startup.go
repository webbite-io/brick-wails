package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// errBrickBinaryNotFound is returned by locateBrick when the brick CLI
// can't be found on PATH or at the well-known ~/.local/bin fallback.
var errBrickBinaryNotFound = errors.New("brick binary not found")

// BrickSelfTestCheck is one entry in the "checks" array of `brick
// --self-test`'s JSON report.
type BrickSelfTestCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// BrickSelfTestResult is the JSON brick prints for
// `brick --self-test --no-upgrade-check`.
type BrickSelfTestResult struct {
	Status  string               `json:"status"`
	Version string               `json:"version"`
	Ready   bool                 `json:"ready"`
	Checks  []BrickSelfTestCheck `json:"checks"`
}

// BrickLocateResult reports whether the brick CLI binary could be found.
type BrickLocateResult struct {
	Found bool   `json:"found"`
	Path  string `json:"path"`
}

// BrickRunResult is the outcome of running a brick subcommand to
// completion (setup or install) — for callers that need the final exit
// code rather than the live output stream.
type BrickRunResult struct {
	ExitCode int  `json:"exitCode"`
	Ok       bool `json:"ok"`
}

// locateBrick looks for the brick binary on PATH first (this also finds
// brick.exe on Windows, since exec.LookPath consults PATHEXT there), then
// falls back to the well-known ~/.local/bin/brick location that brick's
// own install.sh uses on Linux/macOS.
func locateBrick() (string, error) {
	if path, err := exec.LookPath("brick"); err == nil {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".local", "bin", "brick")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errBrickBinaryNotFound
}

// runBrickSelfTest runs `brick --self-test --no-upgrade-check` and parses
// its JSON report. brick prints the report to stdout regardless of the
// outcome (ready:false is a normal, expected result), so a non-zero exit
// code alone is not treated as an error here — only a report that can't be
// parsed at all is.
func runBrickSelfTest(binPath string) (*BrickSelfTestResult, error) {
	cmd := exec.Command(binPath, "--self-test", "--no-upgrade-check")
	out, runErr := cmd.Output()

	var result BrickSelfTestResult
	if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("running brick --self-test: %w", runErr)
		}
		return nil, fmt.Errorf("parsing brick --self-test output: %w", err)
	}
	return &result, nil
}

// lineWriter buffers arbitrary writes and calls onLine once per complete
// line, so a subprocess's streamed stdout/stderr can be forwarded to the
// frontend as discrete log lines instead of raw byte chunks. Safe for
// concurrent use since stdout and stderr are written from separate
// goroutines by os/exec.
type lineWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	onLine func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		data := w.buf.Bytes()
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(data[:i]), "\r")
		w.buf.Next(i + 1)
		w.onLine(line)
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		w.onLine(strings.TrimRight(w.buf.String(), "\r\n"))
		w.buf.Reset()
	}
}

// runStreamed runs cmd to completion, forwarding its combined stdout/stderr
// to onLine one line at a time, and returns its exit code. A non-zero exit
// code is a normal, expected outcome (setup/install can legitimately fail)
// — only a failure to start the process at all is returned as an error.
func runStreamed(cmd *exec.Cmd, onLine func(string)) (int, error) {
	lw := &lineWriter{onLine: onLine}
	cmd.Stdout = lw
	cmd.Stderr = lw

	err := cmd.Run()
	lw.flush()

	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

// startBrickDaemon launches brick's long-running sync process, detached
// from this app (see detachProcess) so it keeps running independently of
// the tray UI's lifetime. It doesn't wait for brick to exit; a background
// goroutine reaps the process once it eventually does, to avoid leaving a
// zombie behind.
func startBrickDaemon(binPath string) error {
	cmd := exec.Command(binPath, "--no-upgrade-check")
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// installCommand returns the platform-specific command used to install the
// brick CLI: install.sh on Linux/macOS, winget on Windows.
func installCommand() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("winget", "install", "--id", "Webbite.Brick", "-e")
	}
	return exec.Command("sh", "-c", "curl -fsSL https://webbite.io/cli/install.sh | bash")
}

// StartupService drives the startup status window: checking whether brick
// is already running, self-testing and (re-)running setup when it isn't,
// and installing the CLI when it's missing entirely. Bound to the frontend
// via application.NewService in main.go; every exported method here is
// callable from TypeScript through the generated bindings.
type StartupService struct {
	app *application.App
}

// emit is a no-op if app hasn't been assigned yet (it's set right after
// application.New in main.go, before app.Run — well before any of these
// methods can be invoked from the frontend).
func (s *StartupService) emit(name string, data any) {
	if s.app != nil {
		s.app.Event.Emit(name, data)
	}
}

// LocateBrick reports whether the brick CLI binary can be found, and where.
func (s *StartupService) LocateBrick() BrickLocateResult {
	path, err := locateBrick()
	if err != nil {
		return BrickLocateResult{Found: false}
	}
	return BrickLocateResult{Found: true, Path: path}
}

// SelfTest runs `brick --self-test --no-upgrade-check` and returns its
// parsed report.
func (s *StartupService) SelfTest() (BrickSelfTestResult, error) {
	binPath, err := locateBrick()
	if err != nil {
		return BrickSelfTestResult{}, err
	}
	result, err := runBrickSelfTest(binPath)
	if err != nil {
		return BrickSelfTestResult{}, err
	}
	return *result, nil
}

// RunSetup runs `brick --setup-and-exit` inside a new native terminal
// window — brick's guided setup is interactive (it can prompt for login,
// confirmations, etc.), so it needs a real TTY rather than the captured
// output runStreamed uses for non-interactive commands. Resolves once the
// terminal-run command exits (see terminal.go).
func (s *StartupService) RunSetup() (BrickRunResult, error) {
	binPath, err := locateBrick()
	if err != nil {
		return BrickRunResult{}, err
	}
	code, err := runInteractive(binPath, []string{"--setup-and-exit"})
	if err != nil {
		return BrickRunResult{}, err
	}
	return BrickRunResult{ExitCode: code, Ok: code == 0}, nil
}

// StartBrick launches brick's sync daemon in the background so it keeps
// running independently of this app.
func (s *StartupService) StartBrick() error {
	binPath, err := locateBrick()
	if err != nil {
		return err
	}
	return startBrickDaemon(binPath)
}

// InstallBrick runs the platform-specific install command, streaming its
// output line by line as "brick:install-output" events, and resolves once
// it exits.
func (s *StartupService) InstallBrick() (BrickRunResult, error) {
	code, err := runStreamed(installCommand(), func(line string) {
		s.emit("brick:install-output", line)
	})
	if err != nil {
		return BrickRunResult{}, err
	}
	return BrickRunResult{ExitCode: code, Ok: code == 0}, nil
}

// QuitApp exits the whole app — used by the startup window's "Close Brick"
// action, when the user declines to install the CLI.
func (s *StartupService) QuitApp() {
	if s.app != nil {
		s.app.Quit()
	}
}
