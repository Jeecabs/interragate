// Package delta compares two reports for the same configuration, such as
// the plans before and after an edit, and says what changed between them:
// the verdict, what happens to each resource, and the findings.
package delta

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
)

// Delta is what changed from one report to the next.
type Delta struct {
	// From and To are when the two reports were generated.
	From string `json:"from"`
	To   string `json:"to"`
	// RiskBefore and RiskAfter are the two verdicts.
	RiskBefore model.Severity `json:"risk_before"`
	RiskAfter  model.Severity `json:"risk_after"`
	// Changes lists resources whose planned change differs, worst first.
	Changes []Change `json:"changes,omitempty"`
	// Found holds findings only the later report has, and findings it
	// rates more severe than the earlier one did. Fixed holds findings
	// only the earlier report has.
	Found []Finding `json:"found,omitempty"`
	Fixed []Finding `json:"fixed,omitempty"`
	// Outputs are the outputs whose planned change differs.
	Outputs []Output `json:"outputs,omitempty"`
	// Sentences say all of the above in plain English, most important
	// first. Addresses are in backticks, as in the report's narrative.
	Sentences []model.Sentence `json:"sentences"`
}

// Change is a resource whose planned change differs between the reports:
// what the plan does to it, the order it replaces it in, or the values it
// sets. Before or After is empty when the resource did not change in that
// plan; they are the same when only the order or the values differ.
type Change struct {
	Address string       `json:"address"`
	Before  model.Action `json:"before,omitempty"`
	After   model.Action `json:"after,omitempty"`
	// Order is how the later plan replaces a resource both plans replace,
	// "create-first" or "delete-first", when the order changed.
	Order string `json:"order,omitempty"`
	// Attributes are the attributes whose planned values differ when the
	// action does not, such as input or tags.env.
	Attributes []string `json:"attributes,omitempty"`

	was, now *model.Change
	attrs    []attribute
}

// Finding identifies a finding by what it says, since IDs are positional.
type Finding struct {
	Rule     string         `json:"rule"`
	Severity model.Severity `json:"severity"`
	// Was is the severity the earlier report gave a finding that the later
	// one rates more severe.
	Was       model.Severity `json:"was,omitempty"`
	Title     string         `json:"title"`
	Text      string         `json:"text"`
	Addresses []string       `json:"addresses"`
}

// Output is an output whose planned change differs between the plans.
type Output struct {
	Name   string       `json:"name"`
	Before model.Action `json:"before,omitempty"` // no-op when it did not change
	After  model.Action `json:"after,omitempty"`
}

// Empty reports whether nothing that matters changed.
func (d *Delta) Empty() bool {
	return d.RiskBefore == d.RiskAfter && len(d.Changes) == 0 && len(d.Found) == 0 && len(d.Fixed) == 0 && len(d.Outputs) == 0
}

// compareOutputs lists the outputs the plans change differently: one that
// only one of them changes, or that both change to different values.
func compareOutputs(before, after []*model.Output) []Output {
	was := map[string]*model.Output{}
	for _, o := range before {
		was[o.Name] = o
	}
	var out []Output
	seen := map[string]bool{}
	for _, o := range after {
		seen[o.Name] = true
		w := was[o.Name]
		b := model.NoOp
		if w != nil {
			b = w.Action
		}
		switch {
		case b != o.Action:
			out = append(out, Output{Name: o.Name, Before: b, After: o.Action})
		case o.Action != model.NoOp && w != nil && !o.Sensitive && planned(w.Diff) != planned(o.Diff):
			out = append(out, Output{Name: o.Name, Before: b, After: o.Action})
		}
	}
	for _, w := range before {
		if !seen[w.Name] && w.Action != model.NoOp {
			out = append(out, Output{Name: w.Name, Before: w.Action, After: model.NoOp})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// maxSentences keeps the summary readable; the rest is counted.
const maxSentences = 8

// Compare works out what changed from before to after.
func Compare(before, after *model.Report) *Delta {
	d := &Delta{
		From:       before.GeneratedAt,
		To:         after.GeneratedAt,
		RiskBefore: before.Risk.Level,
		RiskAfter:  after.Risk.Level,
	}
	was, now := acting(before), acting(after)
	sev := map[string]model.Severity{}
	// Whether a change only follows from another, and from which, comes
	// from the report in which it happens: the later one, or the earlier
	// for one that went away.
	l := &lineage{follows: map[string]bool{}, causes: map[string][]string{}}
	for _, r := range []*model.Report{before, after} {
		for _, ch := range r.Changes {
			if ch.Action == model.NoOp {
				continue
			}
			l.follows[ch.Address] = ch.Indirect
			l.causes[ch.Address] = nil
			for _, c := range ch.Causes {
				l.causes[ch.Address] = append(l.causes[ch.Address], c.Address)
			}
		}
	}
	for _, ch := range after.Changes {
		sev[ch.Address] = ch.Severity
	}
	for _, ch := range before.Changes {
		if _, ok := sev[ch.Address]; !ok {
			sev[ch.Address] = ch.Severity
		}
	}
	// A moved block added since the earlier plan turns its change to the
	// old address into one at the new: the same resource, now moved.
	for addr, ch := range now {
		if ch.Moved && ch.PrevAddress != "" && was[addr] == nil && was[ch.PrevAddress] != nil && now[ch.PrevAddress] == nil {
			was[addr] = was[ch.PrevAddress]
			delete(was, ch.PrevAddress)
		}
	}
	for addr := range union(was, now) {
		if c, ok := compare(addr, was[addr], now[addr]); ok {
			d.Changes = append(d.Changes, c)
		}
	}
	// What got more dangerous comes first, then what got safer, then the
	// rest; within each, the most disruptive action first.
	sort.Slice(d.Changes, func(i, j int) bool {
		ci, cj := class(d.Changes[i]), class(d.Changes[j])
		if ci != cj {
			return ci < cj
		}
		ri, rj := rank(d.Changes[i]), rank(d.Changes[j])
		if ri != rj {
			return ri > rj
		}
		return d.Changes[i].Address < d.Changes[j].Address
	})
	d.Found, d.Fixed = compareFindings(before.Findings, after.Findings)
	d.Outputs = compareOutputs(before.Outputs, after.Outputs)
	d.Sentences = sentences(d, sev, l)
	return d
}

// compare says how two plans' changes to one resource differ, if they do:
// what the plan does to it (its action, and whether it moves or imports
// it), and otherwise the order of a replacement and the values it sets.
func compare(addr string, was, now *model.Change) (Change, bool) {
	c := Change{Address: addr, was: was, now: now}
	if was != nil {
		c.Before = was.Action
	}
	if now != nil {
		c.After = now.Action
	}
	if c.acts() {
		return c, true
	}
	// A destroy is a destroy: what the object held before it goes is the
	// state's business, not the plan's.
	if now.Action == model.Delete {
		return c, false
	}
	if now.Action == model.Replace && now.Order != was.Order {
		c.Order = now.Order
	}
	c.attrs = attributes(was.Diff, now.Diff)
	for _, a := range c.attrs {
		c.Attributes = append(c.Attributes, a.path)
	}
	return c, c.Order != "" || len(c.attrs) > 0
}

// acts reports whether what the plan does to the resource changed, rather
// than only how it does it.
func (c Change) acts() bool {
	if c.was == nil || c.now == nil {
		return true
	}
	return c.was.Action != c.now.Action || c.was.Moved != c.now.Moved || c.was.Importing != c.now.Importing
}

// class sorts a change into worse (0: it now destroys or replaces what it
// did not, or destroys before it creates), better (1: it no longer does)
// or neither (2).
func class(c Change) int {
	harmful := func(a model.Action) bool { return a == model.Delete || a == model.Replace }
	switch {
	case harmful(c.After) && c.After.Rank() > c.Before.Rank():
		return 0
	case harmful(c.Before) && c.After.Rank() < c.Before.Rank():
		return 1
	case c.Order == "delete-first":
		return 0
	case c.Order == "create-first":
		return 1
	}
	return 2
}

func rank(c Change) int {
	return max(c.After.Rank(), c.Before.Rank())*10 + min(c.After.Rank(), c.Before.Rank())
}

// acting maps each address the plan does something to onto its change.
// Moves and imports count, since they are things the plan does.
func acting(r *model.Report) map[string]*model.Change {
	out := map[string]*model.Change{}
	for _, ch := range r.Changes {
		if ch.Action == model.NoOp && !ch.Moved && !ch.Importing {
			continue
		}
		out[ch.Address] = ch
	}
	return out
}

func union(a, b map[string]*model.Change) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// attribute is somewhere two plans set different values: what the later
// plan does there, or nil when only the earlier one changes it.
type attribute struct {
	path string
	now  *diff.Node
}

// attributes compares what two plans set on one resource, by their diffs.
func attributes(was, now *diff.Node) []attribute {
	a, b := map[string]*diff.Node{}, map[string]*diff.Node{}
	collect(was, nil, a)
	collect(now, nil, b)
	var out []attribute
	for path, n := range b {
		if o, ok := a[path]; !ok || planned(o) != planned(n) {
			out = append(out, attribute{path, n})
		}
	}
	for path := range a {
		if _, ok := b[path]; !ok {
			out = append(out, attribute{path, nil})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// collect maps the places a diff changes onto their nodes: each attribute,
// with objects such as tags opened up, so tags.env is one place. Lists,
// multi-line strings and JSON documents stay whole.
func collect(n *diff.Node, path []any, out map[string]*diff.Node) {
	if n == nil {
		return
	}
	for _, c := range n.Children {
		if c.Kind == diff.Unchanged {
			continue
		}
		p := append(path[:len(path):len(path)], c.Key)
		if c.Type == "object" && c.Encoding == "" && c.Kind != diff.Removed && !c.Sensitive && !c.Unknown && len(c.Children) > 0 {
			collect(c, p, out)
			continue
		}
		out[diff.PathString(p)] = c
	}
}

// planned renders what a plan sets somewhere and leaves out what was there
// before, so two plans that make the same thing compare equal even when the
// state they start from differs. List elements keep both sides, since what
// an element was is how it is told apart from the others. Sensitive values
// are masked in the report, so two of them always compare equal.
func planned(n *diff.Node) string {
	b, _ := json.Marshal(plannedValue(n))
	return string(b)
}

func plannedValue(n *diff.Node) any {
	switch {
	case n.Kind == diff.Removed:
		return nil
	case n.Sensitive:
		return map[string]bool{"sensitive": true}
	case n.Unknown:
		return map[string]bool{"unknown": true}
	case n.Lines != nil:
		var kept []diff.Line
		for _, l := range n.Lines {
			if l.Op != "-" {
				kept = append(kept, l)
			}
		}
		return map[string]any{"lines": kept}
	case len(n.Children) == 0:
		return map[string]any{"value": n.After}
	case n.Type == "list":
		return map[string]any{"elements": n.Children, "hidden": n.Hidden}
	}
	m := map[string]any{"hidden": n.Hidden}
	for _, c := range n.Children {
		m["."+c.Key] = plannedValue(c)
	}
	return m
}

// compareFindings pairs the findings of two reports that are the same
// finding: from the same rule, about the same resources, with the same
// title. The first pass needs the same first sentence too; the second only
// the same things named in it (its code spans), so a finding that counts
// differently ("14 resources depend on it") or names the other tool stays
// the same one, while another port, statement or record is a new finding.
// Found holds the later report's unpaired findings and those whose
// severity rose; Fixed holds the earlier report's unpaired findings.
func compareFindings(before, after []*model.Finding) (found, fixed []Finding) {
	was, now := describe(before), describe(after)
	pair := make([]int, len(now))
	for i := range pair {
		pair[i] = -1
	}
	used := make([]bool, len(was))
	for _, key := range []func(Finding) string{Finding.key, Finding.looseKey} {
		free := map[string][]int{}
		for i, f := range was {
			if !used[i] {
				free[key(f)] = append(free[key(f)], i)
			}
		}
		for j, f := range now {
			if pair[j] >= 0 {
				continue
			}
			if q := free[key(f)]; len(q) > 0 {
				pair[j], used[q[0]] = q[0], true
				free[key(f)] = q[1:]
			}
		}
	}
	for j, f := range now {
		switch i := pair[j]; {
		case i < 0:
			found = append(found, f)
		case f.Severity > was[i].Severity:
			f.Was = was[i].Severity
			found = append(found, f)
		}
	}
	for i, f := range was {
		if !used[i] {
			fixed = append(fixed, f)
		}
	}
	sortFindings(found)
	sortFindings(fixed)
	return found, fixed
}

func describe(fs []*model.Finding) []Finding {
	out := make([]Finding, len(fs))
	for i, f := range fs {
		addrs := append([]string(nil), f.Addresses...)
		sort.Strings(addrs)
		// The message's first sentence stands alone and names the
		// resource, as the report's narrative does.
		text := firstSentence(f.Message)
		if text == "" {
			text = f.Title
		}
		out[i] = Finding{Rule: f.Rule, Severity: f.Severity, Title: f.Title, Text: text, Addresses: addrs}
	}
	return out
}

func (f Finding) key() string {
	return f.Rule + "\x00" + strings.Join(f.Addresses, ",") + "\x00" + toolNames.Replace(f.Title) + "\x00" + toolNames.Replace(f.Text)
}

func (f Finding) looseKey() string {
	return f.Rule + "\x00" + strings.Join(f.Addresses, ",") + "\x00" + toolNames.Replace(f.Title) + "\x00" + spans(f.Text)
}

// toolNames leaves out the name of the tool that made a plan ("changed
// outside OpenTofu"): a finding is the same whichever tool planned it.
var toolNames = strings.NewReplacer("OpenTofu", "\x01", "Terraform", "\x01")

// spans lists what a sentence names in backticks (addresses, attributes,
// values), without the words around them.
func spans(s string) string {
	parts := strings.Split(s, "`")
	var out []string
	for i := 1; i < len(parts)-1; i += 2 {
		out = append(out, parts[i])
	}
	return strings.Join(out, "\x00")
}

func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Severity != fs[j].Severity {
			return fs[i].Severity > fs[j].Severity
		}
		if fs[i].Rule != fs[j].Rule {
			return fs[i].Rule < fs[j].Rule
		}
		if a, b := strings.Join(fs[i].Addresses, ","), strings.Join(fs[j].Addresses, ","); a != b {
			return a < b
		}
		return fs[i].Text < fs[j].Text
	})
}

// verdict names a risk level in the words of the report's verdict, as the
// terminal prints it: "Critical risk", "Low risk", "No changes".
func verdict(s model.Severity) string {
	switch s {
	case model.None:
		return "No changes"
	case model.Info:
		return "Low risk"
	}
	name := s.String()
	return strings.ToUpper(name[:1]) + name[1:] + " risk"
}

// verb is how a sentence names an action: "replaces", "updates".
func verb(a model.Action) string {
	switch a {
	case model.Create:
		return "creates"
	case model.Update:
		return "updates"
	case model.Replace:
		return "replaces"
	case model.Delete:
		return "destroys"
	case model.Read:
		return "reads"
	case model.Forget:
		return "forgets"
	}
	return string(a) + "s"
}

// participle is the action as a past participle: "replaced".
func participle(a model.Action) string {
	switch a {
	case model.Create:
		return "created"
	case model.Update:
		return "updated"
	case model.Replace:
		return "replaced"
	case model.Delete:
		return "destroyed"
	case model.Read:
		return "read"
	case model.Forget:
		return "forgotten"
	}
	return string(a)
}

// op is one thing a plan does to a resource, as a verb and a participle.
type op struct{ verb, participle string }

// ops lists what a plan does to a resource: moving or importing it, then
// its action. An import with no other change is only an import.
func ops(ch *model.Change) []op {
	var out []op
	if ch.Moved {
		out = append(out, op{"moves", "moved"})
	}
	if ch.Importing {
		out = append(out, op{"imports", "imported"})
	}
	if ch.Action != model.NoOp && !(ch.Action == model.Create && ch.Importing) {
		out = append(out, op{verb(ch.Action), participle(ch.Action)})
	}
	return out
}

// compareOps splits what two plans do to a resource into what only the
// later one does, what only the earlier one did, and what both do.
func compareOps(was, now []op) (added, removed, common []string) {
	in := func(o op, list []op) bool {
		for _, x := range list {
			if x == o {
				return true
			}
		}
		return false
	}
	for _, o := range now {
		if in(o, was) {
			common = append(common, o.participle)
		} else {
			added = append(added, o.participle)
		}
	}
	for _, o := range was {
		if !in(o, now) {
			removed = append(removed, o.participle)
		}
	}
	return added, removed, common
}

func verbs(ops []op) []string {
	out := make([]string, len(ops))
	for i, o := range ops {
		out[i] = o.verb
	}
	return out
}

// sentences says what changed. Each carries a severity for its marker:
// bad news takes the severity of what it introduces, good news (a finding
// fixed, a destroy avoided or put after its replacement, the verdict
// falling) takes None, and the rest Info.
func sentences(d *Delta, sev map[string]model.Severity, l *lineage) []model.Sentence {
	var out []model.Sentence
	// Info and Low both read "Low risk": the verdict did not change.
	if was, now := verdict(d.RiskBefore), verdict(d.RiskAfter); was != now {
		s := model.Sentence{Severity: d.RiskAfter, Text: fmt.Sprintf("The verdict rose from %s to %s.", was, now)}
		if d.RiskAfter < d.RiskBefore {
			s = model.Sentence{Severity: model.None, Text: fmt.Sprintf("The verdict fell from %s to %s.", was, now)}
		}
		out = append(out, s)
	}
	// Findings come before resource changes: they are why a change matters.
	for _, f := range d.Found {
		text := "New: " + period(f.Text)
		if f.Was != model.None {
			text = fmt.Sprintf("Now %s, not %s: %s", f.Severity, f.Was, period(f.Text))
		}
		out = append(out, model.Sentence{Severity: f.Severity, Text: text, Refs: f.Addresses})
	}
	for _, f := range d.Fixed {
		out = append(out, model.Sentence{Severity: model.None, Text: "Fixed: " + period(f.Text), Refs: f.Addresses})
	}
	// Changes that went the same way are told together, causes first: one
	// edit often replaces a resource and everything that reads it.
	for _, g := range groups(d.Changes, l) {
		level := model.Info
		switch g.class {
		case 0:
			for _, x := range g.changes {
				level = max(level, sev[x.Address], model.Low)
			}
		case 1:
			level = model.None
		}
		var refs []string
		for _, x := range g.changes {
			refs = append(refs, x.Address)
		}
		out = append(out, model.Sentence{Severity: level, Text: g.text(l), Refs: refs})
	}
	for _, o := range d.Outputs {
		var text string
		switch {
		case o.After == model.NoOp:
			text = fmt.Sprintf("No longer changes output `%s`.", o.Name)
		case o.Before == model.NoOp:
			text = fmt.Sprintf("Now changes output `%s`.", o.Name)
		case o.Before == o.After:
			text = fmt.Sprintf("Now sets output `%s` differently.", o.Name)
		default:
			text = fmt.Sprintf("Output `%s` is now %s, not %s.", o.Name, participle(o.After), participle(o.Before))
		}
		out = append(out, model.Sentence{Severity: model.Info, Text: text})
	}
	if len(out) > maxSentences {
		rest := len(out) - (maxSentences - 1)
		out = append(out[:maxSentences-1], model.Sentence{Severity: model.Info, Text: fmt.Sprintf("And %d more differences.", rest)})
	}
	if len(out) == 0 {
		out = append(out, model.Sentence{Severity: model.None, Text: "The plan is the same as before."})
	}
	return out
}

// firstSentence ends at the first full stop outside backticks that ends a
// sentence, so addresses and versions with dots stay whole.
func firstSentence(s string) string {
	inCode := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '`':
			inCode = !inCode
		case '.':
			if !inCode && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\n') {
				return s[:i+1]
			}
		}
	}
	return strings.TrimSpace(s)
}

// What a sentence about a group of changes says: what the plan does to
// them, the order it replaces them in, or the values it sets.
const (
	actionSentence = iota
	orderSentence
	valueSentence
)

// group is changes told in one sentence, because they changed the same way.
type group struct {
	kind    int
	class   int
	changes []Change
}

// groups gathers the changes told in each sentence, keeping the order of
// each group's first member: changes whose action changed the same way,
// then those replaced in a new order, then those that set the same new
// values. Worse news comes before better news, as for the changes. Within
// a group, the changes that cause others come first.
func groups(changes []Change, l *lineage) []*group {
	index := map[string]*group{}
	var out []*group
	add := func(kind int, key string, c Change) {
		key = fmt.Sprint(kind, "\x00", key)
		g := index[key]
		if g == nil {
			g = &group{kind: kind, class: 2}
			switch kind {
			case actionSentence, orderSentence:
				g.class = class(c)
			}
			index[key] = g
			out = append(out, g)
		}
		g.changes = append(g.changes, c)
	}
	for _, c := range changes {
		if c.acts() {
			add(actionSentence, actionKey(c), c)
			continue
		}
		if c.Order != "" {
			add(orderSentence, c.Order, c)
		}
		if len(c.attrs) > 0 {
			sets, unsets := settings(c.attrs)
			add(valueSentence, strings.Join(sets, "\x00")+"\x00\x00"+strings.Join(unsets, "\x00"), c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].class != out[j].class {
			return out[i].class < out[j].class
		}
		return out[i].kind < out[j].kind
	})
	for _, g := range out {
		if g.kind == actionSentence {
			cs := g.changes
			sort.SliceStable(cs, func(i, j int) bool { return !l.follows[cs[i].Address] && l.follows[cs[j].Address] })
		}
	}
	return out
}

// actionKey is what a change's action sentence says, without its subject.
func actionKey(c Change) string {
	var was, now []op
	if c.was != nil {
		was = ops(c.was)
	}
	if c.now != nil {
		now = ops(c.now)
	}
	return fmt.Sprint(c.was == nil, c.now == nil, was, now)
}

// text says what changed for the group's resources.
func (g *group) text(l *lineage) string {
	c := g.changes[0]
	plural := len(g.changes) > 1
	switch g.kind {
	case orderSentence:
		return orderText(names(g.changes, nil), plural, c.Order)
	case valueSentence:
		sets, unsets := settings(c.attrs)
		return valueText(names(g.changes, nil), plural, sets, unsets)
	}
	subject := names(g.changes, l)
	switch {
	case c.was == nil:
		return fmt.Sprintf("Now %s %s.", and(verbs(ops(c.now))), subject)
	case c.now == nil:
		return fmt.Sprintf("No longer %s %s.", and(verbs(ops(c.was))), subject)
	}
	be := "is"
	if plural {
		be = "are"
	}
	// A move or import both plans make is only worth saying when it is all
	// one of them does: "is now updated as well as moved".
	added, removed, common := compareOps(ops(c.was), ops(c.now))
	switch {
	case len(removed) == 0:
		return fmt.Sprintf("%s %s now %s as well as %s.", capitalizeFirst(subject), be, and(added), and(common))
	case len(added) == 0:
		return fmt.Sprintf("%s %s no longer %s, only %s.", capitalizeFirst(subject), be, and(removed), and(common))
	}
	return fmt.Sprintf("%s %s now %s, not %s.", capitalizeFirst(subject), be, and(added), and(removed))
}

// orderText says which way round the later plan replaces the resources.
func orderText(subject string, plural bool, order string) string {
	switch {
	case order == "create-first" && plural:
		return fmt.Sprintf("Now creates the replacements for %s before destroying the old ones.", subject)
	case order == "create-first":
		return fmt.Sprintf("Now creates the replacement for %s before destroying the old one.", subject)
	case order == "delete-first" && plural:
		return fmt.Sprintf("Now destroys %s before creating their replacements.", subject)
	case order == "delete-first":
		return fmt.Sprintf("Now destroys %s before creating its replacement.", subject)
	case plural:
		return fmt.Sprintf("%s are now replaced in a different order.", capitalizeFirst(subject))
	}
	return fmt.Sprintf("%s is now replaced in a different order.", capitalizeFirst(subject))
}

// maxValue is the most of a value a sentence quotes.
const maxValue = 40

// settings says what the later plan sets where the plans differ, as
// "`input` to "v3"", and names the places only the earlier plan changed.
// Values are the report's own, so what it masks stays masked.
func settings(attrs []attribute) (sets, unsets []string) {
	for _, a := range attrs {
		code := "`" + strings.ReplaceAll(a.path, "`", "'") + "`"
		n := a.now
		switch {
		case n == nil:
			unsets = append(unsets, code)
		case n.Kind == diff.Removed:
			sets = append(sets, code+" to null")
		case n.Sensitive:
			sets = append(sets, code+" to "+diff.SensitivePlaceholder)
		case n.Unknown:
			sets = append(sets, code+" to "+diff.UnknownPlaceholder)
		case len(n.Children) == 0 && n.Lines == nil:
			sets = append(sets, code+" to "+value(n.After))
		default:
			sets = append(sets, code+" differently")
		}
	}
	return sets, unsets
}

// value renders a planned value for a sentence, cut short; a string keeps
// its quotes. Backticks would break the narrative's markup, so they become
// quotes too.
func value(v any) string {
	if s, ok := v.(string); ok && utf8.RuneCountInString(s) > maxValue {
		v = string([]rune(s)[:maxValue-1]) + "…"
	}
	s := diff.String(v)
	if r := []rune(s); len(r) > maxValue+2 {
		s = string(r[:maxValue-1]) + "…"
	}
	return strings.ReplaceAll(s, "`", "'")
}

// valueText says what the later plan sets differently: "`x` now sets
// `input` to "v3" and no longer changes `size`."
func valueText(subject string, plural bool, sets, unsets []string) string {
	verb, stop := "sets", "no longer changes"
	if plural {
		verb, stop = "set", "no longer change"
	}
	switch {
	case len(unsets) == 0:
		return fmt.Sprintf("%s now %s %s.", capitalizeFirst(subject), verb, list(sets, 3))
	case len(sets) == 0:
		return fmt.Sprintf("%s %s %s.", capitalizeFirst(subject), stop, list(unsets, 3))
	}
	sep := " and "
	if len(sets) > 1 {
		sep = ", and "
	}
	return fmt.Sprintf("%s now %s %s%s%s %s.", capitalizeFirst(subject), verb, list(sets, 3), sep, stop, list(unsets, 2))
}

// lineage says which changes only follow from others, and from which, as
// the report in which each happens has it.
type lineage struct {
	follows map[string]bool
	causes  map[string][]string
}

// from reports whether the change at addr follows from the one at cause,
// directly or through others.
func (l *lineage) from(addr, cause string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(a string) bool {
		if seen[a] {
			return false
		}
		seen[a] = true
		for _, up := range l.causes[a] {
			if up == cause || walk(up) {
				return true
			}
		}
		return false
	}
	return walk(addr)
}

// explains reports whether each of rest follows from one of causes.
func (l *lineage) explains(causes, rest []string) bool {
	for _, r := range rest {
		found := false
		for _, c := range causes {
			if l.from(r, c) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// names lists a group's addresses for a sentence. When one or two changes
// cause all the rest, it names those and counts what follows from them;
// otherwise it names up to three and counts the rest. Without a lineage it
// only lists.
func names(g []Change, l *lineage) string {
	var causes, rest []string
	for _, c := range g {
		if l != nil && l.follows[c.Address] {
			rest = append(rest, c.Address)
		} else {
			causes = append(causes, c.Address)
		}
	}
	if n := len(causes); n > 0 && n <= 2 && len(rest) > 1 && l.explains(causes, rest) {
		pronoun := "it"
		if n == 2 {
			pronoun = "them"
		}
		return fmt.Sprintf("%s and %d more that follow from %s", strings.Join(quote(causes), ", "), len(rest), pronoun)
	}
	all := quote(append(causes, rest...))
	if len(all) > 3 {
		return fmt.Sprintf("%s and %d more", strings.Join(all[:2], ", "), len(all)-2)
	}
	return and(all)
}

// and joins items as "a, b and c".
func and(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// list joins up to n items as "a, b and c" and counts the rest.
func list(items []string, n int) string {
	if len(items) > n {
		return strings.Join(items[:n], ", ") + fmt.Sprintf(" and %d more", len(items)-n)
	}
	return and(items)
}

func quote(addrs []string) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = "`" + a + "`"
	}
	return out
}

func capitalizeFirst(s string) string {
	if s == "" || s[0] == '`' {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func period(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}
