package rules

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

// hygieneRules flag failed or incomplete plans and checks, removed outputs,
// unpinned providers and modules, and missing tags.
func hygieneRules() []*Rule {
	return []*Rule{
		{
			ID: "plan-errored", Title: "The plan failed", Severity: model.Critical, Category: Correctness,
			Doc:   "Flags plans that errored. They are incomplete and cannot be applied.",
			Check: checkPlanErrored,
		},
		{
			ID: "plan-incomplete", Title: "The plan is incomplete", Severity: model.Medium, Category: Correctness,
			Doc:   "Flags plans with deferred changes, where another plan and apply is needed after this one.",
			Check: checkPlanIncomplete,
		},
		{
			ID: "check-failed", Title: "A check or condition failed", Severity: model.High, Category: Correctness,
			Doc:   "Flags check blocks, preconditions and postconditions that failed or errored during planning.",
			Check: checkChecks,
		},
		{
			ID: "output-removed", Title: "Removes an output", Severity: model.Low, Category: Hygiene,
			Doc:   "Flags removed root outputs. Anything reading them through terraform_remote_state, tfe_outputs or scripts breaks.",
			Check: checkOutputRemoved,
		},
		{
			ID: "provider-unpinned", Title: "Provider has no version constraint", Severity: model.Low, Category: Hygiene,
			Doc:   "Flags providers without a version constraint in required_providers, so a new major version can arrive on the next init.",
			Check: checkProviderUnpinned,
		},
		{
			ID: "module-unpinned", Title: "Module source is not pinned", Severity: model.Low, Category: Hygiene,
			Doc:   "Flags registry modules without a version constraint and Git modules without a ref, which change under you.",
			Check: checkModuleUnpinned,
		},
		{
			ID: "required-tags", Title: "Missing required tags", Severity: model.Medium, Category: Hygiene,
			Doc:   "When required_tags is set in .interragate.yaml, flags created or updated resources that will not carry every required tag.",
			Check: checkRequiredTags,
		},
	}
}

func checkPlanErrored(c *Context) []*model.Finding {
	if !c.Plan.Errored {
		return nil
	}
	return []*model.Finding{{
		Title:       "The plan failed",
		Short:       "is an incomplete, failed plan",
		Message:     "The plan errored, so it is incomplete and cannot be applied. What it shows is only what Terraform got through before the error.",
		Remediation: "Fix the error from the plan output and plan again.",
	}}
}

func checkPlanIncomplete(c *Context) []*model.Finding {
	if len(c.Plan.DeferredChanges) == 0 && (c.Plan.Complete == nil || *c.Plan.Complete) {
		return nil
	}
	msg := "The plan is incomplete: applying it does not converge, and another plan and apply will follow."
	if n := len(c.Plan.DeferredChanges); n > 0 {
		msg = fmt.Sprintf("%s were deferred until values they depend on are known. Apply this plan, then plan again to see the rest.", Plural(n, "change", "changes"))
	}
	return []*model.Finding{{Title: "The plan is incomplete", Message: msg, Short: "is an incomplete plan"}}
}

func checkChecks(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Report.Checks {
		if ch.Status != "fail" && ch.Status != "error" {
			continue
		}
		msg := fmt.Sprintf("%s %s.", Code(ch.Address), map[string]string{"fail": "failed", "error": "errored"}[ch.Status])
		if len(ch.Problems) > 0 {
			msg += " " + Plain(strings.Join(ch.Problems, " "))
		}
		out = append(out, &model.Finding{Title: "A check or condition failed", Short: "fails " + ch.Address, Message: msg, Addresses: []string{ch.Address}})
	}
	return out
}

func checkOutputRemoved(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, o := range c.Report.Outputs {
		if o.Action != model.Delete {
			continue
		}
		out = append(out, &model.Finding{
			Title:       "Removes an output",
			Short:       "removes output " + o.Name,
			Message:     fmt.Sprintf("Removes output %s. Anything that reads it, through terraform_remote_state, tfe_outputs or a script, breaks.", Code(o.Name)),
			Addresses:   []string{"output." + o.Name},
			Remediation: "Search other stacks and pipelines for the output name before removing it.",
		})
	}
	return out
}

func checkProviderUnpinned(c *Context) []*model.Finding {
	cfg := c.Plan.Configuration
	if cfg == nil {
		return nil
	}
	pinned := map[string]bool{}
	byName := map[string]*plan.ProviderConfig{}
	for _, pc := range cfg.ProviderConfig {
		if pc.ModuleAddress != "" || strings.HasPrefix(pc.FullName, "terraform.io/builtin/") {
			continue
		}
		if pc.VersionConstraint != "" {
			pinned[pc.FullName] = true
		}
		byName[pc.FullName] = pc
	}
	var names []string
	for full := range byName {
		if !pinned[full] {
			names = append(names, full)
		}
	}
	sort.Strings(names)
	var out []*model.Finding
	for _, full := range names {
		pc := byName[full]
		msg := fmt.Sprintf("Provider %s has no version constraint, so the next init can pick up a new major version with breaking changes.", Code(pc.Name))
		snippet := ""
		if c.Source != nil {
			if v := c.Source.Locked[full]; v != "" {
				parts := strings.Split(v, ".")
				constraint := v
				if len(parts) >= 2 {
					constraint = parts[0] + "." + parts[1]
				}
				msg += fmt.Sprintf(" The lock file has %s.", Code(v))
				snippet = fmt.Sprintf("%s = {\n  source  = %q\n  version = \"~> %s\"\n}", pc.Name, strings.TrimPrefix(full, "registry.terraform.io/"), constraint)
			}
		}
		out = append(out, &model.Finding{
			Title:       "Provider has no version constraint",
			Message:     msg,
			Addresses:   []string{"provider." + pc.Name},
			Snippet:     snippet,
			Remediation: "Add a version constraint for it in required_providers.",
		})
	}
	return out
}

var registrySource = regexp.MustCompile(`^([a-z0-9.-]+\.[a-z]+/)?[A-Za-z0-9_-]+/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+(//.*)?$`)

func checkModuleUnpinned(c *Context) []*model.Finding {
	cfg := c.Plan.Configuration
	if cfg == nil || cfg.RootModule == nil {
		return nil
	}
	var out []*model.Finding
	var walk func(prefix string, m *plan.ConfigModule)
	walk = func(prefix string, m *plan.ConfigModule) {
		for _, name := range diff.SortedKeys(m.ModuleCalls) {
			call := m.ModuleCalls[name]
			addr := "module." + name
			if prefix != "" {
				addr = prefix + "." + addr
			}
			src := call.Source
			local := strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../")
			switch {
			case local:
				if call.Module != nil {
					walk(addr, call.Module)
				}
			case registrySource.MatchString(src) && call.VersionConstraint == "":
				out = append(out, &model.Finding{
					Title:       "Module source is not pinned",
					Message:     fmt.Sprintf("%s uses registry module %s without a version, so every init can bring a different release.", Code(addr), Code(src)),
					Addresses:   []string{addr},
					Remediation: "Add version = \"~> X.Y\" to the module block.",
				})
			case (strings.HasPrefix(src, "git::") || strings.HasPrefix(src, "github.com/") || strings.HasPrefix(src, "bitbucket.org/")) && !strings.Contains(src, "ref="):
				out = append(out, &model.Finding{
					Title:       "Module source is not pinned",
					Message:     fmt.Sprintf("%s pulls %s from the default branch, so every init can bring different code.", Code(addr), Code(src)),
					Addresses:   []string{addr},
					Remediation: "Pin it with ?ref= and a tag or commit.",
				})
			}
		}
	}
	walk("", cfg.RootModule)
	return out
}

func checkRequiredTags(c *Context) []*model.Finding {
	if c.Config == nil || len(c.Config.RequiredTags) == 0 {
		return nil
	}
	missingBy := map[string][]string{}
	var order []string
	for _, ch := range c.Changes(model.Create, model.Update, model.Replace) {
		if !Managed(ch) {
			continue
		}
		after := c.After(ch)
		attr := ""
		for _, a := range []string{"tags_all", "labels", "tags"} {
			if _, ok := after[a]; ok || c.Unknown(ch, a) {
				attr = a
				break
			}
		}
		if attr == "" || c.Unknown(ch, attr) {
			continue
		}
		tags, _ := after[attr].(map[string]any)
		var missing []string
		for _, t := range c.Config.RequiredTags {
			if _, ok := tags[t]; !ok {
				missing = append(missing, t)
			}
		}
		if len(missing) == 0 {
			continue
		}
		key := strings.Join(missing, ", ")
		if missingBy[key] == nil {
			order = append(order, key)
		}
		missingBy[key] = append(missingBy[key], ch.Address)
	}
	var out []*model.Finding
	for _, key := range order {
		addrs := missingBy[key]
		codes := make([]string, len(addrs))
		for i, a := range addrs {
			codes[i] = Code(a)
		}
		out = append(out, &model.Finding{
			Title:       "Missing required tags",
			Message:     fmt.Sprintf("%s will not have the required %s: %s.", Plural(len(addrs), "resource", "resources"), key, List(codes, 5)),
			Addresses:   addrs,
			Remediation: "Add the tags to the resources, or to the provider's default_tags so every resource gets them.",
		})
	}
	return out
}
