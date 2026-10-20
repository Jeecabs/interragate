package sarif

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
)

func TestRender(t *testing.T) {
	r := &model.Report{
		Changes: []*model.Change{{Address: "aws_s3_bucket.logs", Location: &model.Location{File: "main.tf", Line: 12, EndLine: 16}}},
		Findings: []*model.Finding{{ID: "stateful-destroy:aws_s3_bucket.logs", Rule: "stateful-destroy", Severity: model.Critical, Title: "Destroys an S3 bucket",
			Message: "Destroying `aws_s3_bucket.logs` deletes every object.", Remediation: "Take a backup first.", Addresses: []string{"aws_s3_bucket.logs"}}},
	}
	var b bytes.Buffer
	if err := Render(&b, r, Options{Root: "infra", Version: "1.0"}); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	res := doc["runs"].([]any)[0].(map[string]any)["results"].([]any)[0].(map[string]any)
	if res["ruleId"] != "stateful-destroy" || res["level"] != "error" {
		t.Errorf("result = %v", res)
	}
	if msg := res["message"].(map[string]any)["text"]; msg != "Destroying aws_s3_bucket.logs deletes every object. Fix: Take a backup first." {
		t.Errorf("message = %v", msg)
	}
	loc := res["locations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)
	if loc["artifactLocation"].(map[string]any)["uri"] != "infra/main.tf" || loc["region"].(map[string]any)["startLine"] != float64(12) {
		t.Errorf("location = %v", loc)
	}
	rules := doc["runs"].([]any)[0].(map[string]any)["tool"].(map[string]any)["driver"].(map[string]any)["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["fullDescription"] == nil {
		t.Errorf("rules = %v", rules)
	}
}
