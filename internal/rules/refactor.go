package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/kb"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

// refactorRules flag renames and reshuffles that destroy and recreate
// instead of moving.
func refactorRules() []*Rule {
	return []*Rule{
		{
			ID: "missing-moved-block", Title: "Looks like a rename without a moved block", Severity: model.Medium, Category: Refactor,
			Doc: "Pairs each destroyed resource with a created resource of the same type and near-identical settings. That is almost always a rename " +
				"or a move into a module, and a moved block keeps the real object instead of destroying and recreating it. " +
				"Whole modules that moved are suggested as one moved block.",
			Check: checkMissingMoved,
		},
		{
			ID: "count-index-shift", Title: "Removing a list item shifts count indexes", Severity: model.Medium, Category: Refactor,
			Doc: "Detects the classic count pitfall: removing an item from the middle of a list shifts every later instance down one index, " +
				"so they are updated or replaced to become their neighbour and the last one is destroyed. for_each keyed by a stable value avoids it.",
			Check: checkCountShift,
		},
	}
}

var renameIgnored = map[string]bool{"id": true, "arn": true, "tags_all": true, "owner_id": true, "unique_id": true}

var nameLike = []string{"name", "bucket", "identifier", "function_name", "cluster_identifier", "domain_name", "repository_name", "queue_name", "topic_name"}

// sameSettings scores how alike a destroyed object and a created one are,
// over the created object's known, non-empty attributes.
func sameSettings(before, after map[string]any, unknown func(string) bool) (score float64, compared int) {
	same := 0
	for k, av := range after {
		if renameIgnored[k] || unknown(k) || diff.IsEmpty(av) {
			continue
		}
		compared++
		if diff.Equal(av, before[k]) {
			same++
		}
	}
	if compared == 0 {
		return 0, 0
	}
	return float64(same) / float64(compared), compared
}

type renamePair struct {
	from, to *model.Change
	score    float64
}

func checkMissingMoved(c *Context) []*model.Finding {
	var deletes, creates []*model.Change
	for _, ch := range c.Report.Changes {
		if !Managed(ch) || ch.Deposed != "" {
			continue
		}
		switch {
		case ch.Action == model.Delete:
			switch ch.Reason {
			case "delete_because_no_resource_config", "delete_because_no_module", "delete_because_wrong_repetition", "delete_because_each_key", "":
				deletes = append(deletes, ch)
			}
		case ch.Action == model.Create && !ch.Importing:
			creates = append(creates, ch)
		}
	}
	var cands []renamePair
	for _, d := range deletes {
		before := c.Before(d)
		for _, cr := range creates {
			if cr.Type != d.Type && !(d.Type == "null_resource" && cr.Type == "terraform_data") {
				continue
			}
			after := c.After(cr)
			if differentName(before, after) {
				continue
			}
			var score float64
			var n int
			if d.Type == "null_resource" && cr.Type == "terraform_data" {
				if diff.Equal(before["triggers"], after["triggers_replace"]) {
					score, n = 1, 1
				}
			} else {
				score, n = sameSettings(before, after, func(k string) bool { return c.Unknown(cr, k) })
			}
			if (n >= 2 && score >= 0.8) || (n == 1 && score == 1) {
				cands = append(cands, renamePair{from: d, to: cr, score: score})
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	usedFrom, usedTo := map[*model.Change]bool{}, map[*model.Change]bool{}
	var pairs []renamePair
	for _, p := range cands {
		if usedFrom[p.from] || usedTo[p.to] {
			continue
		}
		// Ambiguous: another create matches this delete just as well.
		ambiguous := false
		for _, q := range cands {
			if q.from == p.from && q.to != p.to && q.score == p.score && !usedTo[q.to] {
				ambiguous = true
			}
		}
		if ambiguous {
			continue
		}
		usedFrom[p.from], usedTo[p.to] = true, true
		pairs = append(pairs, p)
	}
	var out []*model.Finding
	grouped := map[*model.Change]bool{}
	for _, g := range moduleMoves(pairs, deletes) {
		var addrs []string
		for _, p := range g.pairs {
			addrs = append(addrs, p.from.Address, p.to.Address)
			grouped[p.from] = true
		}
		f := &model.Finding{
			Title:       "Looks like a module was renamed",
			Severity:    model.High,
			Short:       "recreates a renamed module",
			Message:     fmt.Sprintf("Everything in %s (%s) is destroyed and recreated in %s with the same settings. If you renamed or moved the module, a moved block keeps the existing objects.", Code(g.from), Plural(len(g.pairs), "resource", "resources"), Code(g.to)),
			Addresses:   addrs,
			Snippet:     fmt.Sprintf("moved {\n  from = %s\n  to   = %s\n}", g.from, g.to),
			Remediation: "Add the moved block to the calling module, plan again, and check that these resources now show as moved.",
		}
		out = append(out, f)
	}
	for _, p := range pairs {
		if grouped[p.from] {
			continue
		}
		info := c.Info(p.to)
		sev := model.Medium
		if info.Stateful || info.Category == kb.Compute || info.Category == kb.Database || info.Category == kb.Storage || info.Category == kb.Containers {
			sev = model.High
		}
		f := &model.Finding{
			Title:    "Looks like a rename without a moved block",
			Severity: sev,
			Short:    "recreates a renamed " + info.Noun,
			Message: fmt.Sprintf("%s is destroyed and %s is created with the same settings. If you renamed it, a moved block keeps the existing %s instead of recreating it.",
				Code(p.from.Address), Code(p.to.Address), info.Noun),
			Addresses:   []string{p.from.Address, p.to.Address},
			Snippet:     fmt.Sprintf("moved {\n  from = %s\n  to   = %s\n}", p.from.Address, p.to.Address),
			Remediation: "Add the moved block, plan again, and check that the resource now shows as moved with no destroy.",
		}
		if p.from.Type != p.to.Type {
			f.Message = fmt.Sprintf("%s is destroyed and %s is created with the same triggers. Terraform 1.9 and later can move null_resource to terraform_data without recreating it.", Code(p.from.Address), Code(p.to.Address))
		}
		out = append(out, f)
	}
	return out
}

func differentName(before, after map[string]any) bool {
	for _, k := range nameLike {
		bv, bok := before[k].(string)
		av, aok := after[k].(string)
		if bok && aok && bv != "" && av != "" && bv != av {
			return true
		}
	}
	return false
}

type moduleMove struct {
	from, to string
	pairs    []renamePair
}

// moduleMoves finds pairs that all move from one module instance to another
// with the same local address, covering everything destroyed in the source.
func moduleMoves(pairs []renamePair, deletes []*model.Change) []moduleMove {
	groups := map[[2]string]*moduleMove{}
	var order [][2]string
	for _, p := range pairs {
		if p.from.Module == p.to.Module || p.from.Module == "" || p.to.Module == "" {
			continue
		}
		if strings.TrimPrefix(p.from.Address, p.from.Module+".") != strings.TrimPrefix(p.to.Address, p.to.Module+".") {
			continue
		}
		key := [2]string{p.from.Module, p.to.Module}
		if groups[key] == nil {
			groups[key] = &moduleMove{from: p.from.Module, to: p.to.Module}
			order = append(order, key)
		}
		groups[key].pairs = append(groups[key].pairs, p)
	}
	var out []moduleMove
	for _, key := range order {
		g := groups[key]
		total := 0
		for _, d := range deletes {
			if d.Module == g.from || strings.HasPrefix(d.Module, g.from+".") {
				total++
			}
		}
		if total == len(g.pairs) && len(g.pairs) >= 2 {
			out = append(out, *g)
		}
	}
	return out
}

// shift is one resource whose count instances shift down after an item was
// removed from the middle of the list behind count.
type shift struct {
	resource  string
	instances []*model.Change
	moved     []int // instances proven to take over their neighbour's settings
	deleted   []int
	k         int
	keyAttr   string
	countRefs string
}

// shifts detects count shifts once per context.
func (c *Context) shifts() []*shift {
	if c.shiftCache != nil {
		return *c.shiftCache
	}
	byResource := map[string][]*model.Change{}
	var order []string
	for _, ch := range c.Report.Changes {
		if _, isInt := keyIndex(ch.Key); !isInt || !Managed(ch) {
			continue
		}
		if byResource[ch.Resource] == nil {
			order = append(order, ch.Resource)
		}
		byResource[ch.Resource] = append(byResource[ch.Resource], ch)
	}
	var out []*shift
	for _, res := range order {
		if s := detectShift(c, res, byResource[res]); s != nil {
			out = append(out, s)
		}
	}
	c.shiftCache = &out
	return out
}

func detectShift(c *Context, res string, instances []*model.Change) *shift {
	byIndex := map[int]*model.Change{}
	var deleted, changed []int
	for _, ch := range instances {
		i, _ := keyIndex(ch.Key)
		// Instances in different module instances never shift into each other.
		if prev, ok := byIndex[i]; ok && prev.Module != ch.Module {
			return nil
		}
		byIndex[i] = ch
		switch {
		case ch.Action == model.Delete && ch.Reason == "delete_because_count_index":
			deleted = append(deleted, i)
		case ch.Action == model.Update || ch.Action == model.Replace:
			changed = append(changed, i)
		}
	}
	if len(deleted) == 0 || len(changed) == 0 {
		return nil
	}
	sort.Ints(deleted)
	sort.Ints(changed)
	s := &shift{resource: res, instances: instances, deleted: deleted, k: len(deleted), countRefs: c.countRefs(res)}
	var keyAttrs []string
	for _, i := range changed {
		next := byIndex[i+s.k]
		if next == nil {
			return nil
		}
		ok, attrs := shiftsInto(c, byIndex[i], next)
		if !ok {
			return nil
		}
		if len(attrs) > 0 {
			s.moved = append(s.moved, i)
			keyAttrs = attrs
		}
	}
	if len(s.moved) == 0 {
		return nil
	}
	if len(keyAttrs) == 1 {
		s.keyAttr = keyAttrs[0]
	}
	return s
}

// shifted reports whether a resource has a detected count shift.
func (c *Context) shifted(res string) bool {
	for _, s := range c.shifts() {
		if s.resource == res {
			return true
		}
	}
	return false
}

func checkCountShift(c *Context) []*model.Finding {
	// Resources whose count uses the same references shift together; report
	// them as one finding.
	type group struct {
		module string
		shifts []*shift
	}
	var groups []*group
	index := map[string]*group{}
	for _, s := range c.shifts() {
		key := moduleOfResource(s.resource) + "|" + s.countRefs
		if s.countRefs == "" {
			key = s.resource
		}
		g := index[key]
		if g == nil {
			g = &group{module: moduleOfResource(s.resource)}
			index[key] = g
			groups = append(groups, g)
		}
		g.shifts = append(g.shifts, s)
	}
	var out []*model.Finding
	for _, g := range groups {
		first := g.shifts[0]
		var names []string
		var addrs []string
		sev := model.Medium
		var snippets []string
		dependents := map[string]bool{}
		for _, s := range g.shifts {
			names = append(names, Code(s.resource))
			for _, ch := range s.instances {
				if ch.Action != model.NoOp && !(ch.Action == model.Update && ch.Cosmetic != "") {
					addrs = append(addrs, ch.Address)
				}
			}
			info := c.Info(s.instances[0])
			if info.Stateful || info.Category == kb.Network || info.Category == kb.Compute || info.Category == kb.Database {
				sev = model.High
			}
			if s.keyAttr != "" {
				if snip := forEachMoves(c, s.instances, s.keyAttr); snip != "" {
					snippets = append(snippets, snip)
				}
			}
			for _, d := range c.Graph.Downstream(s.resource) {
				dependents[d] = true
			}
		}
		var shiftDesc string
		if len(first.moved) == 1 {
			shiftDesc = fmt.Sprintf("[%d] is changed to become what [%d] was", first.moved[0], first.moved[0]+first.k)
		} else {
			var moved []string
			for _, i := range first.moved {
				moved = append(moved, fmt.Sprintf("[%d]", i))
			}
			shiftDesc = fmt.Sprintf("%s each take over the settings of the instance %s after them", List(moved, 6), Plural(first.k, "place", "places"))
		}
		var gone []string
		for _, i := range first.deleted {
			gone = append(gone, fmt.Sprintf("[%d]", i))
		}
		subject := names[0]
		if len(names) > 1 {
			subject = List(names, 4)
		}
		msg := fmt.Sprintf("Removing %s from the middle of the list behind count shifts %s: %s, and %s %s destroyed.",
			Plural(first.k, "item", "items"), subject, shiftDesc, List(gone, 6), isAre(len(gone)))
		if len(names) > 1 {
			msg = fmt.Sprintf("Removing %s from the middle of the list behind count shifts %s the same way: %s, and %s %s destroyed.",
				Plural(first.k, "item", "items"), subject, shiftDesc, List(gone, 6), isAre(len(gone)))
		}
		msg += fmt.Sprintf(" Only %s needed to go.", Plural(first.k*len(names), "instance", "instances"))
		if followers := c.countSiblings(g.shifts); len(followers) > 0 {
			msg += " " + List(codesFor(followers), 3) + " " + map[bool]string{true: "uses", false: "use"}[len(followers) == 1] + " the same count and follow" + map[bool]string{true: "s", false: ""}[len(followers) == 1] + "."
		}
		if networkCore[first.instances[0].Type] && len(dependents) > 0 {
			msg += fmt.Sprintf(" %s depend on what is replaced.", Plural(len(dependents), "resource", "resources"))
		}
		f := &model.Finding{
			Title:       "Removing a list item shifts count indexes",
			Severity:    sev,
			Short:       "shifts every " + first.instances[0].Noun + " after the removed item",
			Message:     msg,
			Addresses:   addrs,
			Remediation: "Use for_each keyed by a stable value (for example for_each = toset(var.azs)) so each instance keeps its identity, and add moved blocks from the old indexes to the new keys when you switch.",
		}
		if len(snippets) > 0 {
			f.Snippet = fmt.Sprintf("# After switching to for_each keyed by %s:\n%s", first.keyAttr, strings.Join(snippets, "\n"))
		}
		out = append(out, f)
	}
	return out
}

func codesFor(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = Code(s)
	}
	return out
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// shiftsInto reports whether instance cur is being changed to look like
// next did before, on every known attribute that changes (tags aside). It
// returns the attributes that proved it.
func shiftsInto(c *Context, cur, next *model.Change) (bool, []string) {
	after, nextBefore := c.After(cur), c.Before(next)
	if after == nil || nextBefore == nil {
		return false, nil
	}
	var proved []string
	for _, l := range cur.Diff.Children {
		// Removed values are attributes the provider recomputes on
		// replacement; they prove nothing either way.
		if l.Kind == diff.Unchanged || l.Kind == diff.Removed || l.Unknown || l.Sensitive || l.Trivial || l.Key == "" {
			continue
		}
		switch l.Key {
		case "tags", "tags_all", "labels", "effective_labels", "terraform_labels":
			continue
		}
		if !diff.Equal(after[l.Key], nextBefore[l.Key]) {
			return false, nil
		}
		proved = append(proved, l.Key)
	}
	return true, proved
}

// countSiblings finds resources counted like the shifted ones that also
// lose their last instances, without a detectable shift of their own
// (their changes are usually unknown until apply).
func (c *Context) countSiblings(shifts []*shift) []string {
	refs := shifts[0].countRefs
	if refs == "" {
		return nil
	}
	module := moduleOfResource(shifts[0].resource)
	in := map[string]bool{}
	for _, s := range shifts {
		in[s.resource] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, ch := range c.Report.Changes {
		if in[ch.Resource] || seen[ch.Resource] || ch.Reason != "delete_because_count_index" {
			continue
		}
		if moduleOfResource(ch.Resource) == module && c.countRefs(ch.Resource) == refs {
			seen[ch.Resource] = true
			out = append(out, ch.Resource)
		}
	}
	sort.Strings(out)
	return out
}

func (c *Context) countRefs(res string) string {
	cr := c.configResource(res)
	if cr == nil || cr.CountExpression == nil {
		return ""
	}
	refs := append([]string(nil), cr.CountExpression.References...)
	sort.Strings(refs)
	return strings.Join(refs, ",")
}

// configResource finds the configuration block behind a resource address.
func (c *Context) configResource(res string) *plan.ConfigResource {
	if c.Plan.Configuration == nil {
		return nil
	}
	addr, err := plan.ParseAddress(res)
	if err != nil {
		return nil
	}
	mod := c.Plan.Configuration.RootModule
	for _, step := range addr.Module {
		if mod == nil || mod.ModuleCalls[step.Name] == nil {
			return nil
		}
		mod = mod.ModuleCalls[step.Name].Module
	}
	if mod == nil {
		return nil
	}
	for _, r := range mod.Resources {
		if r.Type == addr.Type && r.Name == addr.Name && r.Mode == addr.Mode {
			return r
		}
	}
	return nil
}

func moduleOfResource(res string) string {
	addr, err := plan.ParseAddress(res)
	if err != nil {
		return ""
	}
	return addr.ModuleConfigString()
}

// forEachMoves writes the moved blocks for switching from count to for_each
// keyed by attr, when attr's values tell the instances apart.
func forEachMoves(c *Context, instances []*model.Change, attr string) string {
	keys := map[int]string{}
	seen := map[string]bool{}
	for _, ch := range instances {
		i, _ := keyIndex(ch.Key)
		v, ok := c.Before(ch)[attr].(string)
		if !ok || v == "" || seen[v] {
			return ""
		}
		seen[v] = true
		keys[i] = v
	}
	// The removed items are the ones whose value no longer appears after the shift.
	survivors := map[string]bool{}
	for _, ch := range instances {
		if ch.Action == model.Delete {
			continue
		}
		if v, ok := c.After(ch)[attr].(string); ok {
			survivors[v] = true
		}
	}
	idx := make([]int, 0, len(keys))
	for i := range keys {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	base := strings.TrimSuffix(instances[0].Address, instances[0].Key)
	var b strings.Builder
	for _, i := range idx {
		if !survivors[keys[i]] {
			continue
		}
		fmt.Fprintf(&b, "moved {\n  from = %s[%d]\n  to   = %s[%q]\n}\n", base, i, base, keys[i])
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func keyIndex(key string) (int, bool) {
	if len(key) < 3 || key[0] != '[' || key[1] == '"' {
		return 0, false
	}
	n := 0
	for _, r := range key[1 : len(key)-1] {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}
