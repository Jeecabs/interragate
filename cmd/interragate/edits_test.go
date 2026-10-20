package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
)

// copyTree copies the files under src into dst, over what is there.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dst, filepath.Dir(rel)), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The mechanics scenario as a repository: v1 committed, v2 not yet.
func TestEditsFromTheWorkingTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		args = append([]string{"-C", dir, "-c", "user.name=interragate", "-c", "user.email=interragate@example.com", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q")
	git("symbolic-ref", "HEAD", "refs/heads/main")
	copyTree(t, "../../testdata/scenarios/mechanics/v1", dir)
	git("add", "-A")
	git("commit", "-q", "-m", "v1")
	copyTree(t, "../../testdata/scenarios/mechanics/v2", dir)

	out, err := runCLI(t, "show", mechanicsPlan, "-C", dir, "-f", "json")
	if err != nil {
		t.Fatal(err)
	}
	var r model.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.Git == nil || r.Git.Base != "HEAD" || !r.Git.Dirty {
		t.Fatalf("git = %+v", r.Git)
	}
	for _, ch := range r.Changes {
		if ch.Address == "terraform_data.deploy" && (ch.Edit == nil || ch.Edit.Kind != "edited") {
			t.Errorf("deploy edit = %+v", ch.Edit)
		}
	}

	out, err = runCLI(t, "explain", mechanicsPlan, "terraform_data.deploy", "-C", dir, "--no-color")
	if err != nil || !strings.Contains(out, "Your edit\n  Changed in main.tf since the last commit.\n  -     version = \"1.4.0\"\n  +     version = \"1.5.0\"\n") {
		t.Errorf("explain: %v\n%s", err, out)
	}

	out, err = runCLI(t, "show", mechanicsPlan, "-C", dir, "-f", "json", "--base", "none")
	if err != nil || strings.Contains(out, `"git"`) || strings.Contains(out, `"edit"`) {
		t.Errorf("--base none still compared: %v", err)
	}
}
