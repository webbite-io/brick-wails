package update

import (
	"fmt"
	"os/exec"
	"runtime"
)

// InstallCommand is the shell command the "Update" button hands off to a
// terminal — the public installer for the desktop app (brick-cli's own
// install.sh lives in its repo; this one is served from webbite.io since the
// desktop app isn't always installed alongside a git checkout).
const InstallCommand = "curl -fsSL https://webbite.io/desktop/appimage/install.sh | bash"

// OpenInTerminal launches the platform's default terminal running
// InstallCommand and returns once the terminal process has started — not
// once the script finishes. The terminal is left open afterwards (a prompt
// to press a key) so the user can see the script's output, including any
// error, the same way brick-cli prints its own success/failure line.
//
// Not used on macOS, where Sparkle installs updates (see updates_darwin.go
// in the main package).
func OpenInTerminal() error {
	switch runtime.GOOS {
	case "darwin":
		return fmt.Errorf("macOS updates are installed by Sparkle, not a script")
	case "windows":
		return openWindows()
	default:
		return openLinux()
	}
}

// keepOpen appends a pause to a shell command so the terminal stays up after
// the script exits, whatever its exit code.
func keepOpen(shCmd string) string {
	return shCmd + "; echo; read -n1 -r -p 'Press any key to close…'"
}

func openWindows() error {
	// "start" opens a new console window; /k keeps it open after the command
	// finishes. The empty "" is the window title argument `start` expects
	// before the command it should run.
	return exec.Command("cmd", "/C", "start", "", "cmd", "/K", InstallCommand).Start()
}

// linuxTerminal describes how to hand a shell command to one terminal
// emulator's argv (each has its own convention for "run this and exit").
type linuxTerminal struct {
	bin  string
	args func(shCmd string) []string
}

var linuxTerminals = []linuxTerminal{
	// x-terminal-emulator is the Debian/Ubuntu alternatives-system default,
	// so it's tried first — it resolves to whatever the user actually set as
	// their preferred terminal.
	{"x-terminal-emulator", func(c string) []string { return []string{"-e", "bash", "-c", c} }},
	{"gnome-terminal", func(c string) []string { return []string{"--", "bash", "-c", c} }},
	{"konsole", func(c string) []string { return []string{"-e", "bash", "-c", c} }},
	{"xfce4-terminal", func(c string) []string { return []string{"-x", "bash", "-c", c} }},
	{"kitty", func(c string) []string { return []string{"bash", "-c", c} }},
	{"alacritty", func(c string) []string { return []string{"-e", "bash", "-c", c} }},
	{"xterm", func(c string) []string { return []string{"-e", "bash", "-c", c} }},
}

func openLinux() error {
	shCmd := keepOpen(InstallCommand)
	for _, t := range linuxTerminals {
		path, err := exec.LookPath(t.bin)
		if err != nil {
			continue
		}
		return exec.Command(path, t.args(shCmd)...).Start()
	}
	return fmt.Errorf("no terminal emulator found (tried x-terminal-emulator, gnome-terminal, konsole, xfce4-terminal, kitty, alacritty, xterm)")
}
