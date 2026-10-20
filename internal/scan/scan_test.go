package scan

import (
	"os"
	"strings"
	"testing"
)

func TestParseTrivy(t *testing.T) {
	data, err := os.ReadFile("testdata/trivy.json")
	if err != nil {
		t.Fatal(err)
	}
	issues, err := Parse("trivy", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) == 0 {
		t.Fatal("no issues")
	}
	first := issues[0]
	if first.Tool != "trivy" || first.ID != "AWS-0017" || first.Severity != "low" || first.File != "main.tf" || first.Resource != "aws_cloudwatch_log_group.app" || first.Start != 292 {
		t.Errorf("first issue = %+v", first)
	}
	// Inside a module, the resource comes from the occurrence in its file.
	var module *Issue
	for i := range issues {
		if issues[i].File == "modules/network/main.tf" {
			module = &issues[i]
			break
		}
	}
	if module == nil || module.Resource == "module.network" || module.Resource == "" {
		t.Errorf("module issue = %+v", module)
	}
}

func TestParseCheckovAndTFLint(t *testing.T) {
	checkov := `[{"check_type":"terraform","results":{"failed_checks":[{"check_id":"CKV_AWS_18","check_name":"Ensure the S3 bucket has access logging enabled","file_path":"/main.tf","file_line_range":[12,20],"resource":"aws_s3_bucket.logs","severity":null,"guideline":"https://example.com/ckv18"}]}}]`
	issues, err := Parse("checkov", []byte(checkov))
	if err != nil || len(issues) != 1 {
		t.Fatalf("checkov: %v %+v", err, issues)
	}
	if is := issues[0]; is.File != "main.tf" || is.Start != 12 || is.End != 20 || is.Severity != "medium" || is.Resource != "aws_s3_bucket.logs" {
		t.Errorf("checkov issue = %+v", is)
	}
	tflint := `{"issues":[{"rule":{"name":"terraform_unused_declarations","severity":"warning","link":"https://example.com/tflint"},"message":"variable \"x\" is declared but not used","range":{"filename":"variables.tf","start":{"line":3,"column":1},"end":{"line":3,"column":15}}}],"errors":[]}`
	issues, err = Parse("tflint", []byte(tflint))
	if err != nil || len(issues) != 1 || issues[0].Severity != "low" || issues[0].File != "variables.tf" || issues[0].Start != 3 {
		t.Fatalf("tflint: %v %+v", err, issues)
	}
	if _, err := Parse("trivy", []byte("not json")); err == nil {
		t.Error("parsed garbage")
	}
}

func TestParseRealCheckov(t *testing.T) {
	data, err := os.ReadFile("testdata/checkov.json")
	if err != nil {
		t.Fatal(err)
	}
	issues, err := Parse("checkov", data)
	if err != nil || len(issues) == 0 {
		t.Fatalf("checkov: %v %d", err, len(issues))
	}
	for _, is := range issues {
		if is.File == "" || is.Start == 0 || is.ID == "" || is.Resource == "" {
			t.Errorf("incomplete issue: %+v", is)
		}
	}
}

// tflint's errors mean it did not check everything, which is not the same
// as finding nothing.
func TestTFLintErrors(t *testing.T) {
	_, err := Parse("tflint", []byte(`{"issues":[],"errors":[{"message":"Plugin \"aws\" not found. Did you run \"tflint --init\"?","severity":"error"}]}`))
	if err == nil || !strings.Contains(err.Error(), "tflint --init") {
		t.Errorf("err = %v", err)
	}
}
