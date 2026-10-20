package analysis

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

// planJSON builds a plan from raw resource_changes and resource_drift.
func planJSON(t *testing.T, changes, drift string) *plan.Plan {
	t.Helper()
	doc := `{"format_version":"1.2","terraform_version":"1.16.0","planned_values":{},` +
		`"resource_changes":` + changes + `,"resource_drift":` + or(drift, "[]") + `}`
	p, err := plan.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sgChange(actions string, before, after string) string {
	return `{"address":"aws_security_group.web","mode":"managed","type":"aws_security_group","name":"web",` +
		`"provider_name":"registry.terraform.io/hashicorp/aws","change":{"actions":["` + actions + `"],` +
		`"before":` + before + `,"after":` + after + `,"after_unknown":{}}}`
}

func TestDriftRemovedFirstElementIsReverted(t *testing.T) {
	rule := func(port int) string {
		b, _ := json.Marshal(map[string]any{"from_port": port, "to_port": port, "protocol": "tcp", "cidr_blocks": []string{"10.0.0.0/8"}})
		return string(b)
	}
	a, b, c := rule(22), rule(443), rule(8080)
	state := `{"ingress":[` + a + `,` + b + `,` + c + `]}`
	real := `{"ingress":[` + b + `,` + c + `]}`
	p := planJSON(t,
		`[`+sgChange("update", real, state)+`]`,
		`[`+sgChange("update", state, real)+`]`)
	r := Analyze(p, Options{})
	if len(r.Drift) != 1 || len(r.Drift[0].Reverted) == 0 {
		t.Fatalf("drift = %+v", r.Drift)
	}
	if !strings.Contains(r.Drift[0].Summary, "reverts") {
		t.Errorf("summary = %q", r.Drift[0].Summary)
	}
	if len(findingsFor(r, "drift-revert")) != 1 {
		t.Errorf("findings = %v", ruleList(r))
	}
}

func TestDeposedDatabaseIsFlagged(t *testing.T) {
	p := planJSON(t, `[
	  {"address":"aws_db_instance.main","mode":"managed","type":"aws_db_instance","name":"main","deposed":"4a2b7c1d",
	   "provider_name":"registry.terraform.io/hashicorp/aws",
	   "change":{"actions":["delete"],"before":{"deletion_protection":true,"skip_final_snapshot":true},"after":null,"after_unknown":{}}},
	  {"address":"aws_db_instance.main","mode":"managed","type":"aws_db_instance","name":"main",
	   "provider_name":"registry.terraform.io/hashicorp/aws",
	   "change":{"actions":["no-op"],"before":{"deletion_protection":true},"after":{"deletion_protection":true},"after_unknown":{}}}
	]`, "")
	r := Analyze(p, Options{})
	deposed := "aws_db_instance.main (deposed object 4a2b7c1d)"
	expectFinding(t, r, "stateful-destroy", deposed, model.Critical)
	expectFinding(t, r, "protected-delete", deposed, model.High)
	if r.Risk.Level != model.Critical {
		t.Errorf("risk = %s", r.Risk.Level)
	}
	seen := map[string]bool{}
	for _, c := range r.Changes {
		if seen[c.Address] {
			t.Errorf("duplicate address %s", c.Address)
		}
		seen[c.Address] = true
	}
}

func TestSensitiveDNSRecordIsNotQuoted(t *testing.T) {
	p := planJSON(t, `[{"address":"aws_route53_record.verify","mode":"managed","type":"aws_route53_record","name":"verify",
	  "provider_name":"registry.terraform.io/hashicorp/aws",
	  "change":{"actions":["update"],
	    "before":{"name":"_verify.example.com","type":"TXT","records":["old-TOKEN"],"ttl":300},
	    "after":{"name":"_verify.example.com","type":"TXT","records":["new-TOKEN-SECRET"],"ttl":300},
	    "after_unknown":{},"before_sensitive":{"records":true},"after_sensitive":{"records":true}}}]`, "")
	r := Analyze(p, Options{})
	assertNoSecrets(t, r, "old-TOKEN", "new-TOKEN-SECRET")
}

func TestOddLocalsDoNotCrash(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`
variable "flags" { default = { true = "on" } }
locals {
  mode  = var.flags[true]
  other = var.flags[null]
}
resource "terraform_data" "x" { input = local.mode }
`), 0o644)
	p := planJSON(t, `[{"address":"terraform_data.x","mode":"managed","type":"terraform_data","name":"x",
	  "provider_name":"terraform.io/builtin/terraform","change":{"actions":["create"],"before":null,"after":{"input":"on"},"after_unknown":{"id":true}}}]`, "")
	r := Analyze(p, Options{SourceDir: dir})
	if len(r.Changes) != 1 {
		t.Fatalf("changes = %d", len(r.Changes))
	}
}

func TestEmptyListsAreNotNull(t *testing.T) {
	r := Analyze(planJSON(t, `[]`, ""), Options{})
	data, _ := json.Marshal(r)
	for _, field := range []string{`"changes":[]`, `"findings":[]`, `"drift":[]`, `"outputs":[]`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("report lacks %s", field)
		}
	}
}

func TestOldPlansAreRejected(t *testing.T) {
	_, err := plan.Parse([]byte(`{"format_version":"0.1","terraform_version":"0.13.7","planned_values":{}}`))
	if err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("err = %v", err)
	}
}

// Terraform masks a value only where the schema says so; the same secret
// often reappears unmarked (terraform_data mirrors input into output).
func TestSecretsAreRedactedEverywhere(t *testing.T) {
	p, err := plan.ReadFile("../../testdata/plans/redact.json")
	if err != nil {
		t.Fatal(err)
	}
	r := Analyze(p, Options{})
	assertNoSecrets(t, r, "hunter2-SECRET")
	if c := change(t, r, "terraform_data.cfg"); c.Diff == nil {
		t.Fatal("diff missing after redaction")
	}
}

func TestNullEntriesAreSkipped(t *testing.T) {
	p, err := plan.Parse([]byte(`{"format_version":"1.2","planned_values":{},"resource_changes":[null,{"address":"null_resource.a","mode":"managed","type":"null_resource","name":"a","provider_name":"registry.terraform.io/hashicorp/null","change":{"actions":["create"],"before":null,"after":{},"after_unknown":{"id":true}}}],"resource_drift":[null],"output_changes":{"x":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r := Analyze(p, Options{}); len(r.Changes) != 1 {
		t.Fatalf("changes = %d", len(r.Changes))
	}
}
