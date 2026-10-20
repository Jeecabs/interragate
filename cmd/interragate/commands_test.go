package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
)

func TestDemo(t *testing.T) {
	out, err := runCLI(t, "demo", "--no-open", "--no-color")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"aws-webapp", "sample plan", "Destroys an S3 bucket", "Map and diffs: interragate demo aws"} {
		if !strings.Contains(out, want) {
			t.Errorf("demo output lacks %q", want)
		}
	}

	out, err = runCLI(t, "demo", "mechanics", "-f", "json")
	if err != nil {
		t.Fatal(err)
	}
	var r model.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("demo -f json is not a report: %v", err)
	}
	// The sample's configuration was read, so changes have locations.
	if !r.Plan.SourceRead || r.Plan.Dir != "mechanics" {
		t.Errorf("plan info = %+v, want the sample's configuration read from a directory named mechanics", r.Plan)
	}

	if _, err := runCLI(t, "demo", "gcp"); err == nil || !strings.Contains(err.Error(), "try aws or mechanics") {
		t.Errorf("unknown sample: err = %v", err)
	}
}

func TestNoConfigPointsAtSubdirectories(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"envs/prod", "envs/staging", "modules/network", ".terraform/modules/x", "docs"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if dir != "docs" {
			if err := os.WriteFile(filepath.Join(root, dir, "main.tf"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	got := configDirs(root, 3, 6)
	want := []string{filepath.Join("envs", "prod"), filepath.Join("envs", "staging")}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("configDirs = %v, want %v (child modules and caches skipped)", got, want)
	}
	_, err := runCLI(t, "-C", root)
	if err == nil || !strings.Contains(err.Error(), "these directories have some") || !strings.Contains(err.Error(), "interragate -C "+want[0]) {
		t.Errorf("err = %v", err)
	}
}

func TestDoctor(t *testing.T) {
	dir := t.TempDir()
	out, err := runCLI(t, "doctor", "-C", dir, "--no-color")
	var code exitError
	if !errors.As(err, &code) || code != 1 {
		t.Fatalf("doctor in an empty directory: err = %v, want exit 1", err)
	}
	if !strings.Contains(out, "has no Terraform configuration") || !strings.Contains(out, "interragate demo") {
		t.Errorf("doctor output:\n%s", out)
	}

	if err := os.WriteFile(filepath.Join(dir, "main.tf"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".interragate.yaml"), []byte("fail_on: sometimes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ = runCLI(t, "doctor", "-C", dir, "--no-color")
	for _, want := range []string{"has Terraform configuration (1 file)", "has not been initialised yet", "cannot be used"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
}

func TestCompletion(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		out, err := runCLI(t, "completion", shell)
		if err != nil {
			t.Fatal(err)
		}
		// Every command and flag is offered, and explain completes
		// addresses from the plan.
		for _, want := range []string{"doctor", "demo", "fail-on", "no-init", "__addresses"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s completion lacks %q", shell, want)
			}
		}
	}
	if _, err := runCLI(t, "completion", "tcsh"); err == nil {
		t.Error("an unknown shell should fail")
	}
}

func TestAddresses(t *testing.T) {
	out, err := runCLI(t, "__addresses", mechanicsPlan)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	has := map[string]bool{}
	for _, l := range lines {
		has[l] = true
	}
	for _, want := range []string{"null_resource.queue", `null_resource.queue["orders"]`, "random_string.imported"} {
		if !has[want] {
			t.Errorf("addresses lack %s", want)
		}
	}
	// Completion must never print errors into the shell.
	if out, err := runCLI(t, "__addresses", "no-such-plan.json"); err != nil || out != "" {
		t.Errorf("a missing plan gave %q, %v", out, err)
	}
}

func TestDiff(t *testing.T) {
	out, err := runCLI(t, "diff", mechanicsPlan, awsPlan, "--no-color")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"→", "Critical risk", "The verdict rose from High risk to Critical risk.", "New: Opens SSH (22/tcp) to the internet on aws_security_group.app."} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output lacks %q:\n%s", want, out)
		}
	}
	// A plan compared with itself.
	out, err = runCLI(t, "diff", awsPlan, awsPlan, "-f", "markdown")
	if err != nil || !strings.Contains(out, "- The plan is the same as before.") {
		t.Errorf("same plan: %v\n%s", err, out)
	}
	// --fail-on turns new findings into exit status 2, for CI.
	_, err = runCLI(t, "diff", mechanicsPlan, awsPlan, "--fail-on", "critical")
	var code exitError
	if !errors.As(err, &code) || code != 2 {
		t.Errorf("new critical finding with --fail-on critical: err = %v, want exit 2", err)
	}
	if _, err := runCLI(t, "diff", awsPlan, mechanicsPlan, "--fail-on", "critical"); err != nil {
		t.Errorf("no new critical findings: err = %v", err)
	}
	if _, err := runCLI(t, "diff", awsPlan); err == nil {
		t.Error("diff with one plan should fail")
	}
}
