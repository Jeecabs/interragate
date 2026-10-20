package rules

import (
	"fmt"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
)

// untiedRules flag destroys and replacements no edit on the branch
// explains.
func untiedRules() []*Rule {
	return []*Rule{
		{
			ID: "untied-destroy", Title: "Destroys something no edit explains", Severity: model.Medium, Category: Correctness,
			Doc: "When interragate compares the configuration with git, flags a destroy or replacement that none of the edits explain: nothing in " +
				"the resource's block, its module call or what it reads changed, and it does not follow from a change that did. It may come " +
				"from a variable or .tfvars change, a provider upgrade or a change on the base; either way it is worth knowing before the " +
				"apply. Only plans whose configuration files changed are checked, and not destroy plans, which -destroy explains.",
			Check: checkUntiedDestroy,
		},
	}
}

// selfExplained are the reasons a plan gives for a replacement that no
// edit needs to explain.
var selfExplained = map[string]bool{"replace_because_tainted": true, "replace_by_request": true, "replace_by_triggers": true}

// Traced reports whether interragate tied the plan's changes to the edits
// behind them: it compared the configuration with git, configuration files
// had changed and it read them. Only then does a change without an edit
// say anything; with nothing edited, every change is without one. A
// destroy plan is not traced either, as -destroy explains every change.
func Traced(r *model.Report) bool {
	return r.Git != nil && len(r.Git.Files) > 0 && r.Plan.SourceRead && !r.Plan.Destroy
}

func checkUntiedDestroy(c *Context) []*model.Finding {
	r := c.Report
	if !Traced(r) {
		return nil
	}
	moved := map[string]model.ProviderChange{}
	for _, p := range r.Git.Providers {
		if p.From != "" && p.To != "" {
			moved[p.Source[strings.LastIndex(p.Source, "/")+1:]] = p
		}
	}
	whose := "your edits since " + Code(r.Git.Base)
	if r.Git.Base == "HEAD" {
		whose = "your uncommitted edits"
	}
	var out []*model.Finding
	for _, ch := range c.Changes(model.Delete, model.Replace) {
		// Drift has rules of its own, and a taint, a -replace or a
		// replace_triggered_by is an explanation the plan gives itself.
		if !Managed(ch) || ch.Origin != "" || ch.Deposed != "" || ch.Drifted || selfExplained[ch.Reason] {
			continue
		}
		verb := "destroyed"
		if ch.Action == model.Replace {
			verb = "replaced"
		}
		msg := fmt.Sprintf("%s is %s, but none of %s touch it or anything it depends on.", Code(ch.Address), verb, whose)
		if p, ok := moved[ch.Provider]; ok {
			msg += fmt.Sprintf(" The %s provider moves from %s to %s, which may be why.", Code(ch.Provider), p.From, p.To)
		} else {
			msg += " It may come from a variable or .tfvars change, a provider upgrade or a change on the base."
		}
		f := finding(ch, "", msg)
		f.Short = "destroys something no edit explains"
		f.Remediation = "Find out why before applying: check the variable files, the dependency lock file and what changed on the base."
		out = append(out, f)
	}
	return out
}
