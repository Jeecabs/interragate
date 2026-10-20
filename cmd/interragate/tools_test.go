package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// toolsOnPath puts a broken terraform shim and a tofu that records what it
// is asked to do first on PATH, and returns where tofu's record goes.
func toolsOnPath(t *testing.T) (bin, record string) {
	t.Helper()
	bin = t.TempDir()
	record = filepath.Join(t.TempDir(), "tofu-calls")
	write := func(name, script string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("terraform", "echo 'mise ERROR No version is set for shim: terraform' >&2\necho 'mise use -g terraform@1.16.6' >&2\nexit 1\n")
	write("tofu", `echo "$1" >> "`+record+`"
case "$1" in
  version) echo '{"terraform_version": "1.13.1"}' ;;
  *) exit 1 ;;
esac
`)
	t.Setenv("PATH", bin)
	t.Setenv("INTERRAGATE_TF_BIN", "")
	t.Setenv("TF_DATA_DIR", "")
	return bin, record
}

func terraformDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"main.tf":             "resource \"terraform_data\" \"x\" {}\n",
		".terraform.lock.hcl": "provider \"registry.terraform.io/hashicorp/random\" {\n  version = \"3.9.1\"\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A directory initialised by terraform, with terraform broken and tofu
// working: interragate stops with the shim's advice and never runs tofu,
// whose init would rewrite the lock file.
func TestPlanNeverSwitchesTools(t *testing.T) {
	_, record := toolsOnPath(t)
	dir := terraformDir(t)
	lock, _ := os.ReadFile(filepath.Join(dir, ".terraform.lock.hcl"))

	_, err := runCLI(t, "-C", dir, "--no-color")
	if err == nil || !strings.Contains(err.Error(), "terraform is on PATH but does not run") || !strings.Contains(err.Error(), "mise use -g terraform@1.16.6") {
		t.Errorf("err = %v", err)
	}
	if calls, _ := os.ReadFile(record); len(calls) > 0 {
		t.Errorf("tofu was run: %q", calls)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, ".terraform.lock.hcl")); string(after) != string(lock) {
		t.Errorf("the lock file changed:\n%s", after)
	}
}

// doctor reports on the binary interragate would use, and agrees with the
// planner about whether it can plan.
func TestDoctorAgreesWithThePlanner(t *testing.T) {
	bin, _ := toolsOnPath(t)
	dir := terraformDir(t)
	out, err := runCLI(t, "doctor", "-C", dir, "--no-color")
	var code exitError
	if !errors.As(err, &code) || code != 1 {
		t.Errorf("doctor with only a broken terraform for a terraform directory: err = %v, want exit 1", err)
	}
	for _, want := range []string{"✗ terraform is on PATH but does not run", "This directory needs terraform", "✓ tofu 1.13.1 runs"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}

	// A broken binary interragate would not use is a note, not a failure.
	if err := os.WriteFile(filepath.Join(bin, "terraform"), []byte("#!/bin/sh\necho '{\"terraform_version\": \"1.16.6\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tofu"), []byte("#!/bin/sh\necho 'mise ERROR No version is set for shim: tofu' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err = runCLI(t, "doctor", "-C", dir, "--no-color")
	if err != nil {
		t.Errorf("a healthy directory failed doctor: %v\n%s", err, out)
	}
	for _, want := range []string{"! tofu is on PATH but does not run", "interragate will use terraform here, because its lock file names providers from the Terraform registry"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}

	// --bin and INTERRAGATE_TF_BIN are checked by running them.
	out, err = runCLI(t, "doctor", "-C", dir, "--no-color", "--bin", filepath.Join(bin, "tofu"))
	if !errors.As(err, &code) || !strings.Contains(out, "--bin names "+filepath.Join(bin, "tofu")+", which does not run") {
		t.Errorf("doctor --bin with a broken binary: err = %v\n%s", err, out)
	}
	t.Setenv("INTERRAGATE_TF_BIN", filepath.Join(bin, "terraform"))
	out, err = runCLI(t, "doctor", "-C", dir, "--no-color")
	if err != nil || !strings.Contains(out, "INTERRAGATE_TF_BIN chooses terraform 1.16.6") {
		t.Errorf("doctor with INTERRAGATE_TF_BIN: err = %v\n%s", err, out)
	}
}
