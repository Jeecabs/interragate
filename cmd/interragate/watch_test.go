package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Jeecabs/interragate/internal/tfexec"
)

func TestWatcher(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.tf", "a")
	write("prod.tfvars", "a")
	write("modules/net/main.tf", "a")
	shared := filepath.Join(t.TempDir(), "shared.tfvars")
	if err := os.WriteFile(shared, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := newWatcher(dir, &options{extra: []string{"-var-file=env/extra.tfvars", "-var-file", shared}})
	w.dirs = append(w.dirs, filepath.Join(dir, "modules", "net"))
	w.last = w.scan()
	w.poll, w.quiet = 10*time.Millisecond, 30*time.Millisecond

	wait := func() []string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		got, err := w.wait(ctx)
		if err != nil {
			return nil
		}
		return got
	}

	// Several writes in quick succession, as an editor saves, are one
	// change; files Terraform ignores and other files are not watched.
	write(".main.tf.swp", "x")
	write("notes.md", "x")
	write("main.tf", "bb")
	write("modules/net/main.tf", "bb")
	if got := wait(); len(got) != 2 || got[0] != "main.tf" || got[1] != "modules/net/main.tf" {
		t.Errorf("changed = %v", got)
	}

	// A var file named on the command line counts, even outside the root.
	write("env/extra.tfvars", "x")
	if got := wait(); len(got) != 1 || got[0] != "env/extra.tfvars" {
		t.Errorf("changed = %v", got)
	}
	// So does one given by its full path, which keeps it.
	if err := os.WriteFile(shared, []byte("bb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := wait(); len(got) != 1 || got[0] != filepath.ToSlash(shared) {
		t.Errorf("changed = %v, want %s", got, shared)
	}

	// A deleted file is a change.
	if err := os.Remove(filepath.Join(dir, "prod.tfvars")); err != nil {
		t.Fatal(err)
	}
	if got := wait(); len(got) != 1 || got[0] != "prod.tfvars" {
		t.Errorf("changed = %v", got)
	}

	// Nothing else happening: wait gives up when its context ends.
	if got := wait(); got != nil {
		t.Errorf("changed = %v with nothing changed", got)
	}
}

func TestIgnored(t *testing.T) {
	for name, want := range map[string]bool{"main.tf": false, ".main.tf.swp": true, "#main.tf#": true, "main.tf~": true, ".terraform.lock.hcl": true} {
		if got := ignored(name); got != want {
			t.Errorf("ignored(%q) = %v", name, got)
		}
	}
}

// watch opens the live report through a private file that redirects to it,
// so the address's secret token never appears on a command line, which
// every user of the machine can read; the file goes when watch stops.
func TestWatchKeepsTheTokenOffTheCommandLine(t *testing.T) {
	bin, dir, _ := fakeTerraform(t, mechanicsPlan)
	opened := make(chan string, 1)
	old := opener
	opener = func(url string) *exec.Cmd {
		opened <- url
		return exec.Command("true")
	}
	t.Cleanup(func() { opener = old })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		var out bytes.Buffer
		done <- run(ctx, []string{"watch", "-C", dir, "--bin", bin, "--no-color"}, &out, &out)
	}()
	var url string
	select {
	case url = <-opened:
	case <-time.After(10 * time.Second):
		t.Fatal("watch opened nothing")
	}
	path, ok := strings.CutPrefix(url, "file://")
	if !ok {
		t.Fatalf("watch opened %s, not a file", url)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the redirect file has mode %v, want 0600", perm)
	}
	page, _ := os.ReadFile(path)
	m := regexp.MustCompile(`url=(http://127\.0\.0\.1:\d+/([0-9a-f]{32})/)`).FindStringSubmatch(string(page))
	if m == nil {
		t.Fatalf("the redirect file does not lead to the live report:\n%s", page)
	}
	if strings.Contains(url, m[2]) {
		t.Errorf("the token is on the opener's command line: %s", url)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("watch: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watch did not stop")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the redirect file outlived watch")
	}
}

func TestWatchRejectsOutputFlags(t *testing.T) {
	if _, err := runCLI(t, "watch", "-f", "json"); err == nil {
		t.Error("watch -f json should be refused")
	}
}

func TestPlanProgressLine(t *testing.T) {
	var shown []string
	p := &planProgress{lead: "Planning with terraform in infra", width: 200, show: func(s string) { shown = append(shown, s) }}
	p.event(tfexec.Event{Type: "refresh_start", Address: "aws_vpc.main"})
	if want := "Planning with terraform in infra: refreshed 0 resources, now aws_vpc.main…"; p.line() != want {
		t.Errorf("line = %q", p.line())
	}
	p.event(tfexec.Event{Type: "refresh_complete", Address: "aws_vpc.main"})
	p.event(tfexec.Event{Type: "planned_change", Address: "aws_vpc.main"})
	if want := "Planning with terraform in infra: refreshed 1 resource, 1 change so far…"; p.line() != want {
		t.Errorf("line = %q", p.line())
	}
	p.width = 30
	if got := p.line(); len([]rune(got)) > 29 {
		t.Errorf("line is wider than the terminal: %q", got)
	}
	if len(shown) == 0 {
		t.Error("nothing was shown")
	}
}
