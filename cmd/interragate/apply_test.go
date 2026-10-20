package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/tfexec"
)

// fakeTerraform writes a stand-in for terraform that plans by copying a
// fixture and records what it is asked to apply, so interragate apply can be
// tested without touching anything real.
func fakeTerraform(t *testing.T, fixture string) (bin, dir, log string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("# fake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	log = filepath.Join(t.TempDir(), "applied")
	bin = filepath.Join(t.TempDir(), "terraform")
	script := `#!/bin/sh
for last; do :; done
case "$1" in
  version) echo '{"terraform_version": "1.16.6"}' ;;
  init) mkdir -p .terraform ;;
  plan) for a; do case "$a" in -out=*) echo fake > "${a#-out=}" ;; esac; done ;;
  show) cat "` + abs + `" ;;
  apply) echo "applied $last" >> "` + log + `" ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir, log
}

func applied(t *testing.T, log string) bool {
	t.Helper()
	b, err := os.ReadFile(log)
	return err == nil && strings.Contains(string(b), "applied ")
}

func withConfirmation(t *testing.T, answer string, terminal bool) {
	t.Helper()
	oldIn, oldTerm := confirmInput, stdinIsTerminal
	confirmInput = strings.NewReader(answer)
	stdinIsTerminal = func() bool { return terminal }
	t.Cleanup(func() { confirmInput, stdinIsTerminal = oldIn, oldTerm })
}

// With nobody to confirm, apply says so before it plans, not after.
func TestApplyNeedsSomeoneToConfirm(t *testing.T) {
	bin, dir, log := fakeTerraform(t, mechanicsPlan)
	withConfirmation(t, "", false)
	out, err := runCLI(t, "apply", "--bin", bin, "-C", dir, "--no-color")
	if err == nil || !strings.Contains(err.Error(), "--auto-approve") || applied(t, log) {
		t.Errorf("err = %v, applied = %v", err, applied(t, log))
	}
	if out != "" {
		t.Errorf("it planned first:\n%s", out)
	}
}

// question is a confirmation that never comes: it says when apply starts
// waiting for it.
type question struct{ asked chan struct{} }

func (q question) Read([]byte) (int, error) {
	close(q.asked)
	select {}
}

// Ctrl-C at the question stops apply: it does not wait on, to apply after
// all when "yes" comes later.
func TestApplyStopsOnCtrlC(t *testing.T) {
	bin, dir, log := fakeTerraform(t, mechanicsPlan)
	q := question{make(chan struct{})}
	withConfirmation(t, "", true)
	confirmInput = q
	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		<-q.asked
		cancel(&tfexec.Stopped{Signal: os.Interrupt})
	}()
	var out bytes.Buffer
	err := run(ctx, []string{"apply", "--bin", bin, "-C", dir, "--no-color", "--fail-on", "critical"}, &out, &out)
	var code exitError
	if !errors.As(err, &code) || code != 130 || applied(t, log) || !strings.HasSuffix(out.String(), "Not applied.\n") {
		t.Errorf("err = %v, applied = %v\n%s", err, applied(t, log), out.String())
	}
}

// The mechanics plan has a high finding, so "yes" is not enough: the
// directory's name is.
func TestApplyRiskyPlanAsksForTheDirectoryName(t *testing.T) {
	bin, dir, log := fakeTerraform(t, mechanicsPlan)
	withConfirmation(t, "yes\n", true)
	out, err := runCLI(t, "apply", "--bin", bin, "-C", dir, "--no-color")
	var code exitError
	if !errors.As(err, &code) || applied(t, log) || !strings.Contains(out, "type the directory's name") || !strings.Contains(out, "Not applied.") {
		t.Fatalf("answering yes: err = %v, applied = %v\n%s", err, applied(t, log), out)
	}

	withConfirmation(t, filepath.Base(dir)+"\n", true)
	if _, err := runCLI(t, "apply", "--bin", bin, "-C", dir, "--no-color"); err != nil || !applied(t, log) {
		t.Errorf("answering the name: err = %v, applied = %v", err, applied(t, log))
	}
}

// interragate expects the AWS plan's apply to fail on deletion protection,
// and the question says that before anything about findings.
func TestApplyExpectedToFailSaysSo(t *testing.T) {
	bin, dir, log := fakeTerraform(t, awsPlan)
	withConfirmation(t, "yes\n", true)
	out, err := runCLI(t, "apply", "--bin", bin, "-C", dir, "--no-color", "--fail-on", "none")
	if err == nil || applied(t, log) || !strings.Contains(out, "interragate expects this apply to fail part way") {
		t.Fatalf("err = %v, applied = %v\n%s", err, applied(t, log), out)
	}
}

func TestApplyOrdinaryPlanAsksForYes(t *testing.T) {
	bin, dir, log := fakeTerraform(t, mechanicsPlan)
	withConfirmation(t, "yes\n", true)
	out, err := runCLI(t, "apply", "--bin", bin, "-C", dir, "--no-color", "--fail-on", "critical")
	if err != nil || !applied(t, log) || !strings.Contains(out, `Only "yes" applies it`) {
		t.Errorf("err = %v, applied = %v\n%s", err, applied(t, log), out)
	}
}

func TestApplyAutoApproveRefusesRiskyPlans(t *testing.T) {
	bin, dir, log := fakeTerraform(t, mechanicsPlan)
	withConfirmation(t, "", false)
	_, err := runCLI(t, "apply", "--bin", bin, "-C", dir, "--no-color", "--auto-approve")
	if err == nil || !strings.Contains(err.Error(), "not applying") || applied(t, log) {
		t.Fatalf("err = %v, applied = %v", err, applied(t, log))
	}
	if _, err := runCLI(t, "apply", "--bin", bin, "-C", dir, "--no-color", "--auto-approve", "--fail-on", "none"); err != nil || !applied(t, log) {
		t.Errorf("--fail-on none: err = %v, applied = %v", err, applied(t, log))
	}
}

// Whether there is anything to apply is Terraform's call when the plan
// carries it (1.8 and later). Before that, changed outputs and drift
// recorded by a -refresh-only plan count, not just resource changes.
func TestApplyNothingToDo(t *testing.T) {
	const (
		output = `"output_changes":{"pet":{"actions":["create"],"before":null,"after":"x","after_unknown":false,"before_sensitive":false,"after_sensitive":false}}`
		drift  = `"resource_drift":[{"address":"terraform_data.x","mode":"managed","type":"terraform_data","name":"x","provider_name":"terraform.io/builtin/terraform","change":{"actions":["update"],"before":{"input":"a"},"after":{"input":"b"},"after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}]`
	)
	cases := []struct {
		name, plan, question string
	}{
		{"empty", ``, ""},
		{"outputs only", output, `Apply 1 output change with terraform?`},
		{"outputs only, applyable", `"applyable":true,` + output, `Apply 1 output change with terraform?`},
		{"refresh-only drift", drift, `Apply 1 change made outside Terraform with terraform?`},
		{"drift Terraform will not apply", `"applyable":false,` + drift, ""},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "plan.json")
		body := `{"format_version":"1.2","terraform_version":"1.16.6","resource_changes":[]`
		if c.plan != "" {
			body += "," + c.plan
		}
		if err := os.WriteFile(path, []byte(body+"}"), 0o644); err != nil {
			t.Fatal(err)
		}
		bin, dir, log := fakeTerraform(t, path)
		withConfirmation(t, "yes\n", true)
		out, err := runCLI(t, "apply", "--bin", bin, "-C", dir, "--no-color")
		if c.question == "" {
			if err != nil || applied(t, log) || !strings.Contains(out, "Nothing to apply.") {
				t.Errorf("%s: err = %v, applied = %v\n%s", c.name, err, applied(t, log), out)
			}
			continue
		}
		if err != nil || !applied(t, log) || !strings.Contains(out, c.question) {
			t.Errorf("%s: err = %v, applied = %v, want the question %q\n%s", c.name, err, applied(t, log), c.question, out)
		}
	}
}

// Which environment a plan is for is the question behind many a bad apply,
// so the question names the workspace when it is not the default.
func TestApplyNamesTheWorkspace(t *testing.T) {
	bin, dir, log := fakeTerraform(t, mechanicsPlan)
	t.Setenv("TF_WORKSPACE", "prod")
	withConfirmation(t, "yes\n", true)
	out, err := runCLI(t, "apply", "--bin", bin, "-C", dir, "--no-color", "--fail-on", "critical")
	if err != nil || !applied(t, log) || !strings.Contains(out, "to the prod workspace") || !strings.Contains(out, "workspace prod") {
		t.Errorf("err = %v, applied = %v\n%s", err, applied(t, log), out)
	}
}
