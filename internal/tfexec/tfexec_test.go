package tfexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBin writes an executable shell script called name into dir.
func fakeBin(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestNeedsInit(t *testing.T) {
	cases := []struct {
		output string
		want   bool
	}{
		{"Error: Module not installed\n\nThis module is not yet installed. Run \"terraform init\" to install all modules", true},
		{"Error: Inconsistent dependency lock file\n\nTo update the locked dependency selections to match a changed configuration,\nrun:\n  tofu init -upgrade", true},
		{"Error: Backend initialization required, please run \"terraform init\"", true},
		{"Error: No valid credential sources found", false},
		{"Error: Reference to undeclared resource", false},
	}
	for _, c := range cases {
		err := error(&CommandError{Err: errors.New("exit status 1"), Output: c.output})
		if got := NeedsInit(fmt.Errorf("planning: %w", err)); got != c.want {
			t.Errorf("NeedsInit(%q) = %v, want %v", c.output, got, c.want)
		}
	}
	if NeedsInit(errors.New("terraform init")) {
		t.Error("only a failed command's output counts")
	}
}

func TestInitialised(t *testing.T) {
	t.Setenv("TF_DATA_DIR", "")
	dir := t.TempDir()
	if Initialised(dir) {
		t.Error("a fresh directory is not initialised")
	}
	if err := os.Mkdir(filepath.Join(dir, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !Initialised(dir) {
		t.Error("a directory with .terraform is initialised")
	}

	// TF_DATA_DIR moves what init leaves, relative to the directory.
	t.Setenv("TF_DATA_DIR", "elsewhere")
	if Initialised(dir) {
		t.Error("with TF_DATA_DIR, .terraform does not count")
	}
	if err := os.Mkdir(filepath.Join(dir, "elsewhere"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !Initialised(dir) {
		t.Error("TF_DATA_DIR is relative to the directory")
	}
}

// What Terraform and OpenTofu say when the backend changed after init,
// against what they say when there is a backend to set up for the first
// time, which a plain init handles.
func TestBackendChange(t *testing.T) {
	cases := []struct{ output, want string }{
		{"Error: Backend initialization required, please run \"terraform init\"\n\nReason: Backend configuration block has changed\n\nThe \"backend\" is the interface", "Backend configuration block has changed"},
		{"Error: Backend initialization required: please run \"tofu init\"\n\nReason: Backend configuration block has changed\n", "Backend configuration block has changed"},
		{"Error: Backend initialization required, please run \"terraform init\"\n\nReason: Unsetting the previously set backend \"s3\"\n", `Unsetting the previously set backend "s3"`},
		{"Error: Backend configuration changed\n\nA change in the backend configuration has been detected, which may require\nmigrating existing state.", "Backend configuration changed"},
		{"Error: Backend initialization required, please run \"terraform init\"\n\nReason: Initial configuration of the requested backend \"s3\"\n", ""},
		{"Error: Module not installed\n\nRun \"terraform init\" to install all modules", ""},
	}
	for _, c := range cases {
		err := error(&CommandError{Err: errors.New("exit status 1"), Output: c.output})
		if got := BackendChange(fmt.Errorf("planning: %w", err)); got != c.want {
			t.Errorf("BackendChange(%q) = %q, want %q", c.output, got, c.want)
		}
	}
}

// Output interragate keeps, for an error message or the live page, has no
// colour codes: Terraform is asked for plain output when only interragate
// sees it, and codes are removed when it is also shown at a terminal (-v).
func TestCapturedOutputIsPlain(t *testing.T) {
	bin := t.TempDir()
	// Colours its errors unless told not to, as Terraform does.
	fakeBin(t, bin, "terraform", `for a; do [ "$a" = -no-color ] && { echo "Error: asked for plain"; exit 1; }; done
printf '\033[1m\033[31mError:\033[0m\033[0m \033[1mcoloured\033[0m\n'
exit 1
`)
	r := &Runner{Bin: filepath.Join(bin, "terraform"), Tool: "terraform", Dir: t.TempDir()}
	var ce *CommandError
	if err := r.Init(context.Background(), nil); !errors.As(err, &ce) || ce.Output != "Error: asked for plain" {
		t.Errorf("captured only: err = %v", err)
	}
	var shown bytes.Buffer
	if err := r.Init(context.Background(), &shown); !errors.As(err, &ce) || ce.Output != "Error: coloured" {
		t.Errorf("shown as well: output = %q", ce.Output)
	}
	if !strings.Contains(shown.String(), "\x1b[31m") {
		t.Errorf("the terminal lost its colour: %q", shown.String())
	}
}

// The output of a failed command is shown once: in the error when it ran
// quietly, not again when it was printed as it ran.
func TestCommandErrorShowsOutputOnce(t *testing.T) {
	quiet := &CommandError{Err: errors.New("exit status 1"), Output: "Error: boom", Command: "terraform plan"}
	if !strings.Contains(quiet.Error(), "Error: boom") {
		t.Errorf("quiet error = %q", quiet.Error())
	}
	loud := &CommandError{Err: errors.New("exit status 1"), Output: "Error: boom", Shown: true, Command: "terraform plan"}
	if strings.Contains(loud.Error(), "Error: boom") {
		t.Errorf("verbose error repeats the output: %q", loud.Error())
	}
}

func TestWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TF_WORKSPACE", "")
	t.Setenv("TF_DATA_DIR", "")
	if got := Workspace(dir); got != "default" {
		t.Errorf("fresh directory: %q", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".terraform", "environment"), []byte("prod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Workspace(dir); got != "prod" {
		t.Errorf("selected workspace: %q", got)
	}
	t.Setenv("TF_WORKSPACE", "staging")
	if got := Workspace(dir); got != "staging" {
		t.Errorf("TF_WORKSPACE wins: %q", got)
	}

	// With TF_DATA_DIR, workspace select records it there instead.
	t.Setenv("TF_WORKSPACE", "")
	data := t.TempDir()
	t.Setenv("TF_DATA_DIR", data)
	if err := os.WriteFile(filepath.Join(data, "environment"), []byte("qa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Workspace(dir); got != "qa" {
		t.Errorf("with TF_DATA_DIR: %q", got)
	}
}

func TestPlanWithProgress(t *testing.T) {
	bin, work := t.TempDir(), t.TempDir()
	events := `{"type":"version","@message":"Terraform 1.16.6"}
{"type":"refresh_start","hook":{"resource":{"addr":"aws_db_instance.orders"}}}
{"type":"refresh_complete","hook":{"resource":{"addr":"aws_db_instance.orders"}}}
{"type":"planned_change","change":{"resource":{"addr":"aws_db_instance.orders"}}}
`
	fakeBin(t, bin, "terraform", "cat <<'JSON'\n"+events+"JSON\n")
	r := &Runner{Bin: filepath.Join(bin, "terraform"), Tool: "terraform", Dir: work}
	var got []string
	if err := r.PlanWithProgress(context.Background(), filepath.Join(work, "plan"), nil, func(e Event) { got = append(got, e.Type+" "+e.Address) }); err != nil {
		t.Fatal(err)
	}
	want := "version |refresh_start aws_db_instance.orders|refresh_complete aws_db_instance.orders|planned_change aws_db_instance.orders"
	if strings.Join(got, "|") != want {
		t.Errorf("events = %q", got)
	}
}

// A failed plan's error reads like Terraform's own: the summary, where, the
// code and the detail, rebuilt from the diagnostics in the event stream.
func TestPlanWithProgressFailure(t *testing.T) {
	bin, work := t.TempDir(), t.TempDir()
	diag := `{"type":"diagnostic","diagnostic":{"severity":"error","summary":"Invalid expression","detail":"Expected the start of an expression, but found an invalid expression token.","range":{"filename":"main.tf","start":{"line":37}},"snippet":{"context":"resource \"random_pet\" \"cluster\"","code":"  length = ","start_line":37}}}`
	fakeBin(t, bin, "terraform", "echo '"+diag+"'\nexit 1\n")
	r := &Runner{Bin: filepath.Join(bin, "terraform"), Tool: "terraform", Dir: work}
	err := r.PlanWithProgress(context.Background(), filepath.Join(work, "plan"), nil, nil)
	want := `terraform plan failed:

Error: Invalid expression

  on main.tf line 37, in resource "random_pet" "cluster":
  37:   length = 

Expected the start of an expression, but found an invalid expression token.`
	if err == nil || err.Error() != want {
		t.Errorf("err =\n%v\nwant\n%s", err, want)
	}
	// The advice to run init survives, so a missing module still triggers it.
	fakeBin(t, bin, "terraform", `echo '{"type":"diagnostic","diagnostic":{"severity":"error","summary":"Module not installed","detail":"This module is not yet installed. Run \"terraform init\" to install all modules required by this configuration."}}'`+"\nexit 1\n")
	if err := r.PlanWithProgress(context.Background(), filepath.Join(work, "plan"), nil, nil); !NeedsInit(err) {
		t.Errorf("NeedsInit(%v) = false", err)
	}
	// Output that is not JSON at all comes through as it was.
	fakeBin(t, bin, "terraform", "echo 'mise ERROR something broke' >&2\nexit 1\n")
	if err := r.PlanWithProgress(context.Background(), filepath.Join(work, "plan"), nil, nil); err == nil || !strings.Contains(err.Error(), "mise ERROR something broke") {
		t.Errorf("raw output lost: %v", err)
	}
}
