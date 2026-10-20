package analysis

import (
	"container/heap"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/graph"
	"github.com/Jeecabs/interragate/internal/kb"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
	"github.com/Jeecabs/interragate/internal/rules"
)

// parallelism is how many operations Terraform and OpenTofu run at once
// unless -parallelism says otherwise.
const parallelism = 10

// reduceMemory bounds the memory, in bytes, that working out which waits
// are direct takes. That grows with the square of the number of steps, so
// past this it is done in several passes rather than one.
var reduceMemory = 64 << 20

// Step operations, as the report names them.
const (
	opCreate  = "create"
	opUpdate  = "update"
	opDestroy = "destroy"
	opRead    = "read"
)

// Steps are grouped by kind: their operation, except that late destroys,
// which wait for their replacement, are kept apart from the others.
const kindLate = "late"

var (
	creating   = []string{opCreate, opUpdate, opRead}
	destroying = []string{opDestroy, kindLate}
)

// anyModule stands for every instance of a module, where every takes one.
const anyModule = "*"

// applyInput is what the simulation needs from a plan.
type applyInput struct {
	changes []*model.Change
	graph   *graph.Graph
	// stored maps a change's address to the resources its object depended
	// on, as recorded in state. Objects missing from it are new.
	stored map[string][]string
	// cbd holds resources whose configuration sets create_before_destroy,
	// which holds back even a plain destroy of one of their instances.
	cbd map[string]bool
	// refs adds references the plan's configuration leaves out, such as
	// replace_triggered_by, by resource.
	refs map[string][]string
	// provided lists, for resources gone from the configuration, what the
	// configuration of the provider that destroys them reads.
	provided map[string][]string
	destroy  bool
}

// simStep is a step, or a join that stands for several steps of one kind
// on a resource's instances. An instance waits for every instance of a
// resource it refers to; Terraform's graph joins those instances in one
// node rather than linking every pair, and so does this, so the waits grow
// with the number of instances rather than with their product.
type simStep struct {
	*model.ApplyStep
	ch   *model.Change    // nil for a join
	join *model.ApplyJoin // set for a join
	// late destroys wait for their replacement and for what is related to
	// the object, as create_before_destroy has it; other destroys go first.
	late    bool
	index   int
	pred    []wait // sorted by index once connect is done
	succ    []*simStep
	waiting int
	rank    int // its duration plus the longest chain of steps waiting on it
	lead    int // its duration plus the longest chain of steps it waits on
	end     int
	seq     int
}

// wait is something a step waits for.
type wait struct {
	on *simStep
	// weak waits put the destroy of what depends on an object before the
	// object's own steps. Terraform drops one of those rather than close a
	// cycle, as a provider configured from a resource being replaced can.
	weak bool
	// redundant marks a wait that others imply, while reduce works.
	redundant bool
}

func (st *simStep) waitFor(p *simStep, weak bool) {
	if p != nil && p != st {
		st.pred = append(st.pred, wait{on: p, weak: weak})
	}
}

func (st *simStep) drop(p *simStep) {
	st.pred = slices.DeleteFunc(st.pred, func(w wait) bool { return w.on == p })
}

// group is every step of one kind on a resource's instances in one module
// instance or, with parts, in all of them.
type group struct {
	res, kind string
	module    string     // the module's configuration path
	steps     []*simStep // sorted by ID within each module instance
	parts     []*group   // each module instance's, when there are several
	all       *group     // for a module instance's, every instance's; itself when there is one
	node      *simStep   // what stands for the steps, once something waits for them
}

// class is the steps of a group that record the same dependencies, which
// the rules then treat alike.
type class struct {
	res, module, kind string // module is the module instance
	deps              []string
	recorded          bool // deps are what state recorded, not worked out from the configuration
	steps             []*simStep
	group             *group
	node              *simStep
}

// dependents are the classes that record a dependency on one resource:
// those in other modules, and those in each instance of the resource's own
// module, which only steps in that instance are ordered against.
type dependents struct {
	outside []*class
	inside  map[string]*[]*class
}

type groupKey struct{ res, module, kind string }

type applySim struct {
	in        applyInput
	steps     []*simStep // sorted by ID
	joins     []*simStep // in the order they are made
	nodes     []*simStep // the steps, then the joins
	byID      map[string]*simStep
	creators  map[string][]*simStep // create, update and read steps by resource
	destroyed map[string]bool       // resources with destroy steps
	instances map[string][]*model.Change
	upstream  map[string][]string // everything upstream of a resource
	gone      map[string][]string // what is destroyed upstream of a resource
	nearest   map[string][]string
	provided  map[string][]string
	// connect works on groups and classes of steps.
	groups  map[groupKey]*group
	all     map[groupKey]*group // across module instances, keyed without one
	classes []*class
	rdeps   map[string]*dependents
	outside map[string][]*simStep // what stands for dependents outside a resource's module, by resource and kinds
	modules map[string]string     // configuration module paths of resources
	joinIDs map[string]bool
}

// apply simulates the apply, or returns nil when there is nothing to apply.
func (a *analyzer) apply(r *model.Report) *model.Apply {
	if r.Plan.Errored {
		return nil
	}
	in := applyInput{changes: r.Changes, graph: a.graph, stored: storedDeps(a.plan.PriorState),
		cbd: map[string]bool{}, refs: map[string][]string{}, provided: map[string][]string{}, destroy: r.Plan.Destroy}
	seen := map[string]bool{}
	for _, ch := range r.Changes {
		if _, ok := in.provided[ch.Resource]; !ok && a.graph.Nodes[ch.Resource] == nil {
			in.provided[ch.Resource] = a.graph.Providers[a.providerKey(ch)]
		}
		b := a.block(ch)
		if b == nil || seen[ch.Resource] {
			continue
		}
		seen[ch.Resource] = true
		if cbd := b.Lifecycle.CreateBeforeDestroy; cbd != nil && *cbd {
			in.cbd[ch.Resource] = true
		}
		// Terraform orders a resource after what its replace_triggered_by
		// names, which can only be a managed resource in the same module.
		for _, t := range b.Lifecycle.ReplaceTriggeredBy {
			if parts := graph.Traversal(t); len(parts) >= 2 {
				id := parts[0] + "." + parts[1]
				if m := moduleConfig(ch.Module); m != "" {
					id = m + "." + id
				}
				if id != ch.Resource && a.graph.Nodes[id] != nil {
					in.refs[ch.Resource] = append(in.refs[ch.Resource], id)
				}
			}
		}
	}
	ap := simulateApply(in)
	if ap != nil {
		markFailures(ap, r.Findings)
		ap.Narrative = applyNarrative(ap, r)
	}
	return ap
}

// providerKey finds the configuration of the provider that destroys a
// resource gone from the configuration. State records it, but the plan
// names only its source, so this takes that provider's default
// configuration in the resource's module, or the nearest module above that
// has one, as a module without its own inherits its parent's.
func (a *analyzer) providerKey(ch *model.Change) string {
	rc := a.raw[ch]
	if rc == nil || a.plan.Configuration == nil {
		return ""
	}
	configs := a.plan.Configuration.ProviderConfig
	keys := diff.SortedKeys(configs)
	for m := moduleConfig(ch.Module); ; m = parentModule(m) {
		for _, key := range keys {
			if pc := configs[key]; pc != nil && pc.FullName == rc.ProviderName && pc.Alias == "" && pc.ModuleAddress == m {
				return key
			}
		}
		if m == "" {
			return ""
		}
	}
}

// parentModule drops the last call from a module path.
func parentModule(m string) string {
	if i := strings.LastIndex(m, ".module."); i >= 0 {
		return m[:i]
	}
	return ""
}

// markFailures marks the steps that findings expect to fail.
func markFailures(ap *model.Apply, findings []*model.Finding) {
	byID := map[string]*model.ApplyStep{}
	for _, st := range ap.Steps {
		byID[st.ID] = st
	}
	for _, f := range findings {
		if f.Fails == "" || len(f.Addresses) == 0 {
			continue
		}
		if st := byID[f.Addresses[0]+":"+f.Fails]; st != nil && st.Fails == "" {
			st.Fails = f.ID
		}
	}
}

// storedDeps reads the dependencies state records for each object.
// Terraform orders destroys by these rather than by the configuration, which
// may no longer mention the object; it merges the configuration's into them
// when it refreshes.
func storedDeps(st *plan.State) map[string][]string {
	out := map[string][]string{}
	if st == nil || st.Values == nil {
		return out
	}
	var walk func(m *plan.StateModule)
	walk = func(m *plan.StateModule) {
		if m == nil {
			return
		}
		for _, r := range m.Resources {
			if r == nil {
				continue
			}
			addr := r.Address
			if r.DeposedKey != "" {
				addr += deposedSuffix(r.DeposedKey)
			}
			out[addr] = r.DependsOn
		}
		for _, c := range m.ChildModules {
			walk(c)
		}
	}
	walk(st.Values.RootModule)
	return out
}

// simulateApply works out the order Terraform applies the changes in and
// roughly when each step runs, following the rules of its apply graph:
//
//   - A create, update or read waits for the creates, updates and reads of
//     what it refers to in the configuration, and of every resource in a
//     module it refers to as a whole rather than through an output.
//   - Every step, destroys included, waits for the creates, updates and
//     reads of what its provider's configuration refers to: the provider is
//     configured before anything uses it.
//   - Destroys follow the dependencies recorded in state: what depends on an
//     object is destroyed before it, and a create, update or read waits for
//     the destroys of objects it depends on or that depend on it.
//   - A late destroy, of an object replaced with create_before_destroy or of
//     a deposed object, turns the last kind of wait around: it waits for its
//     replacement and for everything related that is created or updated.
//     So does the destroy of an object that such an object depends on (see
//     forceLate).
//
// Then it runs the steps, at most parallelism at a time.
func simulateApply(in applyInput) *model.Apply {
	if in.graph == nil {
		in.graph = &graph.Graph{Nodes: map[string]*graph.Node{}}
	}
	s := &applySim{in: in, byID: map[string]*simStep{}, creators: map[string][]*simStep{}, destroyed: map[string]bool{},
		instances: map[string][]*model.Change{}, upstream: map[string][]string{}, gone: map[string][]string{}, nearest: map[string][]string{},
		provided: map[string][]string{}, outside: map[string][]*simStep{}, modules: map[string]string{}, joinIDs: map[string]bool{}}
	for _, ch := range in.changes {
		s.instances[ch.Resource] = append(s.instances[ch.Resource], ch)
	}
	s.addSteps()
	if len(s.steps) == 0 {
		return nil
	}
	s.forceLate()
	s.connect()
	order := s.sorted()
	s.reduce(order)
	s.schedule(order)

	ap := &model.Apply{Parallelism: parallelism}
	byStart := slices.Clone(s.steps)
	sort.Slice(byStart, func(i, j int) bool { return byStart[i].seq < byStart[j].seq })
	for _, st := range byStart {
		st.After = waitIDs(st)
		ap.Steps = append(ap.Steps, st.ApplyStep)
		ap.Duration = max(ap.Duration, st.end)
	}
	joins := slices.Clone(s.joins)
	sort.Slice(joins, func(i, j int) bool { return joins[i].ID < joins[j].ID })
	for _, j := range joins {
		j.join.After = waitIDs(j)
		ap.Joins = append(ap.Joins, j.join)
	}
	ap.Critical = s.critical()
	ap.Gaps = s.gaps()
	return ap
}

// waitIDs lists the IDs of what a step or join waits for, sorted.
func waitIDs(st *simStep) []string {
	var ids []string
	for _, w := range st.pred {
		ids = append(ids, w.on.ID)
	}
	sort.Strings(ids)
	return ids
}

// addSteps turns each change into the steps Terraform takes for it.
func (s *applySim) addSteps() {
	for _, ch := range s.in.changes {
		if s.in.destroy && ch.Action != model.Delete {
			continue
		}
		switch ch.Action {
		case model.Create:
			s.add(ch, opCreate)
		case model.Update:
			s.add(ch, opUpdate)
		case model.Read:
			s.add(ch, opRead)
		case model.Delete:
			// A deposed object is what an interrupted create_before_destroy
			// replacement left behind, so it is destroyed late like one.
			d := s.add(ch, opDestroy)
			d.Deposed = ch.Deposed != ""
			d.late = d.Deposed || s.in.cbd[ch.Resource]
		case model.Replace:
			switch ch.Order {
			case "create-first":
				s.add(ch, opCreate)
				d := s.add(ch, opDestroy)
				d.Deposed, d.late = true, true
			case "forget":
				// The old object is left running, unmanaged.
				s.add(ch, opCreate)
			default:
				s.add(ch, opDestroy)
				s.add(ch, opCreate)
			}
		}
		// No-ops, moves, imports without changes and forgets leave the
		// infrastructure alone: Terraform only records them in state.
	}
	sort.Slice(s.steps, func(i, j int) bool { return s.steps[i].ID < s.steps[j].ID })
	for i, st := range s.steps {
		st.index = i
	}
}

func (s *applySim) add(ch *model.Change, op string) *simStep {
	secs, known := kb.Duration(ch.Type, op)
	if op == opUpdate && ch.Cosmetic != "" {
		// Tags and formatting are quick to change on any type; the slow
		// figures are for changes a service has to roll out.
		secs, known = kb.DefaultDuration, false
	}
	st := &simStep{ch: ch,
		ApplyStep: &model.ApplyStep{ID: ch.Address + ":" + op, Address: ch.Address, Op: op, Duration: secs, Known: known}}
	s.steps = append(s.steps, st)
	s.byID[st.ID] = st
	if op != opDestroy {
		s.creators[ch.Resource] = append(s.creators[ch.Resource], st)
	} else {
		s.destroyed[ch.Resource] = true
	}
	return st
}

// forceLate makes late the plain destroys Terraform holds back without
// create_before_destroy in their own configuration. Terraform forces
// create_before_destroy onto everything a resource with it depends on and
// records that in state, which is where the destroy of an object removed
// from the configuration finds it. Were it not so, an object replaced
// create-first that depends on a removed one would wait for the removed
// one's destroy, which waits for the replaced object's old copy to go,
// which waits for the new one: a cycle, which Terraform reports as an
// error. So a destroy is late when its resource has the setting, as an
// instance replaced create-first or a deposed object shows, and when it
// removes something that an existing object with the setting depends on,
// or that a destroy held back that way depends on. A new object does not
// count, as the setting was recorded before it existed.
func (s *applySim) forceLate() {
	if s.in.destroy {
		// Nothing is created, so nothing could wait the other way round.
		return
	}
	plain := map[string][]*simStep{} // by resource
	for _, st := range s.steps {
		if st.Op == opDestroy && !st.late && st.ch.Action == model.Delete {
			plain[st.ch.Resource] = append(plain[st.ch.Resource], st)
		}
	}
	if len(plain) == 0 {
		return
	}
	cbd := map[string]bool{}
	for _, ch := range s.in.changes {
		cbd[ch.Resource] = cbd[ch.Resource] || s.in.cbd[ch.Resource] || ch.Deposed != "" ||
			ch.Action == model.Replace && ch.Order == "create-first"
	}
	var queue []*model.Change
	for _, ch := range s.in.changes {
		switch ch.Action {
		case model.Update, model.Replace, model.Delete, model.NoOp:
			if cbd[ch.Resource] && !ch.Importing {
				queue = append(queue, ch)
			}
		}
	}
	// Every instance of a resource has its setting; the queue has these
	// destroys already.
	for res, ds := range plain {
		if cbd[res] {
			for _, d := range ds {
				d.late = true
			}
			delete(plain, res)
		}
	}
	for len(queue) > 0 {
		ch := queue[0]
		queue = queue[1:]
		deps, _ := s.deps(ch, false)
		for _, res := range deps {
			for _, d := range plain[res] {
				d.late = true
				queue = append(queue, d.ch)
			}
			delete(plain, res)
		}
	}
}

// connect adds the waits between steps; see simulateApply. The rules apply
// to classes of steps rather than to one step at a time, and where a step
// waits for every step of a group, it waits for one step or join standing
// for them.
func (s *applySim) connect() {
	s.classify()
	for _, c := range s.classes {
		var on []wait
		add := func(weak bool, ns ...*simStep) {
			for _, n := range ns {
				on = append(on, wait{on: n, weak: weak})
			}
		}
		if c.kind == opDestroy || c.kind == kindLate {
			for _, up := range s.providedBy(c.res) {
				add(false, s.every(up, creating, anyModule)...)
			}
			// What depends on an object is destroyed before it, so nothing
			// still uses it when it goes.
			add(true, s.dependents(c, destroying)...)
			if c.kind == kindLate {
				// A late destroy waits instead for what is related to be
				// created or updated: what it depends on, and what depends
				// on it.
				for _, res := range c.deps {
					if res != c.res {
						add(true, s.every(res, creating, c.module)...)
					}
				}
				add(false, s.dependents(c, creating)...)
			}
		} else {
			for _, up := range s.nearestCreators(c.res) {
				add(false, s.every(up, creating, anyModule)...)
			}
			// A change must not land on an object that is about to go.
			for _, res := range c.deps {
				if res != c.res {
					add(false, s.every(res, []string{opDestroy}, c.module)...)
				}
			}
			// And an object is changed only once what used it is gone: the
			// provider may refuse to change something still in use.
			add(true, s.dependents(c, []string{opDestroy})...)
		}
		for _, st := range c.steps {
			st.pred = append(st.pred, on...)
		}
	}
	// The two halves of a replacement, or a deposed object and the current
	// one.
	byInstance := map[string][]*simStep{}
	for _, st := range s.steps {
		inst := instanceAddress(st.ch)
		byInstance[inst] = append(byInstance[inst], st)
	}
	for _, st := range s.steps {
		if st.Op != opDestroy {
			continue
		}
		for _, c := range byInstance[instanceAddress(st.ch)] {
			if c.Op != opDestroy {
				sequence(c, st)
			}
		}
	}
	s.nodes = append(slices.Clone(s.steps), s.joins...)
	for _, st := range s.nodes {
		st.pred = dedupeWaits(st.pred)
	}
}

// sequence puts a create, update or read before or after a related destroy
// the way Terraform does: the destroy goes first unless it is late.
func sequence(c, d *simStep) {
	if d.late {
		d.waitFor(c, false)
	} else {
		c.waitFor(d, false)
	}
}

// dedupeWaits sorts waits by what they wait for and merges repeats. A wait
// is weak only if every rule that made it says so.
func dedupeWaits(ws []wait) []wait {
	sort.Slice(ws, func(i, j int) bool { return ws[i].on.index < ws[j].on.index })
	out := ws[:0]
	for _, w := range ws {
		if n := len(out); n > 0 && out[n-1].on == w.on {
			out[n-1].weak = out[n-1].weak && w.weak
			continue
		}
		out = append(out, w)
	}
	return out
}

// classify sorts the steps into groups, by resource, kind and module
// instance, and the groups into classes by the dependencies their steps
// record. It also indexes the classes by the resources they depend on.
func (s *applySim) classify() {
	s.groups, s.all = map[groupKey]*group{}, map[groupKey]*group{}
	var groups []*group
	classes := map[*group][]*class{}
	for _, st := range s.steps {
		kind := st.Op
		if st.late {
			kind = kindLate
		}
		key := groupKey{st.ch.Resource, st.ch.Module, kind}
		g := s.groups[key]
		if g == nil {
			g = &group{res: key.res, kind: kind, module: moduleConfig(key.module)}
			s.groups[key] = g
			groups = append(groups, g)
		}
		g.steps = append(g.steps, st)
		deps, recorded := s.deps(st.ch, slices.Contains(destroying, kind))
		i := slices.IndexFunc(classes[g], func(c *class) bool { return s.sameDeps(c, deps, recorded) })
		if i < 0 {
			i = len(classes[g])
			c := &class{res: key.res, module: key.module, kind: kind, deps: deps, recorded: recorded, group: g}
			classes[g] = append(classes[g], c)
			s.classes = append(s.classes, c)
		}
		classes[g][i].steps = append(classes[g][i].steps, st)
	}
	for _, g := range groups {
		key := groupKey{res: g.res, kind: g.kind}
		switch all := s.all[key]; {
		case all == nil:
			s.all[key] = g
		case all.parts == nil:
			s.all[key] = &group{res: g.res, kind: g.kind, module: g.module,
				steps: slices.Concat(all.steps, g.steps), parts: []*group{all, g}}
		default:
			all.steps = append(all.steps, g.steps...)
			all.parts = append(all.parts, g)
		}
	}
	for _, g := range groups {
		g.all = s.all[groupKey{res: g.res, kind: g.kind}]
	}
	s.rdeps = map[string]*dependents{}
	for _, c := range s.classes {
		own := moduleConfig(c.module)
		for _, res := range c.deps {
			if res == c.res {
				continue
			}
			d := s.rdeps[res]
			if d == nil {
				d = &dependents{inside: map[string]*[]*class{}}
				s.rdeps[res] = d
			}
			list := &d.outside
			if own == s.moduleOf(res) {
				if list = d.inside[c.module]; list == nil {
					list = &[]*class{}
					d.inside[c.module] = list
				}
			}
			// A repeated dependency would land next to the first.
			if n := len(*list); n == 0 || (*list)[n-1] != c {
				*list = append(*list, c)
			}
		}
	}
}

// moduleOf returns the configuration path of the module a resource is in.
func (s *applySim) moduleOf(res string) string {
	if !strings.HasPrefix(res, "module.") {
		return ""
	}
	m, ok := s.modules[res]
	if !ok {
		if a, err := plan.ParseAddress(res); err == nil {
			m = a.ModuleConfigString()
		}
		s.modules[res] = m
	}
	return m
}

// every returns what to wait for to wait for every step of the given kinds
// on res's instances that a step in module instance mod is ordered against:
// one step or join for each kind. State records dependencies per resource
// rather than per module instance, so Terraform does not order destroys by
// them across instances of one module, where they would tie unrelated
// objects together; anyModule takes every instance.
func (s *applySim) every(res string, kinds []string, mod string) []*simStep {
	var out []*simStep
	for _, kind := range kinds {
		all := s.all[groupKey{res: res, kind: kind}]
		switch {
		case all == nil:
		case mod != anyModule && moduleConfig(mod) == all.module:
			if g := s.groups[groupKey{res, mod, kind}]; g != nil {
				out = append(out, s.node(g))
			}
		default:
			out = append(out, s.node(all))
		}
	}
	return out
}

// dependents returns what to wait for to wait for the steps of the given
// kinds that record a dependency on c's resource and that c's steps are
// ordered against.
func (s *applySim) dependents(c *class, kinds []string) []*simStep {
	d := s.rdeps[c.res]
	if d == nil {
		return nil
	}
	of := func(cs []*class) []*class {
		var out []*class
		for _, c := range cs {
			if slices.Contains(kinds, c.kind) {
				out = append(out, c)
			}
		}
		return out
	}
	key := c.res + " " + strings.Join(kinds, ",")
	out, ok := s.outside[key]
	if !ok {
		out = s.cover(of(d.outside), true)
		s.outside[key] = out
	}
	if in := d.inside[c.module]; in != nil {
		return append(slices.Clone(out), s.cover(of(*in), false)...)
	}
	return out
}

// cover returns what to wait for to wait for every step of some classes: a
// group's step or join where they take in the whole group (with across,
// one for every module instance's where they take in all of those), and
// each class's otherwise.
func (s *applySim) cover(cs []*class, across bool) []*simStep {
	// Most often a class is its group's every step, in one module instance.
	var out []*simStep
	var rest []*class
	for _, c := range cs {
		if g := c.group; len(c.steps) == len(g.steps) && (!across || g.all == g) {
			out = append(out, s.node(g))
		} else {
			rest = append(rest, c)
		}
	}
	if len(rest) == 0 {
		return out
	}
	have := map[*group]int{}
	for _, c := range rest {
		have[c.group] += len(c.steps)
	}
	whole := map[*group]bool{}
	wholeAll := func(all *group) bool {
		if v, ok := whole[all]; ok {
			return v
		}
		v := true
		for _, p := range all.parts {
			v = v && have[p] == len(p.steps)
		}
		whole[all] = v
		return v
	}
	taken := map[*group]bool{}
	for _, c := range rest {
		g := c.group
		if taken[g] {
			continue
		}
		if across && g.all != g && wholeAll(g.all) {
			out = append(out, s.node(g.all))
			for _, p := range g.all.parts {
				taken[p] = true
			}
			continue
		}
		if have[g] == len(g.steps) {
			out = append(out, s.node(g))
			taken[g] = true
			continue
		}
		if c.node == nil {
			c.node = s.stand(c.res, c.kind, c.steps, nil)
		}
		out = append(out, c.node)
	}
	return out
}

// node returns what stands for every step of a group.
func (s *applySim) node(g *group) *simStep {
	if g.node == nil {
		var parts []*simStep
		for _, p := range g.parts {
			parts = append(parts, s.node(p))
		}
		g.node = s.stand(g.res, g.kind, g.steps, parts)
	}
	return g.node
}

// stand returns the one step, or else a new join waiting for the steps (or
// for parts standing for them).
func (s *applySim) stand(res, kind string, steps, parts []*simStep) *simStep {
	if len(steps) == 1 {
		return steps[0]
	}
	op := kind
	if kind == kindLate {
		op = opDestroy
	}
	base := pattern(steps) + ":" + op
	id := base
	for n := 2; s.joinIDs[id] || s.byID[id] != nil; n++ {
		id = fmt.Sprintf("%s#%d", base, n)
	}
	s.joinIDs[id] = true
	j := &simStep{ApplyStep: &model.ApplyStep{ID: id, Op: op}, join: &model.ApplyJoin{ID: id, Resource: res, Op: op},
		index: len(s.steps) + len(s.joins)}
	if parts == nil {
		parts = steps
	}
	for _, p := range parts {
		j.waitFor(p, false)
	}
	s.joins = append(s.joins, j)
	return j
}

// pattern is the address several steps share, with [*] where theirs
// differ: aws_subnet.b[*], or module.app[*].aws_instance.web.
func pattern(steps []*simStep) string {
	first, err := plan.ParseAddress(instanceAddress(steps[0].ch))
	if err != nil {
		return steps[0].ch.Resource + "[*]"
	}
	differ := false
	for _, st := range steps[1:] {
		a, err := plan.ParseAddress(instanceAddress(st.ch))
		if err != nil || len(a.Module) != len(first.Module) {
			return steps[0].ch.Resource + "[*]"
		}
		for i, m := range a.Module {
			if m.Key != first.Module[i].Key {
				first.Module[i].Key, differ = "[*]", true
			}
		}
		if a.Key != first.Key {
			first.Key, differ = "[*]", true
		}
	}
	// Steps on one instance's current and deposed objects.
	if !differ {
		first.Key = "[*]"
	}
	return first.String()
}

// ups lists what Terraform applies before res: what its configuration
// refers to, every resource of a module it refers to as a whole, what its
// provider's configuration refers to, and what replace_triggered_by names.
func (s *applySim) ups(res string) []string {
	var out []string
	if n := s.in.graph.Nodes[res]; n != nil {
		out = slices.Concat(n.Up, n.Modules)
	}
	return slices.Concat(out, s.providerDeps(res), s.in.refs[res])
}

// providerDeps lists what the configuration of res's provider reads.
func (s *applySim) providerDeps(res string) []string {
	if n := s.in.graph.Nodes[res]; n != nil {
		return n.Provider
	}
	return s.in.provided[res]
}

// nearestCreators finds the closest resources upstream of res that are
// created, updated or read in this apply. Terraform walks resources that do
// not change in order too, so waits pass through them.
func (s *applySim) nearestCreators(res string) []string {
	out, ok := s.nearest[res]
	if !ok {
		out = s.nearestFrom(res, s.ups(res))
		s.nearest[res] = out
	}
	return out
}

// providedBy finds the closest resources created, updated or read in this
// apply that res's provider's configuration refers to, or that stand behind
// what it refers to.
func (s *applySim) providedBy(res string) []string {
	out, ok := s.provided[res]
	if !ok {
		out = s.nearestFrom(res, s.providerDeps(res))
		s.provided[res] = out
	}
	return out
}

func (s *applySim) nearestFrom(res string, start []string) []string {
	var out, queue []string
	seen := map[string]bool{res: true}
	visit := func(ups []string) {
		for _, up := range ups {
			if seen[up] {
				continue
			}
			seen[up] = true
			if len(s.creators[up]) > 0 {
				out = append(out, up)
			} else {
				queue = append(queue, up)
			}
		}
	}
	visit(start)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		visit(s.ups(cur))
	}
	return out
}

// deps lists the resources an object depends on for destroy ordering, and
// reports whether state recorded them. An object that state has no record
// of, a new one, depends on everything upstream of it. Unless all is set,
// that is cut down to the resources the apply destroys, the only ones a
// create, update or read is ordered against (see connect): lists of
// everything upstream of each new object would grow with the square of a
// chain's length.
func (s *applySim) deps(ch *model.Change, all bool) ([]string, bool) {
	if deps, ok := s.in.stored[ch.Address]; ok {
		return deps, true
	}
	if all {
		return s.upstreamOf(ch.Resource), false
	}
	return s.destroyedUpstream(ch.Resource), false
}

// sameDeps reports whether a step whose object depends on deps belongs to a
// class. A new object's list may be cut down, so where only one of the two
// lists is what state recorded, that one is set against everything
// upstream.
func (s *applySim) sameDeps(c *class, deps []string, recorded bool) bool {
	switch {
	case c.recorded == recorded:
		return slices.Equal(c.deps, deps)
	case recorded:
		return slices.Equal(deps, s.upstreamOf(c.res))
	default:
		return slices.Equal(c.deps, s.upstreamOf(c.res))
	}
}

// upstreamOf lists everything upstream of a resource, sorted.
func (s *applySim) upstreamOf(res string) []string {
	if deps, ok := s.upstream[res]; ok {
		return deps
	}
	var deps []string
	seen := map[string]bool{res: true}
	queue := []string{res}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, up := range s.ups(cur) {
			if !seen[up] {
				seen[up] = true
				deps = append(deps, up)
				queue = append(queue, up)
			}
		}
	}
	sort.Strings(deps)
	s.upstream[res] = deps
	return deps
}

// destroyedUpstream lists, sorted, the resources upstream of res that the
// apply destroys. It works out the list for everything upstream of res too,
// each once, from the lists of what is directly upstream, and shares a list
// wherever a resource adds nothing to it, so a long chain costs little.
func (s *applySim) destroyedUpstream(res string) []string {
	if out, ok := s.gone[res]; ok {
		return out
	}
	// The resources upstream of res without a list yet, and what is
	// directly upstream of each.
	index := map[string]int{res: 0}
	names := []string{res}
	var ups [][]string
	for i := 0; i < len(names); i++ {
		ups = append(ups, s.ups(names[i]))
		for _, up := range ups[i] {
			_, done := s.gone[up]
			if _, ok := index[up]; !ok && !done {
				index[up] = len(names)
				names = append(names, up)
			}
		}
	}
	// Resources upstream of each other, which only an approximate graph
	// has, have everything upstream in common but themselves. Each set
	// comes after the sets upstream of it.
	comp := components(len(names), func(v int, visit func(int)) {
		for _, up := range ups[v] {
			if w, ok := index[up]; ok {
				visit(w)
			}
		}
	})
	sets := make([][]int, slices.Max(comp)+1)
	for v, c := range comp {
		sets[c] = append(sets[c], v)
	}
	for c, set := range sets {
		var lists [][]string
		var direct []string
		for _, v := range set {
			for _, up := range ups[v] {
				if w, ok := index[up]; !ok || comp[w] != c {
					lists = append(lists, s.gone[up])
					if s.destroyed[up] {
						direct = append(direct, up)
					}
				}
			}
		}
		for _, v := range set {
			extra := direct
			for _, o := range set {
				if o != v && s.destroyed[names[o]] {
					extra = append(slices.Clip(extra), names[o])
				}
			}
			s.gone[names[v]] = sortedUnion(lists, extra)
		}
	}
	return s.gone[res]
}

// sortedUnion merges sorted lists and some further items into one sorted
// list. Where that adds nothing to one of the lists, it is that list.
func sortedUnion(lists [][]string, extra []string) []string {
	var first []string
	for _, l := range lists {
		if len(l) > len(first) {
			first = l
		}
	}
	adds := func(l []string) bool {
		if len(l) == 0 || len(l) == len(first) && &l[0] == &first[0] {
			return false
		}
		for _, x := range l {
			if _, found := slices.BinarySearch(first, x); !found {
				return true
			}
		}
		return false
	}
	if !adds(extra) && !slices.ContainsFunc(lists, adds) {
		return first
	}
	out := slices.Concat(append(slices.Clip(lists), extra)...)
	slices.Sort(out)
	return slices.Compact(out)
}

// instanceAddress is the instance a change acts on, which a deposed object
// shares with the current one.
func instanceAddress(ch *model.Change) string {
	if ch.Deposed == "" {
		return ch.Address
	}
	return strings.TrimSuffix(ch.Address, deposedSuffix(ch.Deposed))
}

// link fills in what waits for each step and join, in index order.
func (s *applySim) link() {
	for _, st := range s.nodes {
		st.succ = st.succ[:0]
	}
	for _, st := range s.nodes {
		for _, w := range st.pred {
			w.on.succ = append(w.on.succ, st)
		}
	}
}

// sorted returns the steps and joins so that each comes after everything
// it waits for. Terraform cannot apply a graph with a cycle, so a plan it
// made has none, but interragate only approximates that graph; rather than
// get stuck, it drops the waits that close cycles. Weak ones go first, as
// Terraform drops a destroy edge between providers rather than close a
// cycle: every weak wait between steps that wait on each other, all found
// in one pass, and then, depth first, any wait that still closes a cycle.
func (s *applySim) sorted() []*simStep {
	s.link()
	if s.dropWeakCycles() {
		s.link()
	}
	state := make([]int8, len(s.nodes)) // 0 unvisited, 1 on the path, 2 done
	post := make([]*simStep, 0, len(s.nodes))
	var closing [][2]*simStep // waits that close a cycle: the waiting step, then what it waits for
	var visit func(st *simStep)
	visit = func(st *simStep) {
		state[st.index] = 1
		kept := st.succ[:0]
		for _, nx := range st.succ {
			switch state[nx.index] {
			case 1:
				// nx waits for st, which waits for nx along the path.
				closing = append(closing, [2]*simStep{nx, st})
				continue
			case 0:
				visit(nx)
			}
			kept = append(kept, nx)
		}
		st.succ = kept
		state[st.index] = 2
		post = append(post, st)
	}
	for _, st := range s.nodes {
		if state[st.index] == 0 {
			visit(st)
		}
	}
	for _, c := range closing {
		c[0].drop(c[1])
	}
	slices.Reverse(post)
	return post
}

// dropWeakCycles drops the weak waits that lie on cycles: those between
// steps in one strongly connected component, each of which waits, through
// others, on every other. It reports whether it dropped any.
func (s *applySim) dropWeakCycles() bool {
	comp := components(len(s.nodes), func(v int, visit func(int)) {
		for _, nx := range s.nodes[v].succ {
			visit(nx.index)
		}
	})
	dropped := false
	for _, st := range s.nodes {
		n := len(st.pred)
		st.pred = slices.DeleteFunc(st.pred, func(w wait) bool { return w.weak && comp[w.on.index] == comp[st.index] })
		dropped = dropped || len(st.pred) < n
	}
	return dropped
}

// components numbers the strongly connected components of a graph of n
// nodes, given what each node leads to, with Tarjan's algorithm. A
// component's number is higher than those of the components it leads to.
func components(n int, edges func(v int, visit func(w int))) []int {
	order := make([]int, n) // when each node was reached, from 1; 0 for not yet
	low := make([]int, n)
	comp := make([]int, n)
	onStack := make([]bool, n)
	var stack []int
	reached, done := 0, 0
	var visit func(v int)
	visit = func(v int) {
		reached++
		order[v], low[v] = reached, reached
		stack = append(stack, v)
		onStack[v] = true
		edges(v, func(w int) {
			if order[w] == 0 {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], order[w])
			}
		})
		if low[v] < order[v] {
			return
		}
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			comp[w] = done
			if w == v {
				break
			}
		}
		done++
	}
	for v := range n {
		if order[v] == 0 {
			visit(v)
		}
	}
	return comp
}

// reduce leaves each step and join waiting only on what it needs directly:
// a wait that another one already implies adds nothing but clutter in the
// report. A wait is implied when what it waits for is an ancestor of
// another wait, so reduce collects ancestors in topological order as sets
// of bits, one bit for everything a step or join with two or more waits
// waits for (the only waits that can be implied). Where those sets would
// take more than reduceMemory, it takes the bits a slice at a time.
func (s *applySim) reduce(order []*simStep) {
	n := len(order)
	pos := make([]int, len(s.nodes))
	for i, st := range order {
		pos[st.index] = i
	}
	bit := make([]int, len(s.nodes))
	for _, st := range order {
		if len(st.pred) > 1 {
			for _, w := range st.pred {
				bit[w.on.index] = 1
			}
		}
	}
	var at []int // the position of each bit's step, in order
	for i, st := range order {
		if bit[st.index] == 1 {
			bit[st.index] = len(at)
			at = append(at, i)
		} else {
			bit[st.index] = -1
		}
	}
	if len(at) == 0 {
		return
	}
	words := min(max(reduceMemory/(8*n), 1), (len(at)+63)/64)
	rows := make([]uint64, n*words)
	for lo := 0; lo < len(at); lo += 64 * words {
		hi := min(lo+64*words, len(at))
		// Nothing before the first bit's step has any of these bits.
		for i := at[lo]; i < n; i++ {
			st := order[i]
			row := rows[i*words : (i+1)*words]
			clear(row)
			for _, w := range st.pred {
				if j := pos[w.on.index]; j >= at[lo] {
					for k, bits := range rows[j*words : (j+1)*words] {
						row[k] |= bits
					}
				}
			}
			for k, w := range st.pred {
				if b := bit[w.on.index] - lo; b >= 0 && b < hi-lo {
					if len(st.pred) > 1 && row[b/64]&(1<<(b%64)) != 0 {
						st.pred[k].redundant = true
					}
				}
			}
			for _, w := range st.pred {
				if b := bit[w.on.index] - lo; b >= 0 && b < hi-lo {
					row[b/64] |= 1 << (b % 64)
				}
			}
		}
	}
	for _, st := range s.nodes {
		st.pred = slices.DeleteFunc(st.pred, func(w wait) bool { return w.redundant })
	}
	s.link()
}

// schedule runs the steps the way Terraform walks its graph: a step starts
// once what it waits for has finished and one of parallelism slots is free.
// When more steps are ready than there are slots, the one with the longest
// chain of work behind it goes first, ties broken by ID. Terraform hands out
// slots roughly in the order steps become ready, so its order varies from
// run to run; this choice gives about the shortest apply such a walk allows,
// and the same answer every time. A join takes no time and no slot: it ends
// with the last of the steps it stands for.
func (s *applySim) schedule(order []*simStep) {
	for i := len(order) - 1; i >= 0; i-- {
		st := order[i]
		for _, nx := range st.succ {
			st.rank = max(st.rank, nx.rank)
		}
		st.rank += st.Duration
	}
	ready := &readyQueue{}
	now, seq := 0, 0
	var finish func(st *simStep)
	finish = func(st *simStep) {
		for _, nx := range st.succ {
			if nx.waiting--; nx.waiting > 0 {
				continue
			}
			if nx.join != nil {
				nx.Start, nx.end = now, now
				finish(nx)
			} else {
				heap.Push(ready, nx)
			}
		}
	}
	var start []*simStep
	for _, st := range order {
		for _, w := range st.pred {
			st.lead = max(st.lead, w.on.lead)
		}
		st.lead += st.Duration
		if st.waiting = len(st.pred); st.waiting == 0 {
			start = append(start, st)
		}
	}
	for _, st := range start {
		if st.join != nil {
			finish(st)
		} else {
			heap.Push(ready, st)
		}
	}
	var running []*simStep
	for ready.Len() > 0 || len(running) > 0 {
		for len(running) < parallelism && ready.Len() > 0 {
			st := heap.Pop(ready).(*simStep)
			st.Start, st.end, st.seq = now, now+st.Duration, seq
			seq++
			running = append(running, st)
		}
		now = running[0].end
		for _, st := range running[1:] {
			now = min(now, st.end)
		}
		// Everything that ends now finishes together, so the next choice
		// sees every step that became ready.
		var done []*simStep
		kept := running[:0]
		for _, st := range running {
			if st.end == now {
				done = append(done, st)
			} else {
				kept = append(kept, st)
			}
		}
		running = kept
		for _, st := range done {
			finish(st)
		}
	}
}

// readyQueue holds the steps that could start, the longest chain first.
type readyQueue []*simStep

func (q readyQueue) Len() int { return len(q) }
func (q readyQueue) Less(i, j int) bool {
	if q[i].rank != q[j].rank {
		return q[i].rank > q[j].rank
	}
	return q[i].ID < q[j].ID
}
func (q readyQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *readyQueue) Push(x any)   { *q = append(*q, x.(*simStep)) }
func (q *readyQueue) Pop() any {
	old := *q
	st := old[len(old)-1]
	*q = old[:len(old)-1]
	return st
}

// critical is the chain of waits that ends last: from the final step, back
// through whichever step or join it waited on finished last. Joins pass the
// chain on without joining it.
func (s *applySim) critical() []string {
	var cur *simStep
	for _, st := range s.steps {
		if cur == nil || endsLater(st, cur) {
			cur = st
		}
	}
	var chain []string
	for cur != nil {
		if cur.join == nil {
			chain = append(chain, cur.ID)
		}
		var next *simStep
		for _, w := range cur.pred {
			if next == nil || endsLater(w.on, next) {
				next = w.on
			}
		}
		cur = next
	}
	slices.Reverse(chain)
	return chain
}

// endsLater orders steps by when they end, then by the work leading up to
// them, then by ID.
func endsLater(a, b *simStep) bool {
	if a.end != b.end {
		return a.end > b.end
	}
	if a.lead != b.lead {
		return a.lead > b.lead
	}
	return a.ID < b.ID
}

// gaps finds, for every replacement that destroys before it creates, the
// window in which the resource does not exist and what depends on it
// meanwhile.
//
// A plain destroy is not a gap: nothing comes back, so there is no window
// to bound, and the findings already say what losing the object means.
func (s *applySim) gaps() []*model.ApplyGap {
	var out []*model.ApplyGap
	for _, ch := range s.in.changes {
		if ch.Action != model.Replace || ch.Order != "delete-first" {
			continue
		}
		d, c := s.byID[ch.Address+":"+opDestroy], s.byID[ch.Address+":"+opCreate]
		if d == nil || c == nil {
			continue
		}
		out = append(out, &model.ApplyGap{Address: ch.Address, From: d.Start, To: c.end, Dependents: s.affected(ch.Resource)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].Address < out[j].Address
	})
	return out
}

// affected lists the resources that read res directly and exist while it is
// missing, so may fail or misbehave until it is back. Data sources are
// looked through, since they only pass its values on.
func (s *applySim) affected(res string) []string {
	var out []string
	seen := map[string]bool{res: true}
	var visit func(id string)
	visit = func(id string) {
		n := s.in.graph.Nodes[id]
		if n == nil {
			return
		}
		for _, down := range n.Down {
			if seen[down] {
				continue
			}
			seen[down] = true
			if dn := s.in.graph.Nodes[down]; dn != nil && dn.Mode == "data" {
				visit(down)
			} else if s.persists(down) {
				out = append(out, down)
			}
		}
	}
	visit(res)
	sort.Strings(out)
	return out
}

// persists reports whether some instance of a resource exists all through
// the apply: one that is neither new nor destroyed first. A new one only
// appears once what it reads is back.
func (s *applySim) persists(res string) bool {
	for _, ch := range s.instances[res] {
		switch {
		case ch.Action == model.Create, ch.Action == model.Delete:
		case ch.Action == model.Replace && ch.Order == "delete-first":
		default:
			return true
		}
	}
	return false
}

// applyNarrative says how long the apply should take and what goes missing
// meanwhile. A time built only from default durations is a guess, so times
// are given only when some step's duration is known.
func applyNarrative(ap *model.Apply, r *model.Report) []model.Sentence {
	byAddr := map[string]*model.Change{}
	for _, ch := range r.Changes {
		byAddr[ch.Address] = ch
	}
	known := false
	for _, st := range ap.Steps {
		known = known || st.Known
	}
	var out []model.Sentence
	fail, failing := failSentence(ap, r.Findings)
	if fail != nil {
		out = append(out, *fail)
	}
	if known {
		d := durationSentence(ap, byAddr, r.Plan)
		// The rest describes the apply that works, once the failures are dealt with.
		switch {
		case failing == 1:
			d.Text = "Once that is fixed, t" + strings.TrimPrefix(d.Text, "T")
		case failing > 1:
			d.Text = "Once those are fixed, t" + strings.TrimPrefix(d.Text, "T")
		}
		out = append(out, d)
	}
	if g := gapToMention(ap, byAddr, known); g != nil {
		out = append(out, gapSentence(g, known))
	}
	if s := strandSentence(ap, byAddr); s != nil {
		out = append(out, *s)
	}
	return out
}

// failSentence says where the apply is expected to stop and what that
// leaves undone. Terraform skips every step that waits for a failed one and
// carries on with the rest. So a step expected to fail that waits for
// another is skipped rather than attempted, and fails only once the first
// is fixed. It also returns how many failures it names.
// failure is what an apply leaves undone when the steps findings expect to
// fail do fail.
type failure struct {
	failing, later     []*model.ApplyStep // the failures that happen, and those that wait for one and come later
	failingAt, laterAt []string           // their addresses
	skipped            []string           // resources skipped because they wait for a failure
	missing            []string           // replacements left destroyed with nothing in their place
	both               []string           // replacements left with their old object as well as the new one
	halted             map[string]bool    // IDs of the steps that do not run: those that fail and those that wait for them
}

// expectFailure works out what the expected failures leave undone, or
// returns nil when no step is expected to fail.
func expectFailure(ap *model.Apply) *failure {
	byID := map[string]*model.ApplyStep{}
	var predicted []*model.ApplyStep
	for _, st := range ap.Steps {
		byID[st.ID] = st
		if st.Fails != "" {
			predicted = append(predicted, st)
		}
	}
	if len(predicted) == 0 {
		return nil
	}
	// What waits for some steps, through joins too, which are not steps
	// themselves, in the order it is found.
	succ := waiters(ap)
	waiting := func(from []*model.ApplyStep) []string {
		var queue, out []string
		for _, st := range from {
			queue = append(queue, succ[st.ID]...)
		}
		seen := map[string]bool{}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
				queue = append(queue, succ[id]...)
			}
		}
		return out
	}
	skippedStep := map[string]bool{}
	for _, id := range waiting(predicted) {
		skippedStep[id] = true
	}
	// The failures that happen, one for each resource, and those that wait
	// for one of them and come later.
	var failing, later []*model.ApplyStep
	var failingAt, laterAt []string
	failed, at := map[string]bool{}, map[string]bool{}
	for _, st := range predicted {
		if !skippedStep[st.ID] && !at[st.Address] {
			failing = append(failing, st)
			failingAt = append(failingAt, st.Address)
			failed[st.ID], at[st.Address] = true, true
		}
	}
	for _, st := range predicted {
		if !at[st.Address] && !slices.Contains(laterAt, st.Address) {
			later = append(later, st)
			laterAt = append(laterAt, st.Address)
		}
	}
	var skipped []string
	for _, id := range waiting(failing) {
		if st := byID[id]; st != nil && !at[st.Address] {
			at[st.Address] = true
			skipped = append(skipped, st.Address)
		}
	}
	runs := func(st *model.ApplyStep) bool { return !skippedStep[st.ID] && !failed[st.ID] }
	// A replacement whose destroy runs but whose create does not, skipped or
	// failed, is left destroyed. One whose create runs but whose old
	// object's destroy is skipped is left with both.
	var missing, both []string
	for _, a := range slices.Concat(failingAt, skipped) {
		destroy, create := byID[a+":destroy"], byID[a+":create"]
		switch {
		case destroy == nil || create == nil:
		case !destroy.Deposed && runs(destroy) && !runs(create):
			missing = append(missing, a)
		case destroy.Deposed && runs(create) && skippedStep[destroy.ID]:
			both = append(both, a)
		}
	}
	halted := maps.Clone(skippedStep)
	maps.Copy(halted, failed)
	return &failure{failing: failing, later: later, failingAt: failingAt, laterAt: laterAt, skipped: skipped, missing: missing, both: both, halted: halted}
}

func failSentence(ap *model.Apply, findings []*model.Finding) (*model.Sentence, int) {
	fl := expectFailure(ap)
	if fl == nil {
		return nil, 0
	}
	failing, later, failingAt, laterAt, skipped, missing, both := fl.failing, fl.later, fl.failingAt, fl.laterAt, fl.skipped, fl.missing, fl.both
	reason := func(st *model.ApplyStep) string {
		for _, f := range findings {
			if r, ok := strings.CutPrefix(f.Title, "Apply will fail: "); ok && f.ID == st.Fails {
				return ", where " + r
			}
		}
		return ""
	}
	codes := func(as []string) string {
		cs := make([]string, len(as))
		for i, a := range as {
			cs[i] = rules.Code(a)
		}
		return rules.List(cs, 3)
	}
	one := func(n int, single, plural string) string {
		if n == 1 {
			return single
		}
		return plural
	}
	text := "The apply should fail at " + codes(failingAt)
	if len(failing) == 1 {
		text += reason(failing[0])
	}
	it := one(len(failing), "it", "them")
	refs := slices.Clone(failingAt)
	n := len(skipped)
	switch {
	case n == 0 && len(missing) == 0:
		text += "; nothing else waits for " + it + ", so the rest still applies."
	case n == 0:
		// Only a failed create leaves something destroyed.
		text += fmt.Sprintf(". Nothing else waits for %s, so the rest still applies, but %s %s left destroyed with nothing in %s place.",
			it, codes(missing), isAre(len(missing)), one(len(missing), "its", "their"))
	case len(missing) == 0 && len(both) == 0 && n <= 3:
		text += fmt.Sprintf(", so %s, which %s for %s, %s; the rest still applies.",
			codes(skipped), one(n, "waits", "wait"), it, one(n, "is left as it is", "are left as they are"))
		refs = append(refs, skipped...)
	case len(missing) == 0 && len(both) == 0:
		text += fmt.Sprintf(", so the %d resources that wait for %s are left as they are; the rest still applies.", n, it)
	default:
		text += fmt.Sprintf(". %s that %s for %s %s skipped, which leaves ", capitalize(countOf(n, "resource")), one(n, "waits", "wait"), it, isAre(n))
		var left []string
		if len(missing) > 0 {
			left = append(left, fmt.Sprintf("%s destroyed with nothing in %s place", codes(missing), one(len(missing), "its", "their")))
		}
		if len(both) > 0 {
			left = append(left, codes(both)+" with "+one(len(both), "its old object as well as the new one", "their old objects as well as the new ones"))
		}
		text += strings.Join(left, ", and ") + "."
	}
	if len(later) > 0 {
		text += fmt.Sprintf(" Fixing %s is not enough: %s should fail as well", one(len(failing), "that", "those"), codes(laterAt))
		if len(later) == 1 {
			text += reason(later[0])
		}
		text += "."
	}
	// A failed create can leave its own resource destroyed, so it would be
	// named twice.
	seen := map[string]bool{}
	refs = slices.DeleteFunc(slices.Concat(refs, missing, both, laterAt), func(a string) bool {
		dup := seen[a]
		seen[a] = true
		return dup
	})
	return &model.Sentence{Severity: model.High, Text: text, Refs: refs}, len(failing) + len(later)
}

// waiters maps the ID of each step and join to the IDs of the steps and
// joins that wait for it directly.
func waiters(ap *model.Apply) map[string][]string {
	succ := map[string][]string{}
	for _, st := range ap.Steps {
		for _, p := range st.After {
			succ[p] = append(succ[p], st.ID)
		}
	}
	for _, j := range ap.Joins {
		for _, p := range j.After {
			succ[p] = append(succ[p], j.ID)
		}
	}
	return succ
}

// countOf says "the one resource" or "the 22 resources".
func countOf(n int, noun string) string {
	if n == 1 {
		return "the one " + noun
	}
	return fmt.Sprintf("the %d %ss", n, noun)
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// strandSentence warns when a resource that holds data is destroyed before
// its replacement exists and the replacement waits on steps the destroy does
// not. Terraform carries on with work that does not depend on a failure, so
// if one of those steps fails, the destroy still happens and the create
// never does: the data is gone and nothing has taken its place. Of the
// resources at risk, it names the one with the most such steps.
func strandSentence(ap *model.Apply, byAddr map[string]*model.Change) *model.Sentence {
	pred := map[string][]string{}
	step := map[string]bool{}
	for _, st := range ap.Steps {
		pred[st.ID] = st.After
		step[st.ID] = true
	}
	for _, j := range ap.Joins {
		pred[j.ID] = j.After
	}
	ancestors := func(id string) map[string]bool {
		seen := map[string]bool{}
		queue := append([]string(nil), pred[id]...)
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if seen[cur] {
				continue
			}
			seen[cur] = true
			queue = append(queue, pred[cur]...)
		}
		return seen
	}
	best, most := "", 0
	for _, g := range ap.Gaps {
		ch := byAddr[g.Address]
		destroy, create := g.Address+":destroy", g.Address+":create"
		if ch == nil || !ch.Stateful || pred[create] == nil && pred[destroy] == nil {
			continue
		}
		before := ancestors(destroy)
		n := 0
		for id := range ancestors(create) {
			if id != destroy && !before[id] && step[id] {
				n++
			}
		}
		if n > most || n == most && n > 0 && g.Address < best {
			best, most = g.Address, n
		}
	}
	if most == 0 {
		return nil
	}
	which := fmt.Sprintf("any of the %d steps", most)
	if most == 1 {
		which = "the one step"
	}
	return &model.Sentence{
		Severity: model.Medium,
		Text:     fmt.Sprintf("If %s that recreating %s waits for fails, it is left destroyed with nothing in its place.", which, rules.Code(best)),
		Refs:     []string{best},
	}
}

// durationSentence gives the estimate and what it is mostly made of: the
// resource with the most time on the chain of waits that ends last.
func durationSentence(ap *model.Apply, byAddr map[string]*model.Change, info model.PlanInfo) model.Sentence {
	steps := map[string]*model.ApplyStep{}
	for _, st := range ap.Steps {
		steps[st.ID] = st
	}
	// A replacement's two steps count together.
	spent := map[string]int{}
	top, chain := "", 0
	for _, id := range ap.Critical {
		st := steps[id]
		spent[st.Address] += st.Duration
		chain += st.Duration
		if top == "" || spent[st.Address] > spent[top] {
			top = st.Address
		}
	}
	what := "apply"
	if info.Destroy {
		what = "destroy"
	}
	text := fmt.Sprintf("The %s should take %s", what, roughly(ap.Duration))
	refs := []string{top}
	switch {
	case spent[top]*2 >= ap.Duration:
		text += ", most of it " + doing(byAddr[top]) + " " + rules.Code(top)
	case chain*2 < ap.Duration:
		// The waits explain less than half the time, so steps spend most of
		// it queueing for a free slot.
		text += fmt.Sprintf(", mostly because %s runs at most %d operations at once", info.ToolName(), ap.Parallelism)
		refs = nil
	default:
		text += "; the longest part is " + doing(byAddr[top]) + " " + rules.Code(top)
	}
	return model.Sentence{Severity: model.Info, Text: text + ".", Refs: refs}
}

// gapToMention picks the gap worth a sentence: the longest, or without known
// durations the one most resources depend on. Utility resources are skipped,
// since nothing outside Terraform notices a random value or a null resource
// is briefly gone; and without durations, a gap nothing depends on says no
// more than the findings do.
func gapToMention(ap *model.Apply, byAddr map[string]*model.Change, known bool) *model.ApplyGap {
	var best *model.ApplyGap
	for _, g := range ap.Gaps {
		ch := byAddr[g.Address]
		if ch == nil || ch.Category == string(kb.Utility) || (!known && len(g.Dependents) == 0) {
			continue
		}
		if best == nil {
			best = g
			continue
		}
		long, bestLong := g.To-g.From, best.To-best.From
		switch {
		case known && long != bestLong:
			if long > bestLong {
				best = g
			}
		case len(g.Dependents) != len(best.Dependents):
			if len(g.Dependents) > len(best.Dependents) {
				best = g
			}
		}
	}
	return best
}

func gapSentence(g *model.ApplyGap, known bool) model.Sentence {
	text := rules.Code(g.Address) + " will not exist"
	if known {
		text += " for " + roughly(g.To-g.From)
	} else {
		text += " while it is replaced"
	}
	sev := model.Low
	if n := len(g.Dependents); n > 0 {
		sev = model.Medium
		who := fmt.Sprintf("%d resources", n)
		if n <= 2 {
			who = codes(g.Dependents)
		}
		verb := "depend"
		if n == 1 {
			verb = "depends"
		}
		if known {
			text += " while "
		} else {
			text += ", and "
		}
		text += who + " " + verb + " on it"
	}
	return model.Sentence{Severity: sev, Text: text + ".", Refs: append([]string{g.Address}, g.Dependents...)}
}

// doing names what the apply does to a change, for prose.
func doing(ch *model.Change) string {
	if ch == nil {
		return "changing"
	}
	switch ch.Action {
	case model.Create:
		return "creating"
	case model.Update:
		return "updating"
	case model.Replace:
		return "replacing"
	case model.Delete:
		return "destroying"
	case model.Read:
		return "reading"
	}
	return "changing"
}

// roughly renders a duration the way people estimate one: under a minute,
// then to the minute, then to five minutes once past the hour.
func roughly(seconds int) string { return model.Roughly(seconds) }
