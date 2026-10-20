package analysis

import (
	"fmt"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/rules"
)

// volatile attributes change on their own; a difference in them is not drift
// anyone needs to hear about.
var volatile = map[string]bool{
	"latest_restorable_time": true, "last_modified": true, "last_modified_date": true,
	"last_updated_date": true, "last_update_status": true, "updated_at": true,
	"modified_date": true, "last_modified_time": true, "last_status_change": true,
}

// drift compares the state Terraform had with what it found on refresh, and
// works out whether the plan keeps or reverts each difference.
func (a *analyzer) drift() []*model.Drift {
	var out []*model.Drift
	for _, rc := range a.plan.ResourceDrift {
		action, _ := Classify(rc.Change.Actions)
		d := &model.Drift{Address: rc.Address, Action: action}
		planned := a.byAddr[rc.Address]
		if action == model.Delete {
			d.Summary = "Deleted outside " + a.info.ToolName() + "."
			if planned != nil && (planned.Action == model.Create || planned.Action == model.Replace) {
				d.Summary += " This plan creates it again."
			}
			out = append(out, d)
			if planned != nil {
				planned.Drifted = true
			}
			continue
		}
		n := diff.Compute(diff.Input{
			Before: rc.Change.Before, After: rc.Change.After,
			AfterUnknown:    rc.Change.AfterUnknown,
			BeforeSensitive: rc.Change.BeforeSensitive, AfterSensitive: rc.Change.AfterSensitive,
			HasBefore: true, HasAfter: true,
		})
		before, _ := rc.Change.Before.(map[string]any)
		after, _ := rc.Change.After.(map[string]any)
		real := realDrift(n.Leaves(), before, after)
		var plannedFull *diff.Node
		if planned != nil {
			plannedFull = a.full[planned]
		}
		configured := a.configured(rc.Address)
		var plannedAfter any
		if planned != nil && a.raw[planned] != nil {
			plannedAfter = a.raw[planned].Change.After
		}
		var shown []*diff.Node
		for _, l := range real {
			path := collapsePath(l)
			reverted, decided := elementReverted(l, rc.Change.Before, rc.Change.After, plannedAfter)
			switch {
			case planned == nil:
				shown = append(shown, l)
			case planned.Action == model.Delete || planned.Action == model.Replace:
				d.Reverted = append(d.Reverted, path)
				shown = append(shown, l)
			case decided && reverted:
				d.Reverted = append(d.Reverted, path)
				shown = append(shown, l)
			case decided:
				if configured[l.Root()] {
					d.Kept = append(d.Kept, path)
					shown = append(shown, l)
				}
			case plannedFull != nil && plannedFull.LookupBefore(l.Path()).Kind != diff.Unchanged:
				d.Reverted = append(d.Reverted, path)
				shown = append(shown, l)
			case configured[l.Root()]:
				d.Kept = append(d.Kept, path)
				shown = append(shown, l)
			default:
				// Kept, and not something the configuration sets: a computed
				// attribute catching up with reality (often because another
				// resource manages that part of the object).
			}
		}
		d.Noise = len(shown) == 0
		d.Reverted = sortedUnique(d.Reverted)
		d.Kept = sortedUnique(d.Kept)
		n.Prune(nil)
		d.Diff = n
		d.Summary = driftSummary(d, shown, planned, a.info.ToolName())
		if planned != nil && !d.Noise {
			planned.Drifted = true
		}
		out = append(out, d)
	}
	return out
}

// elementReverted decides drift inside a list element by value rather than
// by position, since positions differ between the old state, the refreshed
// object and the plan. An element added outside Terraform is reverted when
// the planned list no longer contains it; one removed outside is reverted
// when the plan puts it back. decided is false outside lists, or when the
// planned list is unknown.
func elementReverted(l *diff.Node, stateValue, realValue, plannedAfter any) (reverted, decided bool) {
	e := l.Element()
	if e == nil || plannedAfter == nil {
		return false, false
	}
	path := e.Path()
	listPath := path[:len(path)-1]
	list, ok := diff.Get(plannedAfter, listPath...)
	if !ok {
		return false, false
	}
	items, ok := list.([]any)
	if !ok {
		return false, false
	}
	// Removed elements are indexed into the old state, others into the
	// refreshed object; read the raw value rather than rebuilding it.
	source := realValue
	if e.Kind == diff.Removed {
		source = stateValue
	}
	value, ok := diff.Get(source, path...)
	if !ok || value == nil {
		return false, false
	}
	present := false
	for _, it := range items {
		if diff.Equal(it, value) {
			present = true
			break
		}
	}
	if e.Kind == diff.Removed {
		return present, true
	}
	return !present, true
}

// realDrift drops differences that are not changes to the real object:
// null versus empty values, attributes that change on their own, and tags
// the provider re-attributes when default_tags change.
func realDrift(leaves []*diff.Node, before, after map[string]any) []*diff.Node {
	var out []*diff.Node
	tagReal := map[string]bool{}
	for _, l := range leaves {
		root := l.Root()
		if l.Trivial || volatile[root] {
			continue
		}
		if root == "tags" && len(l.Path()) >= 2 {
			if _, hasAll := before["tags_all"]; hasAll {
				key, _ := l.Path()[1].(string)
				b, _ := diff.Get(before, "tags_all", key)
				a, _ := diff.Get(after, "tags_all", key)
				if diff.Equal(b, a) {
					continue
				}
				tagReal[key] = true
			}
		}
		out = append(out, l)
	}
	// tags.X and tags_all.X describe the same change; keep tags.X.
	kept := out[:0]
	for _, l := range out {
		if l.Root() == "tags_all" && len(l.Path()) >= 2 {
			if key, _ := l.Path()[1].(string); tagReal[key] {
				continue
			}
		}
		kept = append(kept, l)
	}
	return kept
}

// collapsePath names a difference by the list element it sits in, if any,
// so a changed rule reads as ingress[2] rather than nine attribute paths.
func collapsePath(l *diff.Node) string {
	if e := l.Element(); e != nil {
		return e.PathString()
	}
	return l.PathString()
}

func driftSummary(d *model.Drift, real []*diff.Node, planned *model.Change, tool string) string {
	if d.Noise {
		return "Provider-side differences only (default tags, timestamps, empty values); nothing to review."
	}
	parts := describeLeaves(real)
	s := "Changed outside " + tool + ": " + rules.List(parts, 4) + "."
	switch {
	case planned == nil:
	case planned.Action == model.Delete:
		s += " This plan destroys it anyway."
	case planned.Action == model.Replace:
		s += " This plan replaces it, so the outside changes are lost."
	case len(d.Reverted) > 0 && len(d.Kept) == 0:
		s += " This plan reverts " + pronoun(len(d.Reverted)) + "."
	case len(d.Reverted) > 0:
		s += fmt.Sprintf(" This plan reverts %s and keeps the rest.", rules.List(codesOf(d.Reverted), 3))
	default:
		s += " This plan keeps " + pronoun(len(d.Kept)) + "; consider adding the change to the configuration."
	}
	return s
}

func pronoun(n int) string {
	if n == 1 {
		return "it"
	}
	if n == 2 {
		return "both"
	}
	return "them"
}

func codesOf(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = rules.Code(s)
	}
	return out
}

// describeLeaves summarises differences: "`tags_all.Owner` added",
// "`ingress` gained 1 element". Changes inside list elements are counted per
// element rather than listed attribute by attribute.
func describeLeaves(leaves []*diff.Node) []string {
	type group struct {
		list     string
		kind     diff.Kind
		elements map[string]*diff.Node
	}
	var parts []string
	groups := map[string]*group{}
	var order []string
	for _, l := range leaves {
		path := l.Path()
		idx := -1
		for i, step := range path {
			if _, ok := step.(int); ok {
				idx = i
				break
			}
		}
		if idx < 0 {
			parts = append(parts, describeLeaf(l))
			continue
		}
		list := diff.PathString(path[:idx])
		element := diff.PathString(path[:idx+1])
		kind := l.Kind
		e := l.Element()
		if e != nil {
			kind = e.Kind
		}
		key := list + "|" + string(kind)
		g := groups[key]
		if g == nil {
			g = &group{list: list, kind: kind, elements: map[string]*diff.Node{}}
			groups[key] = g
			order = append(order, key)
		}
		g.elements[element] = e
	}
	for _, key := range order {
		g := groups[key]
		n := len(g.elements)
		var desc string
		if n == 1 {
			for _, e := range g.elements {
				desc = describeElement(e)
			}
		}
		switch {
		case g.kind == diff.Added && desc != "":
			parts = append(parts, rules.Code(g.list)+" gained "+desc)
		case g.kind == diff.Removed && desc != "":
			parts = append(parts, rules.Code(g.list)+" lost "+desc)
		case g.kind == diff.Added:
			parts = append(parts, rules.Code(g.list)+" gained "+rules.Plural(n, "element", "elements"))
		case g.kind == diff.Removed:
			parts = append(parts, rules.Code(g.list)+" lost "+rules.Plural(n, "element", "elements"))
		default:
			parts = append(parts, rules.Code(g.list)+" changed")
		}
	}
	return parts
}

// describeElement summarises an added or removed list element: a firewall
// rule reads as 8443/tcp from 0.0.0.0/0 ("partner callback").
func describeElement(e *diff.Node) string {
	if e == nil || (e.Kind != diff.Added && e.Kind != diff.Removed) {
		return ""
	}
	v, ok := plainValue(e, e.Kind == diff.Removed).(map[string]any)
	if !ok {
		return ""
	}
	desc, _ := v["description"].(string)
	quoteDesc := ""
	if desc != "" {
		quoteDesc = " (" + rules.Quote(desc) + ")"
	}
	if from, ok := v["from_port"]; ok {
		to := v["to_port"]
		ports := rules.Value(from)
		if !diff.Equal(from, to) {
			ports += "-" + rules.Value(to)
		}
		proto, _ := v["protocol"].(string)
		if proto == "-1" {
			proto, ports = "all", "all ports"
		}
		var sources []string
		for _, k := range []string{"cidr_blocks", "ipv6_cidr_blocks", "security_groups", "prefix_list_ids"} {
			if l, ok := v[k].([]any); ok {
				for _, x := range l {
					if s, ok := x.(string); ok {
						sources = append(sources, s)
					}
				}
			}
		}
		if self, _ := v["self"].(bool); self {
			sources = append(sources, "itself")
		}
		out := rules.Code(ports + "/" + proto)
		if proto == "all" {
			out = rules.Code("all traffic")
		}
		if len(sources) > 0 {
			out += " from " + rules.List(codesOf(sources), 3)
		}
		return out + quoteDesc
	}
	if name, ok := v["name"].(string); ok && name != "" {
		return rules.Code(name) + quoteDesc
	}
	if desc != "" {
		return rules.Quote(desc)
	}
	return ""
}

// plainValue rebuilds the value a one-sided subtree describes.
func plainValue(n *diff.Node, removed bool) any {
	if n.Sensitive {
		return nil
	}
	if len(n.Children) == 0 {
		if removed {
			return n.Before
		}
		return n.After
	}
	if n.Type == "list" {
		var out []any
		for _, c := range n.Children {
			out = append(out, plainValue(c, removed))
		}
		return out
	}
	out := map[string]any{}
	for _, c := range n.Children {
		out[c.Key] = plainValue(c, removed)
	}
	return out
}

// describeLeaf summarises one difference outside any list.
func describeLeaf(l *diff.Node) string {
	path := l.PathString()
	switch l.Kind {
	case diff.Added:
		return rules.Code(path) + " added"
	case diff.Removed:
		return rules.Code(path) + " removed"
	}
	if l.Sensitive || l.Unknown {
		return rules.Code(path) + " changed"
	}
	return fmt.Sprintf("%s %s → %s", rules.Code(path), rules.Value(l.Before), rules.Value(l.After))
}
