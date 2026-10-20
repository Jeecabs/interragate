package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initTerraform writes a stand-in for terraform that records every command
// it runs. Its init leaves nothing in the directory, as with built-in
// providers only, but marks that it ran ("$0.init"), unless initScript
// says otherwise; its plan runs planScript first, which can fail the way
// Terraform does.
func initTerraform(t *testing.T, planScript, initScript string) (bin, dir, calls string) {
	if initScript == "" {
		initScript = `touch "$0.init"`
	}
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("# fake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs(mechanicsPlan)
	if err != nil {
		t.Fatal(err)
	}
	calls = filepath.Join(t.TempDir(), "calls")
	bin = filepath.Join(t.TempDir(), "terraform")
	script := `#!/bin/sh
echo "$1" >> "` + calls + `"
case "$1" in
  version) echo '{"terraform_version": "1.16.6"}' ;;
  init) ` + initScript + ` ;;
  plan)
` + planScript + `
    for a; do case "$a" in -out=*) echo fake > "${a#-out=}" ;; esac; done ;;
  show) cat "` + fixture + `" ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_DATA_DIR", "")
	return bin, dir, calls
}

func ranCommands(t *testing.T, calls string) string {
	t.Helper()
	b, _ := os.ReadFile(calls)
	return strings.Join(strings.Fields(string(b)), " ")
}

// A directory with no .terraform is planned without init when Terraform
// does not ask for it: init in a configuration with only built-in
// providers leaves no .terraform, and must not run on every plan.
func TestInitOnlyWhenAsked(t *testing.T) {
	bin, dir, calls := initTerraform(t, "", "")
	for range 2 {
		out, err := runCLI(t, "-C", dir, "--bin", bin, "--no-color")
		if err != nil || strings.Contains(out, "Ran terraform init") {
			t.Fatalf("err = %v\n%s", err, out)
		}
	}
	if got := ranCommands(t, calls); got != "version plan show version plan show" {
		t.Errorf("commands = %q, want no init", got)
	}

	// Asked for, init runs once, and interragate says why.
	bin, dir, calls = initTerraform(t, `    [ -e "$0.init" ] || { echo 'Error: Module not installed. Run "terraform init" to install all modules.' >&2; exit 1; }`, "")
	out, err := runCLI(t, "-C", dir, "--bin", bin, "--no-color")
	if err != nil || !strings.Contains(out, "Ran terraform init first: this directory had not been initialised.") {
		t.Errorf("err = %v\n%s", err, out)
	}
	if got := ranCommands(t, calls); got != "version plan init plan show" {
		t.Errorf("commands = %q", got)
	}
}

// When the backend has changed since the last init, init must be told
// whether to move the state, which interragate leaves to the user.
func TestBackendChangeIsLeftToTheUser(t *testing.T) {
	bin, dir, calls := initTerraform(t, `    printf 'Error: Backend initialization required, please run "terraform init"\n\nReason: Backend configuration block has changed\n' >&2; exit 1`, "")
	_, err := runCLI(t, "-C", dir, "--bin", bin, "--no-color")
	if err == nil || !strings.Contains(err.Error(), "terraform init -migrate-state") || !strings.Contains(err.Error(), "terraform init -reconfigure") || !strings.Contains(err.Error(), "(Backend configuration block has changed)") {
		t.Errorf("err = %v", err)
	}
	if got := ranCommands(t, calls); strings.Contains(got, "init") {
		t.Errorf("commands = %q, want no init", got)
	}

	// A backend set up for the first time over existing local state: init
	// needs the user's approval to copy it.
	bin, dir, _ = initTerraform(t,
		`    printf 'Error: Backend initialization required, please run "terraform init"\n\nReason: Initial configuration of the requested backend "s3"\n' >&2; exit 1`,
		`echo "Error: Can't ask approval for state migration when interactive input is disabled." >&2; exit 1`)
	_, err = runCLI(t, "-C", dir, "--bin", bin, "--no-color")
	if err == nil || !strings.Contains(err.Error(), "needs your approval: run terraform init here") {
		t.Errorf("err = %v", err)
	}
}
