package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Jeecabs/interragate/internal/tfexec"
)

// A termination while interragate plans reaches Terraform as the interrupt
// it stops on (releasing its lock), and interragate removes the plan file it
// made, which can hold sensitive values.
func TestStopMidPlan(t *testing.T) {
	work := t.TempDir()
	started, got, out := filepath.Join(work, "started"), filepath.Join(work, "got"), filepath.Join(work, "out")
	bin := filepath.Join(work, "terraform")
	script := `#!/bin/sh
case "$1" in
  version) echo '{"terraform_version": "1.16.6"}'; exit 0 ;;
esac
for a; do case "$a" in -out=*) echo "${a#-out=}" > "` + out + `" ;; esac; done
trap 'echo INT > "` + got + `"; exit 1' INT
touch "` + started + `"
i=0
while [ $i -lt 100 ]; do sleep 0.1; i=$((i+1)); done
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"main.tf"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# fake\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() {
		var buf bytes.Buffer
		done <- run(ctx, []string{"-C", dir, "--bin", bin, "--no-color"}, &buf, &buf)
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the plan never started")
		}
	}
	cancel(&tfexec.Stopped{Signal: syscall.SIGTERM})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("interragate did not stop")
	}
	if b, _ := os.ReadFile(got); strings.TrimSpace(string(b)) != "INT" {
		t.Errorf("terraform got %q, want the interrupt Terraform stops on", b)
	}
	planFile, _ := os.ReadFile(out)
	if _, err := os.Stat(strings.TrimSpace(string(planFile))); err == nil || len(planFile) == 0 {
		t.Errorf("the plan file %q was left behind", planFile)
	}
}
