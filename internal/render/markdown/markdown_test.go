package markdown

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/rules"
)

func TestStaysUnderGitHubLimit(t *testing.T) {
	r := &model.Report{Generator: "interragate test", Risk: model.Risk{Level: model.Critical, Headline: "Lots."}}
	for i := 0; i < 900; i++ {
		r.Findings = append(r.Findings, &model.Finding{
			Rule: "stateful-destroy", Severity: model.Critical, Title: "Destroys an S3 bucket",
			Message:     fmt.Sprintf("Destroying `aws_s3_bucket.b%d` (an S3 bucket) deletes every object in the bucket. %s", i, strings.Repeat("More detail. ", 20)),
			Remediation: strings.Repeat("Back it up first. ", 10),
			Snippet:     fmt.Sprintf("removed {\n  from = aws_s3_bucket.b%d\n}", i),
			Addresses:   []string{fmt.Sprintf("aws_s3_bucket.b%d", i)},
		})
		r.Changes = append(r.Changes, &model.Change{Address: fmt.Sprintf("aws_s3_bucket.b%d", i), Action: model.Delete, Why: "Removed."})
	}
	var b bytes.Buffer
	if err := Render(&b, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if b.Len() > 65536 {
		t.Fatalf("comment is %d bytes; GitHub rejects more than 65536", b.Len())
	}
	if !strings.Contains(b.String(), "more findings; see the full report") {
		t.Error("truncation should be said, not silent")
	}
}

func TestHostileValuesAreInert(t *testing.T) {
	desc := "x` <img src=https://evil.example/p.png> </details> @security-team [click](https://evil.example)"
	msg := "Opens SSH on " + rules.Code("aws_security_group.app") + ". The rule is described as " + rules.Quote(desc) + "."
	r := &model.Report{
		Generator: "interragate test",
		Risk:      model.Risk{Level: model.High, Headline: msg},
		Narrative: []model.Sentence{{Severity: model.High, Text: msg}},
		Findings:  []*model.Finding{{Rule: "public-ingress", Severity: model.High, Title: "Opens SSH", Message: msg, Snippet: "moved {\n  from = a[\"```\"]\n}"}},
	}
	var b bytes.Buffer
	if err := Render(&b, r, Options{}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, bad := range []string{"<img", "</details> @", "@security-team", "[click](https://evil"} {
		if strings.Contains(out, bad) {
			t.Errorf("output contains %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "````hcl") {
		t.Error("a snippet containing ``` needs a longer fence")
	}
	if strings.Count(out, "<details>") != strings.Count(out, "</details>") {
		t.Error("details tags are unbalanced")
	}
}

// An address holding backticks, pipes or HTML cannot break out of its code
// span or the table.
func TestInlineCode(t *testing.T) {
	for in, want := range map[string]string{
		"aws_s3_bucket.logs":                      "`aws_s3_bucket.logs`",
		"aws_s3_bucket.x[\"a`b\"]":                "``aws_s3_bucket.x[\"a`b\"]``",
		"aws_s3_bucket.x[\"<img src=x>|[l](u)\"]": "`aws_s3_bucket.x[\"<img src=x>\\|[l](u)\"]`",
		"`edge": "`` `edge ``",
	} {
		if got := inlineCode(in, true); got != want {
			t.Errorf("inlineCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConsumersSection(t *testing.T) {
	r := &model.Report{Generator: "interragate test", Risk: model.Risk{Level: model.Medium, Headline: "Changes a value storefront-web holds."},
		Consumers: []*model.Consumer{
			{Name: "Acme Pay", Platform: "partner", Owners: []string{"@acme/payments", "ops@acme.example"}, Notify: true, Checked: true,
				Effects: []model.ConsumerEffect{{Kind: model.EffectReach, Severity: model.Medium, Text: "Comes in through `8443/tcp` on `aws_security_group.alb`, which this plan closes."}}},
			{Name: "storefront-web", Platform: "Vercel", Owners: []string{"@acme/web"}, Link: "https://vercel.com/acme/web\\", Checked: true,
				Effects: []model.ConsumerEffect{{Kind: model.EffectValue, Severity: model.High, Text: "Holds the `endpoint` of `aws_db_instance.orders`."}}},
			{Name: "admin", Checked: true, Effects: []model.ConsumerEffect{}},
			{Name: "billing", Checked: true, Hidden: 2, Effects: []model.ConsumerEffect{}},
			{Name: "reporting", Missing: []string{"aws_db_instance.ordres"}, Effects: []model.ConsumerEffect{}},
		}}
	var b bytes.Buffer
	if err := Render(&b, r, Options{}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	web := strings.Index(out, "**[storefront-web](https://vercel.com/acme/web%5C)** · Vercel · @\u200bacme/web")
	pay := strings.Index(out, "**Acme Pay** · partner · @acme/payments · ops@\u200bacme.example")
	if web < 0 || pay < 0 || web > pay {
		t.Errorf("services should be listed worst first, owners mentioned only when asked and only as handles:\n%s", out)
	}
	for _, want := range []string{
		"**Outside Terraform** · services you declared, which interragate cannot see",
		"  - 🟠 Holds the `endpoint` of `aws_db_instance.orders`.",
		"Not affected: admin. Left out by disable or ignore: billing. Declared to use what this plan does not have: reporting (`aws_db_instance.ordres`).",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
