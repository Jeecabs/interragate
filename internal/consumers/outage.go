package consumers

import (
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/model"
)

// Timeline is the apply as interragate simulates it, and where it is
// expected to stop.
type Timeline struct {
	Apply *model.Apply
	// StopsAt are the addresses whose steps are expected to fail.
	StopsAt []string
	// Halted holds the IDs of the steps that do not run when the apply
	// stops there: those expected to fail and those that wait for them.
	Halted map[string]bool
}

// Window is a stretch of the apply, in seconds from its start. An open
// window does not end: nothing in the apply brings back what went.
type Window struct {
	From, To int
	Open     bool
}

func (w Window) length() int {
	if w.Open {
		return math.MaxInt - w.From
	}
	return w.To - w.From
}

// overlap is the stretch two windows share, if they share one.
func (w Window) overlap(v Window) (Window, bool) {
	out := Window{From: max(w.From, v.From), Open: w.Open && v.Open}
	switch {
	case out.Open:
	case w.Open:
		out.To = v.To
	case v.Open:
		out.To = w.To
	default:
		out.To = min(w.To, v.To)
	}
	return out, out.Open || out.To > out.From
}

// Outage is a stretch of the apply in which something services outside
// Terraform rely on is missing.
type Outage struct {
	Uses []*Match
	// Subject is what the services see go: the host they call, or the
	// resource or output they use.
	Subject string
	// Culprit is the change that takes it away.
	Culprit string
	// NoTarget is set when the load balancer behind a host is left with no
	// target, rather than something itself going missing.
	NoTarget bool
	Window   Window
	// Timed is set when the apply's durations are known, so the outage's
	// length is worth giving.
	Timed bool
	// OnceFixed are the addresses where the apply is expected to stop
	// first: the outage happens only once that is fixed.
	OnceFixed []string
	// Stuck are the addresses where the apply is expected to stop, which
	// leaves the outage without an end until that is fixed.
	Stuck []string
}

// Outages finds, for every declared use, the longest stretch of the apply in
// which what it relies on is missing. Uses of the same thing that lose it
// the same way share one outage. A use the plan takes away for good has a
// Loss, not an outage too.
func (a *Assessment) Outages(t Timeline) []*Outage {
	if t.Apply == nil {
		return nil
	}
	timed := slices.ContainsFunc(t.Apply.Steps, func(st *model.ApplyStep) bool { return st.Known })
	lost := map[*Match]bool{}
	for _, im := range a.impacts {
		if im.Kind() == model.EffectLoses {
			lost[im.Of()] = true
		}
	}
	var out []*Outage
	byKey := map[string]*Outage{}
	for _, m := range a.matches {
		if !m.Found() || lost[m] {
			continue
		}
		o := a.longestOutage(m, t)
		if o == nil {
			continue
		}
		key := o.Subject + "|" + o.Culprit + "|" + strconv.FormatBool(o.NoTarget)
		if same := byKey[key]; same != nil {
			same.Uses = append(same.Uses, m)
			continue
		}
		o.Uses, o.Timed = []*Match{m}, timed
		byKey[key] = o
		out = append(out, o)
	}
	return out
}

// longestOutage finds the longest stretch in which what a use relies on is
// missing. Ties go to the culprit whose address sorts first, so a plan
// always gives the same outage.
func (a *Assessment) longestOutage(m *Match, t Timeline) *Outage {
	subject := subjectOf(m)
	p := a.pathOf(m)
	var best *Outage
	consider := func(o *Outage) {
		if o == nil {
			return
		}
		switch {
		case best == nil:
		case o.Window.length() != best.Window.length():
			if o.Window.length() < best.Window.length() {
				return
			}
		case o.Culprit >= best.Culprit:
			return
		}
		best = o
	}
	for _, ch := range p.front {
		if ch.Action == model.Delete && slices.Contains(m.Changes, ch) {
			continue
		}
		g, ok := t.missing(ch.Address)
		if !ok {
			continue
		}
		o := &Outage{Subject: subject, Culprit: ch.Address, Window: g.window}
		switch {
		case g.halted:
			o.OnceFixed = t.StopsAt
		case g.stuck:
			o.Stuck = t.StopsAt
		}
		consider(o)
	}
	consider(emptied(p.groups, t, subject))
	return best
}

// missing says when an address is missing while the apply runs in full,
// and how the expected failure changes that.
type missing struct {
	window Window
	halted bool // its destroy does not run if the apply stops as expected
	stuck  bool // its destroy runs then, but its replacement does not
}

func (t Timeline) missing(address string) (missing, bool) {
	var destroy, create *model.ApplyStep
	for _, st := range t.Apply.Steps {
		switch {
		case st.Address != address:
		case st.Op == "destroy" && !st.Deposed:
			destroy = st
		case st.Op == "create":
			create = st
		}
	}
	if destroy == nil {
		return missing{}, false
	}
	m := missing{window: Window{From: destroy.Start, Open: create == nil}, halted: t.Halted[destroy.ID]}
	if create != nil {
		m.window.To = create.Start + create.Duration
		m.stuck = !m.halted && t.Halted[create.ID]
	}
	return m, true
}

// emptied finds when every target group behind the load balancers on a path
// is empty at once, so they serve nothing though they still exist. A group
// whose targets Terraform does not register, or one member of which stays,
// keeps serving.
func emptied(groups []targetGroup, t Timeline, subject string) *Outage {
	if len(groups) == 0 {
		return nil
	}
	all := Window{Open: true}
	culprit := ""
	halted, stuck := false, true
	for _, g := range groups {
		if len(g.members) == 0 {
			return nil
		}
		for _, ch := range g.members {
			gone, ok := t.missing(ch.Address)
			if !ok {
				return nil
			}
			w, overlaps := all.overlap(gone.window)
			if !overlaps {
				return nil
			}
			all = w
			if culprit == "" {
				culprit = ch.Address
			}
			halted = halted || gone.halted
			stuck = stuck && gone.stuck
		}
	}
	o := &Outage{Subject: subject, Culprit: culprit, NoTarget: true, Window: all}
	switch {
	case halted:
		o.OnceFixed = t.StopsAt
	case stuck:
		o.Stuck = t.StopsAt
	}
	return o
}

// subjectOf says what a use's services see go: the host they call, the
// output they read, or what they use at an address.
func subjectOf(m *Match) string {
	switch t := m.Use.Target.(type) {
	case config.HostTarget:
		return string(t.Host)
	case config.OutputTarget:
		return t.String()
	case config.ResourceTarget:
		return string(t.Address)
	case config.ReachTarget:
		return string(t.Firewall)
	}
	panic(fmt.Sprintf("consumers: a use names a target of unknown type %T", m.Use.Target))
}

// refs are the addresses an outage is about: its culprit, then where the
// apply is expected to stop.
func (o *Outage) refs() []string {
	out := []string{o.Culprit}
	for _, ref := range slices.Concat(o.Stuck, o.OnceFixed) {
		if !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	return out
}
