package auth

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// deviceProbeTimeout bounds the OS-description subcommands below. brick-cli
// runs them unbounded, but here login is driven from the UI, so a wedged
// lsb_release must not hang the login button — a probe that times out just
// leaves the OS name out of the device name.
const deviceProbeTimeout = 2 * time.Second

// DeviceName returns a human-readable name for this device, for display in
// account-hq's device list: "Brick Desktop on {host}", plus a parenthesized OS
// description when one can be determined (e.g. "Brick Desktop on myhost
// (Debian GNU/Linux 13)"). Returns "" if the hostname can't be determined, in
// which case the server falls back to the request's User-Agent header instead.
//
// Mirrors brick-cli's deviceName, with "Brick CLI" replaced by "Brick Desktop"
// so the two apps show up as separate devices on the same machine.
func DeviceName() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	if osName := deviceOSName(); osName != "" {
		return fmt.Sprintf("Brick Desktop on %s (%s)", name, osName)
	}
	return fmt.Sprintf("Brick Desktop on %s", name)
}

// deviceOSName returns a human-readable OS description for this device, or ""
// if one can't be determined (in which case DeviceName falls back to the bare
// hostname, matching brick-cli's behavior for OSes not handled below or where
// the relevant command isn't installed).
func deviceOSName() string {
	ctx, cancel := context.WithTimeout(context.Background(), deviceProbeTimeout)
	defer cancel()

	switch runtime.GOOS {
	case "linux":
		// lsb_release isn't installed on every distro (notably minimal/
		// container images), in which case we just omit the OS name.
		out, err := exec.CommandContext(ctx, "lsb_release", "-ds").Output()
		if err != nil {
			return ""
		}
		return strings.Trim(strings.TrimSpace(string(out)), `"`)
	case "darwin":
		productName, nameErr := exec.CommandContext(ctx, "sw_vers", "-productName").Output()
		productVersion, versionErr := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output()
		if nameErr != nil || versionErr != nil {
			return "macOS"
		}
		return strings.TrimSpace(string(productName)) + " " + strings.TrimSpace(string(productVersion))
	default:
		return ""
	}
}
