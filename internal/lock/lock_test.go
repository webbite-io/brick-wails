package lock

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAcquireSingleton(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brick.lock")
	l1, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire: %v, want ErrLocked", err)
	}
	l1.Release()
	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	l2.Release()
}

func TestPathIn(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "brick")
	p, err := PathIn(dir)
	if err != nil || p != filepath.Join(dir, "brick.lock") {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("config dir not created: %v", err)
	}
}

// The guarantee that matters for coexisting with brick-cli is across
// *processes*: a helper process (this test binary re-executed) holds the
// lock, and this process must be refused until the helper exits.
func TestAcquireAcrossProcesses(t *testing.T) {
	if path := os.Getenv("BRICK_LOCK_HELPER_PATH"); path != "" {
		l, err := Acquire(path)
		if err != nil {
			os.Stdout.WriteString("ERR " + err.Error() + "\n")
			os.Exit(1)
		}
		os.Stdout.WriteString("LOCKED\n")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n') // hold until parent says go
		l.Release()
		os.Exit(0)
	}

	path := filepath.Join(t.TempDir(), "brick.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAcquireAcrossProcesses$")
	cmd.Env = append(os.Environ(), "BRICK_LOCK_HELPER_PATH="+path)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if line != "LOCKED\n" {
		t.Fatalf("helper said %q", line)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("acquire while helper holds lock: %v, want ErrLocked", err)
	}
	stdin.Write([]byte("go\n"))
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire after helper exit: %v", err)
	}
	l.Release()
}
