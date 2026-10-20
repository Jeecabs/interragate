package rules

import (
	"fmt"
	"strings"

	"github.com/Jeecabs/interragate/internal/kb"
	"github.com/Jeecabs/interragate/internal/model"
)

// driftRules flag changes made outside Terraform that this plan undoes or
// redoes.
func driftRules() []*Rule {
	return []*Rule{
		{
			ID: "drift-revert", Title: "Reverts a change made outside Terraform", Severity: model.Medium, Category: Drift,
			Doc: "Compares the state Terraform had with what it found on refresh and flags differences this plan will undo, " +
				"such as a firewall rule or tag added by hand. That is often an emergency fix nobody wrote back into the code. " +
				"Provider noise (default tag re-attribution, timestamps, null versus empty) is ignored.",
			Check: checkDriftRevert,
		},
		{
			ID: "drift-deleted", Title: "Recreates a resource deleted outside Terraform", Severity: model.Medium, Category: Drift,
			Doc:   "Flags resources that were deleted outside Terraform and that this plan creates again, new and empty.",
			Check: checkDriftDeleted,
		},
	}
}

func checkDriftRevert(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, d := range c.Report.Drift {
		ch := c.Change(d.Address)
		if d.Noise || len(d.Reverted) == 0 || ch == nil || ch.Action != model.Update {
			continue
		}
		sev := model.Medium
		info := c.Info(ch)
		if info.Category == kb.Firewall || info.Category == kb.Identity {
			sev = model.High
		}
		onlyTags := true
		for _, p := range d.Reverted {
			if !strings.HasPrefix(p, "tags") && !strings.HasPrefix(p, "labels") {
				onlyTags = false
			}
		}
		if onlyTags {
			sev = model.Low
		}
		tool := c.Report.Plan.ToolName()
		what := strings.TrimPrefix(strings.TrimSuffix(firstSentence(d.Summary), "."), "Changed outside "+tool+": ")
		f := finding(ch, "Reverts a change made outside "+tool,
			fmt.Sprintf("%s was changed outside %s (%s), and this plan reverts it.", Code(ch.Address), tool, what))
		f.Severity = sev
		f.Short = "undoes a manual change to " + Article(info.Noun)
		f.Paths = d.Reverted
		f.Remediation = "If the outside change was a deliberate fix, copy it into the configuration before applying. Otherwise this plan is putting things back as intended."
		out = append(out, f)
	}
	return out
}

func checkDriftDeleted(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, d := range c.Report.Drift {
		ch := c.Change(d.Address)
		if d.Action != model.Delete || ch == nil || (ch.Action != model.Create && ch.Action != model.Replace) {
			continue
		}
		info := c.Info(ch)
		tool := c.Report.Plan.ToolName()
		msg := fmt.Sprintf("%s was deleted outside %s; this plan creates a new one.", Code(ch.Address), tool)
		sev := model.Medium
		if info.Stateful {
			msg += fmt.Sprintf(" The new %s starts empty: %s is gone unless it was backed up.", info.Noun, info.Loss)
			sev = model.High
		}
		f := finding(ch, "Recreates a resource deleted outside "+tool, msg)
		f.Severity = sev
		f.Short = "recreates " + Article(info.Noun) + " deleted outside " + tool
		f.Remediation = "Find out who deleted it and why before recreating it. If it should stay gone, remove it from the configuration."
		out = append(out, f)
	}
	return out
}
