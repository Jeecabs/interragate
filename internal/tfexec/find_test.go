package tfexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	brokenShim       = "echo 'mise ERROR No version is set for shim: terraform' >&2\necho 'mise use -g terraform@1.16.6' >&2\nexit 1\n"
	workingTofu      = `echo '{"terraform_version": "1.13.1", "platform": "linux_amd64"}'` + "\n"
	workingTerraform = `echo '{"terraform_version": "1.16.6"}'` + "\n"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTies(t *testing.T) {
	t.Setenv("TF_DATA_DIR", "")
	cases := []struct {
		name  string
		files map[string]string
		owner string
	}{
		{"empty", nil, ""},
		{"tofu files", map[string]string{"main.tofu": ""}, opentofu},
		{"tofu lock", map[string]string{".terraform.lock.hcl": `provider "registry.opentofu.org/hashicorp/aws" {}`}, opentofu},
		{"terraform lock", map[string]string{".terraform.lock.hcl": `provider "registry.terraform.io/hashicorp/aws" {}`}, terraform},
		// A lock file naming both registries, or neither, says nothing.
		{"mixed lock", map[string]string{".terraform.lock.hcl": "provider \"registry.terraform.io/hashicorp/aws\" {}\nprovider \"registry.opentofu.org/hashicorp/random\" {}\n"}, ""},
		{"private registry", map[string]string{".terraform.lock.hcl": `provider "registry.example.com/acme/thing" {}`}, ""},
		{"comment only", map[string]string{".terraform.lock.hcl": "# registry.opentofu.org/hashicorp/aws\n"}, ""},
		// Without a lock file, the providers init installed decide.
		{"installed providers", map[string]string{".terraform/providers/registry.terraform.io/hashicorp/aws/x": ""}, terraform},
		{"the lock file outranks installed providers", map[string]string{
			".terraform.lock.hcl": `provider "registry.opentofu.org/hashicorp/aws" {}`,
			".terraform/providers/registry.terraform.io/hashicorp/aws/x": "",
		}, opentofu},
	}
	for _, c := range cases {
		dir := t.TempDir()
		writeFiles(t, dir, c.files)
		if got, _ := tiesOf(dir).owner(); got != c.owner {
			t.Errorf("%s: owner = %q, want %q", c.name, got, c.owner)
		}
	}

	// TF_DATA_DIR moves the installed providers.
	dir, data := t.TempDir(), t.TempDir()
	writeFiles(t, data, map[string]string{"providers/registry.opentofu.org/hashicorp/aws/x": ""})
	t.Setenv("TF_DATA_DIR", data)
	if got, _ := tiesOf(dir).owner(); got != opentofu {
		t.Errorf("with TF_DATA_DIR: owner = %q", got)
	}
}

// A version manager's shim is on PATH but fails until a version is chosen.
// In a directory tied to neither tool, Find skips it for a binary that
// runs, and says what the shim printed when nothing else does.
func TestFindSkipsBrokenShims(t *testing.T) {
	bin, work := t.TempDir(), t.TempDir()
	fakeBin(t, bin, "terraform", "echo 'mise ERROR No version is set for shim: terraform' >&2\nexit 1\n")
	t.Setenv("PATH", bin)
	t.Setenv("INTERRAGATE_TF_BIN", "")
	t.Setenv("TF_DATA_DIR", "")
	ctx := context.Background()

	_, err := Find(ctx, work, "")
	if err == nil || !strings.Contains(err.Error(), "terraform is on PATH but does not run: mise ERROR No version is set for shim: terraform") {
		t.Fatalf("err = %v", err)
	}

	fakeBin(t, bin, "tofu", workingTofu)
	ch, err := Choose(ctx, work, "")
	if err != nil || ch.Runner == nil {
		t.Fatalf("choice = %+v, err = %v", ch, err)
	}
	if ch.Runner.Tool != opentofu || ch.Runner.Version != "1.13.1" || len(ch.Skipped) != 1 || ch.Skipped[0].Name != "terraform" {
		t.Errorf("choice = %+v, want a working tofu 1.13.1 after skipping terraform", ch)
	}

	cands, err := Candidates(ctx, work, nil)
	if err != nil || len(cands) != 2 || cands[0].Problem == "" || cands[1].Version != "1.13.1" {
		t.Errorf("candidates = %+v, err = %v", cands, err)
	}
}

func TestFindNothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("INTERRAGATE_TF_BIN", "")
	if _, err := Find(context.Background(), t.TempDir(), ""); err == nil || !strings.Contains(err.Error(), "neither terraform nor tofu is on PATH") {
		t.Errorf("err = %v", err)
	}
}

// A directory initialised by terraform is never planned with tofu, even
// when terraform is broken and tofu works: tofu would rewrite the lock file
// for its own registry. interragate passes on the shim's advice instead.
func TestChooseNeverSwitchesTools(t *testing.T) {
	bin := t.TempDir()
	fakeBin(t, bin, "terraform", brokenShim)
	fakeBin(t, bin, "tofu", workingTofu)
	t.Setenv("PATH", bin)
	t.Setenv("INTERRAGATE_TF_BIN", "")
	t.Setenv("TF_DATA_DIR", "")
	ctx := context.Background()

	locked := t.TempDir()
	writeFiles(t, locked, map[string]string{".terraform.lock.hcl": `provider "registry.terraform.io/hashicorp/random" {}`})
	_, err := Find(ctx, locked, "")
	for _, want := range []string{
		"terraform is on PATH but does not run: mise ERROR",
		"needs terraform, because its lock file names providers from the Terraform registry, and tofu init would rewrite it",
		"mise use -g terraform@1.16.6",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("terraform-locked directory: err = %v, want it to say %q", err, want)
		}
	}

	// With no lock file, the providers init installed say which tool it is.
	installed := t.TempDir()
	writeFiles(t, installed, map[string]string{".terraform/providers/registry.terraform.io/hashicorp/random/x": ""})
	if _, err := Find(ctx, installed, ""); err == nil || !strings.Contains(err.Error(), "init installed its providers from the Terraform registry") {
		t.Errorf("terraform-initialised directory: err = %v", err)
	}

	// .tofu files need tofu: terraform would ignore them.
	fakeBin(t, bin, "terraform", workingTerraform)
	fakeBin(t, bin, "tofu", "echo 'mise ERROR No version is set for shim: tofu' >&2\nexit 1\n")
	tofuFiles := t.TempDir()
	writeFiles(t, tofuFiles, map[string]string{"main.tofu": ""})
	if _, err := Find(ctx, tofuFiles, ""); err == nil || !strings.Contains(err.Error(), "tofu is on PATH but does not run") || !strings.Contains(err.Error(), "needs tofu, because it has .tofu files, which terraform ignores") {
		t.Errorf(".tofu files with a broken tofu: err = %v", err)
	}
	if err := os.Remove(filepath.Join(bin, "tofu")); err != nil {
		t.Fatal(err)
	}
	if _, err := Find(ctx, tofuFiles, ""); err == nil || !strings.Contains(err.Error(), "but tofu is not on PATH") {
		t.Errorf(".tofu files without tofu: err = %v", err)
	}

	// .tofu files and a lock file from the Terraform registry pull both
	// ways; the user decides.
	writeFiles(t, tofuFiles, map[string]string{".terraform.lock.hcl": `provider "registry.terraform.io/hashicorp/random" {}`})
	if _, err := Find(ctx, tofuFiles, ""); err == nil || !strings.Contains(err.Error(), "pass --bin terraform") {
		t.Errorf(".tofu files and a terraform lock file: err = %v", err)
	}
}

// --bin and INTERRAGATE_TF_BIN choose the binary, but it must run, and it
// must not be one whose init would rewrite the lock file.
func TestChooseExplicit(t *testing.T) {
	bin := t.TempDir()
	fakeBin(t, bin, "terraform", workingTerraform)
	fakeBin(t, bin, "tofu", workingTofu)
	fakeBin(t, bin, "broken", brokenShim)
	t.Setenv("PATH", bin)
	t.Setenv("INTERRAGATE_TF_BIN", "")
	t.Setenv("TF_DATA_DIR", "")
	ctx := context.Background()

	r, err := Find(ctx, t.TempDir(), filepath.Join(bin, "tofu"))
	if err != nil || r.Tool != opentofu || r.Name() != "tofu" || r.Version != "1.13.1" {
		t.Errorf("runner = %+v, err = %v", r, err)
	}
	if _, err := Find(ctx, t.TempDir(), filepath.Join(bin, "missing")); err == nil || !strings.Contains(err.Error(), "cannot be found") {
		t.Errorf("a missing binary: err = %v", err)
	}

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".terraform.lock.hcl": `provider "registry.opentofu.org/hashicorp/random" {}`})
	if _, err := Find(ctx, dir, "terraform"); err == nil || !strings.Contains(err.Error(), "--bin names terraform, but this directory's lock file names providers from the OpenTofu registry") {
		t.Errorf("terraform in a tofu-locked directory: err = %v", err)
	}

	t.Setenv("INTERRAGATE_TF_BIN", filepath.Join(bin, "broken"))
	ch, err := Choose(ctx, dir, "")
	if err != nil || ch.Runner != nil || ch.From != "INTERRAGATE_TF_BIN" || !strings.Contains(ch.Problem, "which does not run: mise ERROR") || !strings.Contains(ch.Fix, "mise use -g terraform@1.16.6") {
		t.Errorf("a broken INTERRAGATE_TF_BIN: choice = %+v, err = %v", ch, err)
	}
	t.Setenv("INTERRAGATE_TF_BIN", "")

	// Named explicitly, terraform may plan a directory with .tofu files,
	// and interragate says what it leaves out.
	unlocked := t.TempDir()
	writeFiles(t, unlocked, map[string]string{"main.tofu": ""})
	ch, err = Choose(ctx, unlocked, "terraform")
	if err != nil || ch.Runner == nil || ch.Runner.Version != "1.16.6" || !strings.Contains(ch.Note, "not the .tofu ones") {
		t.Errorf("terraform with .tofu files: choice = %+v, err = %v", ch, err)
	}
}
