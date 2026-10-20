package rules

import (
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/scan"
)

// scannerRules make a rule for each scanner, which carries what it reports
// on the plan's changes, so its findings take severity overrides, ignores
// and fail_on like any other: disable: [trivy] turns them off.
func scannerRules() []*Rule {
	out := make([]*Rule, 0, len(scan.Tools))
	for _, tool := range scan.Tools {
		out = append(out, &Rule{
			ID: tool, Title: "Flagged by " + tool, Severity: model.Medium, Category: Scanner,
			Doc: "Shows what " + tool + " reports on resources this plan creates or replaces, or on lines you edited, when interragate runs it " +
				"(--scan " + tool + "). Issues on what the plan leaves as it was are counted, not listed.",
			Check: func(c *Context) []*model.Finding { return c.Scanned[tool] },
		})
	}
	return out
}
