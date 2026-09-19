//go:build integration && unix

package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/webbite-io/brick-wails/internal/controlapi"
	"github.com/webbite-io/brick-wails/internal/runner"
)

// These tests build the real brick CLI from a checkout (BRICK_CLI_DIR,
// default ../brick-cli) and run it against the same isolated HOME and fake
// servers as the app. They are skipped when no checkout is available.

var (
	cliOnce sync.Once
	cliPath string
	cliErr  string
	// origEnv is captured before any test redirects HOME, so building the CLI
	// uses the real Go module/build caches instead of filling a temp HOME.
	origEnv = os.Environ()
)

func brickCLI(t *testing.T) string {
	t.Helper()
	cliOnce.Do(func() {
		dir := os.Getenv("BRICK_CLI_DIR")
		if dir == "" {
			dir = filepath.Join("..", "..", "brick-cli")
		}
		if _, err := os.Stat(filepath.Join(dir, "cmd", "brick")); err != nil {
			cliErr = "no brick-cli checkout at " + dir + " (set BRICK_CLI_DIR)"
			return
		}
		out, err := os.MkdirTemp("", "brick-cli-bin-")
		if err != nil {
			cliErr = err.Error()
			return
		}
		cliPath = filepath.Join(out, "brick")
		cmd := exec.Command("go", "build", "-o", cliPath, "./cmd/brick")
		cmd.Dir = dir
		cmd.Env = append(origEnv, "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			cliErr = "building brick-cli failed: " + err.Error() + "\n" + string(b)
			cliPath = ""
		}
	})
	if cliPath == "" {
		t.Skip(cliErr)
	}
	return cliPath
}

// cli runs brick with the world's isolated HOME and fake endpoints. The
// working directory is a temp dir so a brick-cli .env can't leak in.
func (w *world) cli(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := w.cliCmd(t, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (w *world) cliCmd(t *testing.T, args ...string) *exec.Cmd {
	cmd := exec.Command(brickCLI(t), args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+w.home,
		"XDG_RUNTIME_DIR="+w.home,
		"ACC_API_URL="+w.env.APIURL,
		"STORAGE_API_URL="+w.env.StorageAPIURL,
		"OAUTH_CLIENT_ID="+w.env.OAuthClientID,
	)
	return cmd
}

type selfTest struct {
	Ready  bool `json:"ready"`
	Checks []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Code   string `json:"code"`
	} `json:"checks"`
}

func (w *world) selfTest(t *testing.T) selfTest {
	t.Helper()
	out, _ := w.cli(t, "", "--self-test", "--no-upgrade-check")
	var st selfTest
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &st); err != nil {
		t.Fatalf("self-test output %q: %v", out, err)
	}
	return st
}

func (st selfTest) check(id string) string {
	for _, c := range st.Checks {
		if c.ID == id {
			return c.Status + "/" + c.Code
		}
	}
	return "missing"
}

// A machine onboarded by the app is fully configured as far as brick-cli is
// concerned (same config file, same schema, same tokens / OAuth client).
func TestCLISelfTestAcceptsAppOnboardedConfig(t *testing.T) {
	w := newWorld(t)
	w.onboard()
	w.run.Stop()
	st := w.selfTest(t)
	if !st.Ready {
		t.Fatalf("brick --self-test not ready after app onboarding: %+v", st)
	}
}

// The instance lock is shared: while the app syncs, brick-cli refuses to
// start a second engine — and vice versa.
func TestInstanceLockSharedWithCLI(t *testing.T) {
	w := newWorld(t)
	w.onboard()

	if got := w.selfTest(t).check("instance_lock"); got != "fail/already_running" {
		t.Errorf("CLI instance_lock while app syncs = %s", got)
	}
	out, err := w.cli(t, "", "--no-upgrade-check", "sync")
	if err == nil || !strings.Contains(out, "already running") {
		t.Errorf("brick sync while app syncs: err=%v out=%q", err, out)
	}

	w.run.Stop()
	cmd := w.cliCmd(t, "--no-upgrade-check", "sync")
	var cliOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &cliOut, &cliOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Signal(syscall.SIGTERM); _ = cmd.Wait() }()
	// The CLI writes its control-API discovery file only after taking the
	// lock, so once it exists the CLI definitely holds it.
	disc, _ := controlapi.RuntimeDir(w.cfgDir)
	eventually(t, "CLI to start syncing", func() bool {
		_, err := os.Stat(filepath.Join(disc, "agent.json"))
		return err == nil
	})
	if err := w.run.Start(runner.StartParams{}); !errors.Is(err, runner.ErrLocked) || w.run.Status().State != runner.StateLocked {
		t.Errorf("app Start while CLI syncs: err=%v state=%s\nCLI output:\n%s", err, w.run.Status().State, cliOut.String())
	}
}

// `brick switch-accounts` stops "the running instance" through the control
// API. With the app syncing, that must stop the app's engine (and not try to
// relaunch a CLI daemon in its place).
func TestCLISwitchAccountsStopsAppEngine(t *testing.T) {
	w := newWorld(t)
	w.onboard()
	eventually(t, "running", func() bool { return w.run.Running() })

	out, err := w.cli(t, "1\n", "--no-upgrade-check", "switch-accounts")
	if err != nil {
		t.Fatalf("switch-accounts: %v\n%s", err, out)
	}
	eventually(t, "app engine stopped", func() bool { return !w.run.Running() })
	if st := w.run.Status(); st.State != runner.StateStopped {
		t.Errorf("state = %s", st.State)
	}
	if !strings.Contains(out, "foreground") {
		t.Errorf("CLI should treat the app as a foreground instance (no relaunch): %q", out)
	}
	// And the app can take over again.
	if err := w.run.Start(runner.StartParams{}); err != nil {
		t.Errorf("restart after switch: %v", err)
	}
}

// Sync state written by brick-cli is picked up by the app: after a CLI sync,
// the app starts without re-transferring anything.
func TestAppReusesCLISyncState(t *testing.T) {
	w := newWorld(t)
	w.fs.PutFile("one.txt", "1")
	w.fs.PutFile("dir/two.txt", "2")
	folder := w.onboard()
	eventually(t, "app initial sync", fileIs(filepath.Join(folder, "dir", "two.txt"), "2"))
	w.run.Stop()

	// The CLI syncs a change made while the app was closed.
	w.fs.PutFile("three.txt", "3")
	cmd := w.cliCmd(t, "--no-upgrade-check", "sync")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "CLI download", fileIs(filepath.Join(folder, "three.txt"), "3"))
	time.Sleep(300 * time.Millisecond) // let it persist state
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()

	w.fs.ResetRequests()
	if err := w.run.Start(runner.StartParams{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "idle", func() bool { return w.run.Status().State == "idle" })
	time.Sleep(200 * time.Millisecond)
	if n := w.fs.Requests("GET /files/") + w.fs.Requests("POST /files") + w.fs.Requests("PUT /files/"); n != 0 {
		t.Errorf("app re-transferred %d files after a CLI sync (state not shared)\nCLI output:\n%s", n, out.String())
	}
}
