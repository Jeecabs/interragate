package analysis

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

func analyzeFixture(t *testing.T, name, sourceDir string) *model.Report {
	t.Helper()
	p, err := plan.ReadFile("../../testdata/plans/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return Analyze(p, Options{SourceDir: sourceDir, Generator: "interragate test"})
}

func findingsFor(r *model.Report, rule string) []*model.Finding {
	var out []*model.Finding
	for _, f := range r.Findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

func change(t *testing.T, r *model.Report, addr string) *model.Change {
	t.Helper()
	for _, c := range r.Changes {
		if c.Address == addr {
			return c
		}
	}
	t.Fatalf("no change %s", addr)
	return nil
}

func expectFinding(t *testing.T, r *model.Report, rule, addr string, sev model.Severity) *model.Finding {
	t.Helper()
	for _, f := range findingsFor(r, rule) {
		if slices.Contains(f.Addresses, addr) {
			if f.Severity != sev {
				t.Errorf("%s on %s: severity %s, want %s", rule, addr, f.Severity, sev)
			}
			return f
		}
	}
	t.Errorf("expected %s on %s; findings: %v", rule, addr, ruleList(r))
	return nil
}

func ruleList(r *model.Report) []string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, f.Rule+":"+strings.Join(f.Addresses, ","))
	}
	return out
}

func TestAWSWebapp(t *testing.T) {
	for _, tc := range []struct{ file, tool string }{
		{"aws-webapp.terraform.json", "terraform"},
		{"aws-webapp.tofu.json", "opentofu"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			r := analyzeFixture(t, tc.file, "../../testdata/scenarios/aws-webapp/v2")
			if r.Plan.Tool != tc.tool {
				t.Errorf("tool = %q", r.Plan.Tool)
			}
			checkAWSWebapp(t, r)
		})
	}
}

func checkAWSWebapp(t *testing.T, r *model.Report) {
	t.Helper()

	if r.Risk.Level != model.Critical {
		t.Errorf("risk = %s", r.Risk.Level)
	}
	if !strings.Contains(r.Risk.Headline, "S3 bucket") || !strings.Contains(r.Risk.Headline, "SSH") {
		t.Errorf("headline = %q", r.Risk.Headline)
	}
	s := r.Summary
	if s.Create != 3 || s.Update != 23 || s.Replace != 5 || s.Delete != 5 || s.Cosmetic != 13 || s.Drifted != 2 {
		t.Errorf("summary = %+v", s)
	}
	if !r.Plan.SourceRead {
		t.Errorf("plan info = %+v", r.Plan)
	}

	expectFinding(t, r, "stateful-destroy", "aws_s3_bucket.logs", model.Critical)
	expectFinding(t, r, "stateful-replace", "aws_db_instance.orders", model.Critical)
	expectFinding(t, r, "protected-delete", "aws_db_instance.orders", model.High)
	expectFinding(t, r, "final-snapshot-disabled", "aws_db_instance.orders", model.Medium)
	expectFinding(t, r, "public-ingress", "aws_security_group.app", model.Critical)
	expectFinding(t, r, "iam-wildcard", "aws_iam_role_policy.app", model.High)
	expectFinding(t, r, "public-bucket", "aws_s3_bucket_public_access_block.assets", model.High)
	expectFinding(t, r, "missing-moved-block", "aws_instance.web", model.High)
	expectFinding(t, r, "drift-revert", "aws_security_group.alb", model.High)
	expectFinding(t, r, "drift-revert", "aws_lb.web", model.Low)
	expectFinding(t, r, "dns-change", "aws_route53_record.www", model.Low)
	expectFinding(t, r, "output-removed", "output.logs_bucket", model.Low)
	shift := expectFinding(t, r, "count-index-shift", "module.network.aws_subnet.private[1]", model.High)
	if shift != nil {
		if !slices.Contains(shift.Addresses, "module.network.aws_subnet.public[2]") {
			t.Errorf("public and private subnets shift together: %v", shift.Addresses)
		}
		if !strings.Contains(shift.Snippet, `to   = module.network.aws_subnet.private["us-east-1c"]`) || strings.Contains(shift.Snippet, `"us-east-1b"`) {
			t.Errorf("for_each migration snippet:\n%s", shift.Snippet)
		}
	}

	// Only what the plan introduces: the ALB's existing public 443 is not news.
	for _, f := range findingsFor(r, "public-ingress") {
		if slices.Contains(f.Addresses, "aws_security_group.alb") {
			t.Errorf("existing HTTPS ingress should not be reported: %s", f.Message)
		}
	}
	// A count shift explains the subnets; network-disruption must not repeat it.
	for _, f := range findingsFor(r, "network-disruption") {
		if strings.Contains(strings.Join(f.Addresses, ","), "aws_subnet") {
			t.Errorf("subnets reported twice: %s", f.Message)
		}
	}
	if len(findingsFor(r, "mass-destroy")) != 0 {
		t.Error("10 of 39 is not a mass destroy")
	}

	ssm := change(t, r, "aws_ssm_parameter.db_endpoint")
	if !ssm.Indirect || len(ssm.Causes) == 0 || ssm.Causes[0].Resource != "aws_db_instance.orders" || ssm.Causes[0].Via != "address" {
		t.Errorf("ssm parameter should follow the database: %+v", ssm.Causes)
	}
	db := change(t, r, "aws_db_instance.orders")
	if !slices.Equal(db.ForcedBy, []string{"kms_key_id", "storage_encrypted"}) {
		t.Errorf("forced by = %v", db.ForcedBy)
	}
	if db.Location == nil || db.Location.File != "main.tf" {
		t.Errorf("location = %+v", db.Location)
	}
	if c := change(t, r, "aws_kms_key.data"); c.Cosmetic != "tags" {
		t.Errorf("kms key only changes tags, got %q", c.Cosmetic)
	}

	var real []string
	for _, d := range r.Drift {
		if !d.Noise {
			real = append(real, d.Address)
		}
	}
	if !slices.Equal(real, []string{"aws_lb.web", "aws_security_group.alb"}) {
		t.Errorf("real drift = %v", real)
	}
	if len(r.Patterns) == 0 || r.Patterns[0].Path != "tags_all.Team" || r.Patterns[0].Count != 19 {
		t.Errorf("patterns = %+v", r.Patterns)
	}

	assertNoSecrets(t, r, "fixture-password-123")
}

func TestMechanics(t *testing.T) {
	for _, tc := range []struct{ file, tool string }{
		{"mechanics.terraform.json", "terraform"},
		{"mechanics.tofu.json", "opentofu"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			r := analyzeFixture(t, tc.file, "../../testdata/scenarios/mechanics/v2")
			if r.Plan.Tool != tc.tool {
				t.Errorf("tool = %q", r.Plan.Tool)
			}
			s := r.Summary
			if s.Move != 1 || s.Import != 1 || s.Forget != 1 || s.Read != 1 {
				t.Errorf("summary = %+v", s)
			}
			expectFinding(t, r, "import-replace", "random_string.imported", model.High)
			expectFinding(t, r, "count-index-shift", "null_resource.worker[1]", model.Medium)
			expectFinding(t, r, "missing-moved-block", "random_string.api_key", model.Medium)
			expectFinding(t, r, "drift-deleted", "local_file.readme", model.Medium)
			expectFinding(t, r, "secret-output", "output.api_key", model.Medium)
			expectFinding(t, r, "output-removed", "output.legacy_endpoint", model.Low)
			expectFinding(t, r, "provider-unpinned", "provider.null", model.Low)

			if c := change(t, r, "random_string.session_key"); !c.Moved || c.PrevAddress != "random_string.session_secret" {
				t.Errorf("move not recognised: %+v", c)
			}
			if c := change(t, r, "random_uuid.legacy_id"); c.Action != model.Forget {
				t.Errorf("forget = %s", c.Action)
			}
			if c := change(t, r, "terraform_data.migrations"); !strings.Contains(c.Why, "terraform_data.deploy") {
				t.Errorf("replace_triggered_by should name its trigger: %q", c.Why)
			}
			if c := change(t, r, "local_file.policy"); !c.Indirect || !strings.Contains(c.Why, "random_id.suffix") {
				t.Errorf("policy file follows random_id.suffix: %q", c.Why)
			}
			if c := change(t, r, "random_integer.port"); !strings.Contains(c.Why, "Tainted") {
				t.Errorf("tainted why = %q", c.Why)
			}
			if c := change(t, r, "null_resource.queue[\"reports\"]"); !strings.Contains(c.Why, "reports") {
				t.Errorf("each key why = %q", c.Why)
			}
			if c := change(t, r, "random_shuffle.az"); c.Order != "create-first" {
				t.Errorf("order = %q", c.Order)
			}
			assertNoSecrets(t, r, "BJlKzxqRQV7X5VtzDfr1VAoK", "seed-one", "seed-two")
		})
	}
}

// Locals are invisible in plan JSON; reading the source connects them.
func TestSourceConnectsLocals(t *testing.T) {
	with := analyzeFixture(t, "mechanics.terraform.json", "../../testdata/scenarios/mechanics/v2")
	without := analyzeFixture(t, "mechanics.terraform.json", "")
	has := func(r *model.Report) bool {
		for _, e := range r.Graph.Edges {
			if e == [2]string{"random_pet.cluster", "terraform_data.deploy"} {
				return true
			}
		}
		return false
	}
	if !has(with) || has(without) {
		t.Fatalf("edge through local.labels: with source %v, without %v", has(with), has(without))
	}
	if without.Plan.SourceRead {
		t.Error("source should not be marked as read")
	}
}

func assertNoSecrets(t *testing.T, r *model.Report, secrets ...string) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		if strings.Contains(string(data), s) {
			t.Errorf("report leaks sensitive value %q", s)
		}
	}
}
