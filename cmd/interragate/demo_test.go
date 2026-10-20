package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// fakeOpener stands in for xdg-open: it records what it is asked to open,
// and fails as xdg-open does with no browser when fail is set.
func fakeOpener(t *testing.T, fail bool) *[]string {
	t.Helper()
	var opened []string
	old := opener
	opener = func(url string) *exec.Cmd {
		opened = append(opened, url)
		if fail {
			return exec.Command("sh", "-c", "exit 3")
		}
		return exec.Command("true")
	}
	t.Cleanup(func() { opener = old })
	return &opened
}

// demo opens its report once, says where it is, and says so truthfully
// when the browser could not be opened.
func TestDemoOpensTheReport(t *testing.T) {
	opened := fakeOpener(t, false)
	out, err := runCLI(t, "demo", "--open", "--no-color")
	if err != nil {
		t.Fatal(err)
	}
	if len(*opened) != 1 {
		t.Fatalf("opened %d times: %q", len(*opened), *opened)
	}
	path := strings.TrimPrefix((*opened)[0], "file://")
	if !strings.Contains(out, "Opened the full report in your browser: "+path) {
		t.Errorf("demo did not say where the report is:\n%s", out)
	}
	os.Remove(path)

	opened = fakeOpener(t, true)
	out, err = runCLI(t, "demo", "--no-color")
	if err != nil {
		t.Fatal(err)
	}
	path = strings.TrimPrefix((*opened)[0], "file://")
	if strings.Contains(out, "Opened the full report") || !strings.Contains(out, "The full report is at "+path+" (could not open a browser") {
		t.Errorf("a failed opener:\n%s", out)
	}
	os.Remove(path)
}
