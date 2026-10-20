package text

import (
	"bytes"
	"testing"

	"github.com/Jeecabs/interragate/internal/delta"
	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
)

// deployed is a report for a plan that updates terraform_data.deploy's
// input from v1.
func deployed(risk model.Severity, input string, findings ...*model.Finding) *model.Report {
	n := diff.Compute(diff.Input{Before: map[string]any{"input": "v1"}, After: map[string]any{"input": input}, HasBefore: true, HasAfter: true})
	n.Prune(nil)
	return &model.Report{
		Risk:     model.Risk{Level: risk},
		Summary:  model.Summary{Update: 1},
		Changes:  []*model.Change{{Address: "terraform_data.deploy", Action: model.Update, Diff: n}},
		Findings: findings,
	}
}

// After an edit that only changes a value, the terminal says what the plan
// now sets, not that it is the same as before; the verdict and the
// sentence about it use the same words.
func TestRenderDelta(t *testing.T) {
	for _, c := range []struct {
		name          string
		before, after *model.Report
		want          string
	}{
		{"a new value", deployed(model.Info, "v2"), deployed(model.Info, "v3"),
			"10:00:00  main.tf changed.\n" +
				"  Low risk  ~1 update\n" +
				"  · terraform_data.deploy now sets input to \"v3\".\n"},
		{"the same plan", deployed(model.Info, "v2"), deployed(model.Info, "v2"),
			"10:00:00  main.tf changed.\n" +
				"  Low risk  ~1 update\n" +
				"  ✓ The plan is the same as before.\n"},
		{"a finding fixed", deployed(model.High, "v2", &model.Finding{Rule: "r", Severity: model.High, Title: "Opens port 8080/tcp to the internet", Addresses: []string{"terraform_data.deploy"}}), deployed(model.Info, "v2"),
			"10:00:00  main.tf changed.\n" +
				"  Low risk  ~1 update\n" +
				"  ✓ The verdict fell from High risk to Low risk.\n" +
				"  ✓ Fixed: Opens port 8080/tcp to the internet.\n"},
	} {
		var b bytes.Buffer
		if err := RenderDelta(&b, c.after, delta.Compare(c.before, c.after), "10:00:00  main.tf changed.", Options{Width: 100}); err != nil {
			t.Fatal(err)
		}
		if got := b.String(); got != c.want {
			t.Errorf("%s:\n%s\nwant:\n%s", c.name, got, c.want)
		}
	}
}
