package consumers

import (
	"slices"
	"sort"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/graph"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

// View is what assessing reads about each change beyond the report.
// rules.Context has it.
type View interface {
	// Raw is the plan's entry behind a change, with its values and the
	// masks that say which are unknown or sensitive.
	Raw(ch *model.Change) *plan.ResourceChange
	// DriftFor is what was changed outside Terraform at an address, or nil.
	DriftFor(address string) *model.Drift
}

// Input is what assessing reads.
type Input struct {
	Services []config.Consumer
	// File is the configuration file's name, for prose.
	File   string
	Report *model.Report
	Plan   *plan.Plan
	Graph  *graph.Graph
	View   View
}

// Assessment is what a plan does to the services outside Terraform that a
// configuration file declares.
type Assessment struct {
	in      Input
	matches []*Match
	impacts []Impact
	outages []*Outage
	// reports holds the finding made for each impact and outage, keyed by
	// the pointer to it, once one is made.
	reports map[any]*model.Finding
}

// Assess finds what each declared use names in the plan, and what the plan
// does to it. Outages wait for the apply's simulation: see Outages.
func Assess(in Input) *Assessment {
	a := &Assessment{in: in, reports: map[any]*model.Finding{}}
	a.matches = a.match()
	for _, m := range a.matches {
		a.impacts = append(a.impacts, a.impactsOf(m)...)
	}
	return a
}

// Matches are what each declared use names in the plan, in the order the
// file declares them.
func (a *Assessment) Matches() []*Match { return a.matches }

// Impacts are what the plan does to the uses, in the order declared.
func (a *Assessment) Impacts() []Impact { return a.impacts }

// Findings reports the impacts of one kind: a finding for each thing the
// plan does, naming every service it affects. It remembers which finding
// reports each impact, for Services.
func (a *Assessment) Findings(kind model.EffectKind) []*model.Finding {
	var order []string
	groups := map[string][]Impact{}
	for _, im := range a.impacts {
		if im.Kind() != kind {
			continue
		}
		k := im.key()
		if groups[k] == nil {
			order = append(order, k)
		}
		groups[k] = append(groups[k], im)
	}
	out := make([]*model.Finding, 0, len(order))
	for _, k := range order {
		group := groups[k]
		f := findingFor(group, a.in.File, a.in.Report.Plan.ToolName())
		for _, im := range group {
			a.reports[im] = f
		}
		out = append(out, f)
	}
	return out
}

// OutageFindings works out the outages the simulated apply causes and
// reports them, a finding each. It remembers which finding reports each
// outage, for Services and Reported.
func (a *Assessment) OutageFindings(t Timeline) []*model.Finding {
	a.outages = a.Outages(t)
	out := make([]*model.Finding, 0, len(a.outages))
	for _, o := range a.outages {
		f := o.Finding(a.in.File, a.in.Report.Plan.ToolName())
		a.reports[o] = f
		out = append(out, f)
	}
	return out
}

// Reported lists the outages that a shown finding reports, for the apply's
// summary.
func (a *Assessment) Reported(shown func(*model.Finding) bool) []*Outage {
	var out []*Outage
	for _, o := range a.outages {
		if f := a.reports[o]; f != nil && shown(f) {
			out = append(out, o)
		}
	}
	return out
}

// Services lists every declared service with what the plan does to it, for
// the report. Only shown findings count: what disable or ignore leaves out
// is counted as hidden. override is the configuration's severity for a
// rule, if it sets one.
func (a *Assessment) Services(shown func(*model.Finding) bool, override func(rule string) (model.Severity, bool)) []*model.Consumer {
	tool := a.in.Report.Plan.ToolName()
	out := make([]*model.Consumer, 0, len(a.in.Services))
	bySvc := make(map[*config.Consumer]*model.Consumer, len(a.in.Services))
	for i := range a.in.Services {
		svc := &a.in.Services[i]
		c := &model.Consumer{Name: svc.Name, Platform: PlatformName(svc.Platform), Owners: svc.Owners, Notify: svc.Notify, Link: svc.Link, Effects: []model.ConsumerEffect{}}
		bySvc[svc] = c
		out = append(out, c)
	}
	for _, m := range a.matches {
		c := bySvc[m.Service]
		if m.Found() {
			c.Checked = true
		} else {
			c.Missing = append(c.Missing, m.Missing())
		}
	}
	severity := func(f *model.Finding, level model.Severity) model.Severity {
		if s, ok := override(f.Rule); ok {
			return s
		}
		return level
	}
	for _, im := range a.impacts {
		c := bySvc[im.Of().Service]
		f := a.reports[im]
		if f == nil || !shown(f) {
			c.Hidden++
			continue
		}
		c.Effects = append(c.Effects, model.ConsumerEffect{Kind: im.Kind(), Severity: severity(f, levelFor(im)), Text: im.ServiceSide(tool),
			Update: im.Update(), Finding: f.ID, Refs: []string{im.Subject()}})
	}
	for _, o := range a.outages {
		f := a.reports[o]
		for _, m := range o.Uses {
			c := bySvc[m.Service]
			if f == nil || !shown(f) {
				c.Hidden++
				continue
			}
			c.Effects = append(c.Effects, model.ConsumerEffect{Kind: model.EffectOutage, Severity: severity(f, levelOf(m.Service)), Text: o.ServiceSide(m),
				Finding: f.ID, Refs: o.refs()})
		}
	}
	for _, c := range out {
		sort.SliceStable(c.Effects, func(i, j int) bool { return c.Effects[i].Severity > c.Effects[j].Severity })
		c.Missing = slices.Compact(c.Missing)
	}
	return out
}
