package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
)

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := run(context.Background(), args, &out, &errOut)
	return out.String() + errOut.String(), err
}

const awsPlan = "../../testdata/plans/aws-webapp.terraform.json"
const mechanicsPlan = "../../testdata/plans/mechanics.terraform.json"

func TestShowJSON(t *testing.T) {
	out, err := runCLI(t, "show", awsPlan, "--format", "json", "-C", "../../testdata/scenarios/aws-webapp/v2")
	if err != nil {
		t.Fatal(err)
	}
	var r model.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("output is not a report: %v", err)
	}
	if r.Risk.Level != model.Critical || len(r.Findings) == 0 {
		t.Errorf("risk %s, %d findings", r.Risk.Level, len(r.Findings))
	}
	if r.Apply == nil || len(r.Apply.Steps) == 0 || r.Apply.Duration == 0 || len(r.Apply.Gaps) == 0 {
		t.Errorf("apply = %+v", r.Apply)
	}
}

func TestCheckExitCodes(t *testing.T) {
	_, err := runCLI(t, "check", awsPlan, "--no-color")
	var code exitError
	if !errors.As(err, &code) || code != 2 {
		t.Fatalf("check on a critical plan: err = %v, want exit 2", err)
	}
	if _, err := runCLI(t, "check", mechanicsPlan, "--fail-on", "critical"); err != nil {
		t.Fatalf("no critical findings, want success, got %v", err)
	}
	if _, err := runCLI(t, "check", mechanicsPlan, "--fail-on", "bogus"); err == nil {
		t.Fatal("expected an error for an unknown severity")
	}
}

func TestExplain(t *testing.T) {
	out, err := runCLI(t, "explain", awsPlan, "aws_db_instance.orders", "--no-color")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"aws_db_instance.orders", "storage_encrypted", "forces replacement", "Findings"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q", want)
		}
	}
	if _, err := runCLI(t, "explain", awsPlan, "aws_nope.nope"); err == nil {
		t.Error("expected an error for an unknown address")
	}
}

func TestArgs(t *testing.T) {
	o, err := parseArgs([]string{"plan.json", "-f", "md", "--open", "--", "-var-file=prod.tfvars"})
	if err != nil {
		t.Fatal(err)
	}
	if o.format != "markdown" || !o.open || len(o.args) != 1 || len(o.extra) != 1 || o.extra[0] != "-var-file=prod.tfvars" {
		t.Errorf("parsed %+v", o)
	}
	if _, err := parseArgs([]string{"--nope"}); err == nil {
		t.Error("unknown flags should fail")
	}
	if _, err := parseArgs([]string{"--format", "yaml"}); err == nil {
		t.Error("unknown formats should fail")
	}
}

func TestRulesList(t *testing.T) {
	out, err := runCLI(t, "rules", "--format", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## stateful-destroy") || !strings.Contains(out, "## count-index-shift") {
		t.Error("rule docs missing")
	}
}

func TestRejectsStateFiles(t *testing.T) {
	_, err := runCLI(t, "show", "../../testdata/plans/not-a-plan.json")
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("err = %v", err)
	}
}

func TestFailOnNoneAndExtraOutputs(t *testing.T) {
	dir := t.TempDir()
	md, js, page := dir+"/c.md", dir+"/r.json", dir+"/r.html"
	if _, err := runCLI(t, "check", awsPlan, "--fail-on", "none", "--markdown", md, "--json", js, "--html", page); err != nil {
		t.Fatalf("fail-on none should never fail: %v", err)
	}
	for _, p := range []string{md, js, page} {
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			t.Errorf("%s not written: %v", p, err)
		}
	}
}

func TestConfigFailOn(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/.interragate.yaml"
	os.WriteFile(cfg, []byte("fail_on: critical\n"), 0o644)
	// mechanics has high findings but no critical ones.
	if _, err := runCLI(t, "check", mechanicsPlan, "--config", cfg); err != nil {
		t.Fatalf("config fail_on critical: %v", err)
	}
}

func TestPlanNeedsConfiguration(t *testing.T) {
	_, err := runCLI(t, "plan", "-C", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "no Terraform configuration") {
		t.Fatalf("err = %v", err)
	}
}

func TestGitHubAnnotations(t *testing.T) {
	out, err := runCLI(t, "show", awsPlan, "-f", "github", "-C", "../../testdata/scenarios/aws-webapp/v2")
	if err != nil {
		t.Fatal(err)
	}
	want := "::error file=testdata/scenarios/aws-webapp/v2/main.tf,line="
	if !strings.Contains(out, want) {
		t.Fatalf("annotations lack %q:\n%s", want, out[:min(len(out), 600)])
	}
	if strings.Count(out, "\n::") < 10 {
		t.Errorf("expected one annotation per finding:\n%s", out)
	}
}

func TestFlagsBeforeSubcommand(t *testing.T) {
	out, err := runCLI(t, "-f", "json", "show", awsPlan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("expected JSON, got %q", out[:min(len(out), 80)])
	}
}

func TestByteOrderMark(t *testing.T) {
	data, err := os.ReadFile(mechanicsPlan)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/bom.json"
	os.WriteFile(path, append([]byte("\xef\xbb\xbf"), data...), 0o644)
	if _, err := runCLI(t, "show", path, "--no-color"); err != nil {
		t.Fatalf("plan with a byte-order mark: %v", err)
	}
}

func TestBadFailOnWritesNothing(t *testing.T) {
	out := t.TempDir() + "/keep.md"
	os.WriteFile(out, []byte("previous"), 0o644)
	if _, err := runCLI(t, "check", awsPlan, "--fail-on", "bogus", "-o", out); err == nil {
		t.Fatal("expected an error")
	}
	if data, _ := os.ReadFile(out); string(data) != "previous" {
		t.Error("the output file was truncated before the flag was validated")
	}
}

func TestReportWritesExtraOutputs(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)
	plan := wd + "/" + awsPlan
	if _, err := runCLI(t, "report", plan, "--json", dir+"/r.json"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir + "/interragate-report.html", dir + "/r.json"} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}
