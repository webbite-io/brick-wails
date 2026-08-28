package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// This file runs brick subcommands that need a real, interactive terminal
// — brick's guided setup (`--setup-and-exit`) can prompt for login and
// confirmations, so its output can't just be captured and mirrored into our
// own UI the way runStreamed does for non-interactive commands. Instead we
// write a small wrapper script that runs the command and records its exit
// code to a file, then open that script in whatever native terminal
// emulator is available, and wait for the exit-code file to appear.
//
// Waiting on a file rather than on the launching process itself is
// deliberate: several terminal emulators (gnome-terminal chief among them)
// hand off to an already-running server process and return immediately,
// so there's no child process left to Wait() on that would tell us
// anything useful.

const (
	terminalPollInterval = 400 * time.Millisecond
	terminalWaitTimeout  = 30 * time.Minute
)

// runInteractive runs binPath with args inside a new native terminal
// window so the user can see prompts and respond to them, and returns its
// exit code once it finishes.
func runInteractive(binPath string, args []string) (int, error) {
	tmpDir, err := os.MkdirTemp("", "brick-terminal-*")
	if err != nil {
		return -1, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	resultPath := filepath.Join(tmpDir, "exit-code")
	scriptPath, err := writeTerminalScript(tmpDir, binPath, args, resultPath)
	if err != nil {
		return -1, err
	}

	termCmd, err := terminalCommand(scriptPath)
	if err != nil {
		return -1, err
	}
	if err := termCmd.Start(); err != nil {
		return -1, fmt.Errorf("opening terminal: %w", err)
	}
	// Reap it in the background; we don't wait on it directly (see the
	// package comment above for why) and don't want a zombie left behind
	// on the platforms where it does stay a direct child (xterm & co).
	go termCmd.Wait()

	deadline := time.Now().Add(terminalWaitTimeout)
	for {
		if data, err := os.ReadFile(resultPath); err == nil {
			code, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr != nil {
				return -1, fmt.Errorf("reading terminal result: %w", convErr)
			}
			return code, nil
		}
		if time.Now().After(deadline) {
			return -1, errors.New("timed out waiting for the terminal window to finish")
		}
		time.Sleep(terminalPollInterval)
	}
}

// writeTerminalScript writes a small wrapper script that runs binPath with
// args, then writes its exit code to resultPath, then exits (so the
// terminal window closes once it's done).
func writeTerminalScript(dir, binPath string, args []string, resultPath string) (string, error) {
	if runtime.GOOS == "windows" {
		return writeWindowsScript(dir, binPath, args, resultPath)
	}
	return writeUnixScript(dir, binPath, args, resultPath)
}

func writeUnixScript(dir, binPath string, args []string, resultPath string) (string, error) {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(shellQuote(binPath))
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(shellQuote(a))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "echo $? > %s\n", shellQuote(resultPath))
	b.WriteString("exit\n")

	path := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(path, []byte(b.String()), 0o700); err != nil {
		return "", err
	}
	return path, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func writeWindowsScript(dir, binPath string, args []string, resultPath string) (string, error) {
	var b strings.Builder
	b.WriteString("@echo off\r\n")
	b.WriteString(batchQuote(binPath))
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(batchQuote(a))
	}
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "echo %%errorlevel%% > %s\r\n", batchQuote(resultPath))
	b.WriteString("exit\r\n")

	path := filepath.Join(dir, "run.bat")
	if err := os.WriteFile(path, []byte(b.String()), 0o700); err != nil {
		return "", err
	}
	return path, nil
}

func batchQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// terminalCommand returns the platform-specific command that opens
// scriptPath (already executable/associated) in a new, visible terminal
// window.
func terminalCommand(scriptPath string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "windows":
		return windowsTerminalCommand(scriptPath), nil
	case "darwin":
		return darwinTerminalCommand(scriptPath), nil
	default:
		return linuxTerminalCommand(scriptPath)
	}
}

// windowsTerminalCommand uses cmd.exe's "start" to open a new console
// window running the batch script. The quoted "Brick Setup" argument is
// required by "start"'s own quirky parsing — without a title argument it
// would otherwise treat a quoted script path as the window title instead
// of the command to run.
func windowsTerminalCommand(scriptPath string) *exec.Cmd {
	return exec.Command("cmd", "/C", "start", "Brick Setup", scriptPath)
}

// darwinTerminalCommand asks Terminal.app to run the script via AppleScript.
// Terminal.app's default profile setting ("When the shell exits: Close if
// the shell exited cleanly") closes the window once the script's trailing
// `exit` runs; if the user has changed that setting, the window will just
// stay open showing the finished command, which is a harmless fallback.
func darwinTerminalCommand(scriptPath string) *exec.Cmd {
	shellCmd := "sh " + shellQuote(scriptPath)
	script := fmt.Sprintf(
		"tell application \"Terminal\"\n\tactivate\n\tdo script %s\nend tell",
		appleScriptQuote(shellCmd),
	)
	return exec.Command("osascript", "-e", script)
}

func appleScriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// linuxTerminalCandidates lists terminal emulators to try, in order, along
// with the args that tell each one to run scriptPath as a single command
// (rather than opening an interactive shell) — since the script ends with
// `exit`, this is what makes the window close on its own once it's done.
var linuxTerminalCandidates = []struct {
	bin  string
	args func(scriptPath string) []string
}{
	{"x-terminal-emulator", func(p string) []string { return []string{"-e", p} }},
	{"gnome-terminal", func(p string) []string { return []string{"--", p} }},
	{"konsole", func(p string) []string { return []string{"-e", p} }},
	{"xfce4-terminal", func(p string) []string { return []string{"-x", p} }},
	{"tilix", func(p string) []string { return []string{"-e", p} }},
	{"alacritty", func(p string) []string { return []string{"-e", p} }},
	{"kitty", func(p string) []string { return []string{p} }},
	{"foot", func(p string) []string { return []string{p} }},
	{"terminator", func(p string) []string { return []string{"-x", p} }},
	{"xterm", func(p string) []string { return []string{"-e", p} }},
}

// linuxTerminalCommand tries $TERMINAL first (if set and it resolves to a
// real executable), then a fixed list of common terminal emulators — there
// is no single standard terminal across Linux desktop environments.
func linuxTerminalCommand(scriptPath string) (*exec.Cmd, error) {
	if term := os.Getenv("TERMINAL"); term != "" {
		if path, err := exec.LookPath(term); err == nil {
			return exec.Command(path, "-e", scriptPath), nil
		}
	}
	for _, candidate := range linuxTerminalCandidates {
		if path, err := exec.LookPath(candidate.bin); err == nil {
			return exec.Command(path, candidate.args(scriptPath)...), nil
		}
	}
	return nil, errors.New("no terminal emulator found on this system")
}
