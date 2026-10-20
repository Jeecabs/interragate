//go:build unix

package tfexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPassOn(t *testing.T) {
	cases := []struct {
		name                 string
		sig                  os.Signal
		shared, leader, want bool
	}{
		// At a terminal, Ctrl-C reaches Terraform from the terminal.
		{"Ctrl-C at a terminal", os.Interrupt, true, true, false},
		// kill from elsewhere reaches only interragate, the leader of its
		// group.
		{"kill at a terminal", syscall.SIGTERM, true, true, true},
		{"kill -HUP at a terminal", syscall.SIGHUP, true, true, true},
		// timeout(1) and make signal the whole group, Terraform included.
		{"timeout at a terminal", syscall.SIGTERM, true, false, false},
		// In a group of its own, Terraform hears only what interragate
		// passes on.
		{"interrupt, own group", os.Interrupt, false, false, true},
		{"termination, own group", syscall.SIGTERM, false, false, true},
		{"hangup, own group", syscall.SIGHUP, false, false, true},
	}
	for _, c := range cases {
		if got := passOn(c.sig, c.shared, c.leader); got != c.want {
			t.Errorf("%s: passOn = %v, want %v", c.name, got, c.want)
		}
	}
}

// stoppable writes a stand-in terraform whose plan says when it has
// started, records the signals it gets, and exits as Terraform does after
// stopping.
func stoppable(t *testing.T) (bin, started, got string) {
	t.Helper()
	dir := t.TempDir()
	started, got = filepath.Join(dir, "started"), filepath.Join(dir, "got")
	fakeBin(t, dir, "terraform", `trap 'echo TERM >> "`+got+`"; exit 1' TERM
trap 'echo INT >> "`+got+`"; exit 1' INT
touch "`+started+`"
i=0
while [ $i -lt 100 ]; do sleep 0.1; i=$((i+1)); done
`)
	return filepath.Join(dir, "terraform"), started, got
}

// sleeper is the path to sleep, for shims run with PATH narrowed to them.
func sleeper(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep on PATH")
	}
	return path
}

func waitFor(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s never appeared", path)
}

// Stopping interragate stops the plan it is running, once, with the Ctrl-C
// Terraform expects, and waits for it to finish stopping. Without a
// terminal, as under go test, Terraform runs in a process group of its own
// and hears only from interragate.
func TestStopPassesOnToTerraform(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGHUP, os.Interrupt} {
		bin, started, got := stoppable(t)
		r := &Runner{Bin: bin, Tool: "terraform", Dir: t.TempDir()}
		ctx, cancel := context.WithCancelCause(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.Plan(ctx, filepath.Join(t.TempDir(), "plan"), nil, nil) }()
		waitFor(t, started)
		cancel(&Stopped{Signal: sig})
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%v: the plan did not stop", sig)
		}
		if b, _ := os.ReadFile(got); string(b) != "INT\n" {
			t.Errorf("%v: terraform got %q, want one interrupt", sig, b)
		}
	}
}

// A wrapper script that runs terraform without passing signals on still
// stops: the interrupt goes to the whole group, as a terminal sends it.
func TestStopReachesWrappedTerraform(t *testing.T) {
	inner, started, got := stoppable(t)
	dir := t.TempDir()
	fakeBin(t, dir, "terraform", `"`+inner+`" "$@"
echo wrapper carried on >> "`+got+`"
`)
	r := &Runner{Bin: filepath.Join(dir, "terraform"), Tool: "terraform", Dir: t.TempDir()}
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Plan(ctx, filepath.Join(t.TempDir(), "plan"), nil, nil) }()
	waitFor(t, started)
	cancel(&Stopped{Signal: syscall.SIGTERM})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the plan did not stop")
	}
	if b, _ := os.ReadFile(got); !strings.HasPrefix(string(b), "INT\n") {
		t.Errorf("the wrapped terraform got %q, want an interrupt", b)
	}
}

// A shim that hangs is given up on within the probe's time, even when it
// is a script whose child holds its output open.
func TestProbeTimesOut(t *testing.T) {
	old := probeTimeout
	probeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { probeTimeout = old })
	bin := t.TempDir()
	fakeBin(t, bin, "terraform", sleeper(t)+" 20\n")
	t.Setenv("PATH", bin)
	t.Setenv("INTERRAGATE_TF_BIN", "")
	t.Setenv("TF_DATA_DIR", "")
	start := time.Now()
	_, err := Find(context.Background(), t.TempDir(), "")
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Find took %s", took)
	}
	if err == nil || !strings.Contains(err.Error(), "terraform version did not finish within 300ms") {
		t.Errorf("err = %v", err)
	}
}

// Ctrl-C while a shim is being probed stops interragate; it does not go on
// to the next binary.
func TestProbeStopsOnInterrupt(t *testing.T) {
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "tofu-ran")
	fakeBin(t, bin, "terraform", sleeper(t)+" 20\n")
	fakeBin(t, bin, "tofu", "echo ran > "+record+"\n"+workingTofu)
	t.Setenv("PATH", bin)
	t.Setenv("INTERRAGATE_TF_BIN", "")
	t.Setenv("TF_DATA_DIR", "")
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(200*time.Millisecond, func() { cancel(&Stopped{Signal: os.Interrupt}) })
	start := time.Now()
	_, err := Find(ctx, t.TempDir(), "")
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Find took %s", took)
	}
	if err == nil || err.Error() != "interrupted" {
		t.Errorf("err = %v, want interrupted", err)
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("tofu was tried after the interrupt")
	}
}
