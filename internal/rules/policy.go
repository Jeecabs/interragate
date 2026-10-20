package rules

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

// Policies turns the policies in a configuration into rules, in the order
// they are written. Run evaluates them after interragate's own rules, so
// their findings take the same severity overrides, ignores and output.
func Policies(cfg *config.Config) []*Rule {
	if cfg == nil {
		return nil
	}
	var out []*Rule
	for _, p := range cfg.Policies {
		// config.Load rejects invalid policies; one built in code without
		// validation, or that takes a built-in rule's ID, is left out
		// rather than half applied.
		if p.Validate() != nil || Lookup(p.ID) != nil {
			continue
		}
		pol := compile(p)
		sev, _ := model.ParseSeverity(p.Severity)
		out = append(out, &Rule{ID: p.ID, Title: pol.title, Severity: sev, Category: Policy, Doc: pol.doc(), Check: pol.check})
	}
	return out
}

// policy is a policy from .interragate.yaml, ready to evaluate.
type policy struct {
	title, message, remediation string
	match                       config.Selector
	providers, categories       []string // lowercased, as interragate writes them
	actions                     map[model.Action]bool
	when                        []condition
}

// condition is one of a policy's conditions, ready to evaluate.
type condition struct {
	attribute, op string
	path          []diff.Step
	value         any // the operand, in the types plan JSON decodes to
	re            *regexp.Regexp
}

// compile prepares a policy that has passed Validate, so its actions,
// paths and expressions all parse.
func compile(p config.Policy) *policy {
	pol := &policy{title: oneLine(p.Title), message: oneLine(p.Message), remediation: oneLine(p.Remediation),
		match: p.Match, actions: map[model.Action]bool{}}
	for _, name := range p.Match.Action {
		a, _ := config.ParseAction(name)
		pol.actions[a] = true
	}
	for _, name := range p.Match.Provider {
		pol.providers = append(pol.providers, strings.ToLower(name))
	}
	for _, c := range p.Match.Category {
		pol.categories = append(pol.categories, strings.ToLower(c))
	}
	for _, c := range p.When {
		path, _ := config.ParsePath(c.Attribute)
		cond := condition{attribute: c.Attribute, op: c.Operator, path: path, value: planValue(c.Value)}
		if c.Operator == "matches" {
			cond.re = regexp.MustCompile(c.Value.(string))
		}
		pol.when = append(pol.when, cond)
	}
	return pol
}

func (p *policy) check(c *Context) []*model.Finding {
	var out []*model.Finding
	if len(p.match.Origin) > 0 && !Traced(c.Report) {
		return nil // nothing is known about where changes come from, as for untied-destroy
	}
	for _, ch := range c.Report.Changes {
		// Addresses match as Terraform writes them, without the label
		// interragate gives a deposed object.
		addr, change := ch.Address, plan.Change{}
		if rc := c.Raw(ch); rc != nil {
			addr, change = rc.Address, rc.Change
		}
		if !p.selects(ch, addr) {
			continue
		}
		met, paths, ok := p.meets(change, ch.Action)
		if !ok {
			continue
		}
		f := finding(ch, p.title, p.sentence(ch, met))
		f.Short = "breaks the policy " + p.quoted()
		f.Paths = paths
		f.Remediation = p.remediation
		out = append(out, f)
	}
	return out
}

// selects reports whether the policy's match covers a change.
func (p *policy) selects(ch *model.Change, addr string) bool {
	m := p.match
	if len(p.actions) > 0 {
		if !p.actions[ch.Action] {
			return false
		}
	} else if !leavesObject(ch) {
		return false
	}
	return matchAny(m.Type, ch.Type) && matchAny(m.Address, addr) && p.inModule(addr) &&
		matchAny(p.providers, strings.ToLower(ch.Provider)) && matchAny(p.categories, ch.Category) &&
		(m.Stateful == nil || *m.Stateful == ch.Stateful) && (len(m.Origin) == 0 || slices.Contains(m.Origin, originOf(ch)))
}

// originOf names where a change comes from as match.origin does.
func originOf(ch *model.Change) string {
	if ch.Origin == "" {
		return "untied"
	}
	return ch.Origin
}

// leavesObject reports whether a change leaves an object in place for a
// policy's conditions to describe, which is what a policy without action
// covers. A destroy removes whatever broke the policy, and an unchanged
// resource is not news, as with interragate's own rules.
func leavesObject(ch *model.Change) bool {
	switch ch.Action {
	case model.Create, model.Update, model.Replace:
		return true
	case model.NoOp:
		return ch.Importing
	}
	return false
}

// inModule reports whether a resource is in a module the policy names, or
// in one that module calls. Module paths match with their instance keys,
// as in module.app["blue"], and without them, so module.app covers every
// instance of a module called with count or for_each. The root module is
// no module.
func (p *policy) inModule(addr string) bool {
	if len(p.match.Module) == 0 {
		return true
	}
	a, err := plan.ParseAddress(addr)
	if err != nil {
		return false
	}
	for i := range a.Module {
		within := plan.Address{Module: a.Module[:i+1]}
		if matchAny(p.match.Module, within.ModuleString()) || matchAny(p.match.Module, within.ModuleConfigString()) {
			return true
		}
	}
	return false
}

// matchAny reports whether s matches one of the patterns, or there are none.
func matchAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if config.Match(p, s) {
			return true
		}
	}
	return len(patterns) == 0
}

// meets checks every condition against a change. For the message, it says
// how each one was met and where.
func (p *policy) meets(change plan.Change, action model.Action) (met, paths []string, ok bool) {
	for _, cond := range p.when {
		s, held := cond.test(change, action)
		if !held {
			return nil, nil, false
		}
		met = append(met, cond.describe(s))
		if at := s.At(cond.attribute); !slices.Contains(paths, at) {
			paths = append(paths, at)
		}
	}
	return met, paths, true
}

// sentence names the resource, what the plan does to it and the policy
// that forbids it, then how the conditions were met and the policy's own
// message.
func (p *policy) sentence(ch *model.Change, met []string) string {
	msg := fmt.Sprintf("%s %s, which the policy %s does not allow", Code(ch.Address), done(ch), p.quoted())
	if ch.Action == model.NoOp && !ch.Moved && !ch.Importing {
		msg = fmt.Sprintf("%s does not change in this plan, but the policy %s does not allow it", Code(ch.Address), p.quoted())
	}
	if len(met) > 0 {
		msg += ": " + List(met, len(met))
	}
	msg += "."
	if p.message != "" {
		msg += " " + p.message
	}
	return msg
}

func (p *policy) quoted() string { return `"` + strings.TrimSuffix(Plain(p.title), ".") + `"` }

// done says what the plan does to a resource, to follow its address: "is
// replaced".
func done(ch *model.Change) string {
	verb := map[model.Action]string{
		model.Create: "created", model.Update: "updated", model.Replace: "replaced", model.Delete: "destroyed",
		model.Read: "read", model.Forget: "removed from the state without being destroyed",
	}[ch.Action]
	switch {
	case ch.Importing && (ch.Action == model.Create || ch.Action == model.NoOp):
		return "is imported"
	case ch.Importing && verb != "":
		return "is imported and " + verb
	case ch.Moved && ch.PrevAddress != "" && verb == "":
		return "is moved from " + Code(ch.PrevAddress)
	case ch.Moved && ch.PrevAddress != "":
		return "is moved from " + Code(ch.PrevAddress) + " and " + verb
	case verb == "":
		return "changes"
	}
	return "is " + verb
}

// gerunds name what a policy's actions do, for its description.
var gerunds = map[model.Action]string{
	model.Create: "creating", model.Update: "updating", model.Replace: "replacing", model.Delete: "destroying",
	model.Read: "reading", model.Forget: "forgetting", model.NoOp: "leaving unchanged",
}

// doc describes what the policy flags, for interragate rules.
func (p *policy) doc() string {
	m := p.match
	verbs := []string{"changes to"}
	if len(m.Action) > 0 {
		verbs = nil
		for _, name := range m.Action {
			a, _ := config.ParseAction(name)
			if v := gerunds[a]; !slices.Contains(verbs, v) {
				verbs = append(verbs, v)
			}
		}
	}
	what := "resources"
	switch {
	case len(m.Type) > 0:
		what = either(codesFor(m.Type))
	case len(p.categories) > 0:
		what = either(p.categories) + " resources"
	}
	parts := []string{"Flags", either(verbs), what}
	if len(m.Type) > 0 && len(p.categories) > 0 {
		parts = append(parts, "in the", either(p.categories), "category")
	}
	if m.Stateful != nil {
		parts = append(parts, map[bool]string{true: "that hold data", false: "that hold no data"}[*m.Stateful])
	}
	if len(m.Origin) > 0 {
		var where []string
		for _, o := range m.Origin {
			where = append(where, map[string]string{"edit": "your edits make", "follows": "follow from your edits", "untied": "no edit explains"}[o])
		}
		parts = append(parts, "that", either(where))
	}
	if len(m.Address) > 0 {
		parts = append(parts, "at", either(codesFor(m.Address)))
	}
	if len(m.Module) > 0 {
		parts = append(parts, "in", either(codesFor(m.Module)))
	}
	if len(m.Provider) > 0 {
		parts = append(parts, "from the", either(codesFor(m.Provider)), "provider")
	}
	if len(p.when) > 0 {
		var conds []string
		for _, c := range p.when {
			conds = append(conds, c.describe(diff.Selection{}))
		}
		parts = append(parts, "when", List(conds, len(conds)))
	}
	doc := strings.Join(parts, " ") + "."
	if p.message != "" {
		doc += " " + p.message
	}
	return doc
}

// test checks the condition against a change and returns the value that
// meets it. Values come from the planned object or, for a destroy, from
// the object as it is now.
func (c *condition) test(change plan.Change, action model.Action) (diff.Selection, bool) {
	if c.op == "changes" {
		before := diff.Select(change.Before, nil, change.BeforeSensitive, c.path)
		after := diff.Select(change.After, change.AfterUnknown, change.AfterSensitive, c.path)
		s, changed := diff.Differs(before, after)
		return s, changed == c.value.(bool)
	}
	v, unk, sens := change.After, change.AfterUnknown, change.AfterSensitive
	if action == model.Delete || action == model.Forget {
		v, unk, sens = change.Before, nil, change.BeforeSensitive
	}
	for _, s := range diff.Select(v, unk, sens, c.path) {
		if c.holds(s) {
			return s, true
		}
	}
	return diff.Selection{}, false
}

// holds reports whether one diff.Selection value meets the condition. Values
// known only after apply and sensitive values are never compared: only
// present and changes can see them, so a policy cannot probe a secret.
func (c *condition) holds(s diff.Selection) bool {
	switch c.op {
	case "present":
		return (s.IsUnknown() || s.Value != nil) == c.value.(bool)
	case "contains":
		return contains(s, c.value)
	}
	if s.Opaque() {
		return false
	}
	switch c.op {
	case "equals":
		return same(s.Value, c.value)
	case "not_equals":
		return !same(s.Value, c.value)
	case "in":
		return slices.ContainsFunc(c.value.([]any), func(w any) bool { return same(s.Value, w) })
	case "matches":
		str, ok := s.Value.(string)
		return ok && c.re.MatchString(str)
	}
	return false
}

// contains reports whether a list holds want, passing over elements that
// are unknown or sensitive, or whether a string has want in it.
func contains(s diff.Selection, want any) bool {
	switch x := s.Value.(type) {
	case []any:
		for i, e := range x {
			if !diff.Marked(diff.Child(s.Unknown, i)) && !diff.Marked(diff.Child(s.Sensitive, i)) && same(e, want) {
				return true
			}
		}
	case string:
		w, ok := scalarText(want)
		return ok && !s.Opaque() && strings.Contains(x, w)
	}
	return false
}

// describe says how a condition was met: "`ingress[0].from_port` is `22`".
func (c *condition) describe(s diff.Selection) string {
	p := Code(s.At(c.attribute))
	later := ""
	if diff.Marked(s.Unknown) {
		later = " (known after apply)"
	}
	switch c.op {
	case "equals":
		return p + " is " + Code(diff.String(c.value))
	case "not_equals":
		return p + " is not " + Code(diff.String(c.value))
	case "in":
		var values []string
		for _, v := range c.value.([]any) {
			values = append(values, Code(diff.String(v)))
		}
		return p + " is " + either(values)
	case "matches":
		return p + " matches " + Code(c.re.String())
	case "contains":
		return p + " contains " + Code(diff.String(c.value))
	case "present":
		if !c.value.(bool) {
			return p + " is not set"
		}
		return p + " is set" + later
	case "changes":
		if !c.value.(bool) {
			return p + " does not change"
		}
		return p + " changes" + later
	}
	return p
}

// same compares a value from the plan with one from .interragate.yaml.
// Quoting in YAML is easy to get wrong, so a string matches a number or bool
// written the same way: "22" and 22, "true" and true.
func same(v, want any) bool {
	switch w := want.(type) {
	case []any:
		l, ok := v.([]any)
		if !ok || len(l) != len(w) {
			return false
		}
		for i := range w {
			if !same(l[i], w[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		m, ok := v.(map[string]any)
		if !ok || len(m) != len(w) {
			return false
		}
		for k, e := range w {
			if x, ok := m[k]; !ok || !same(x, e) {
				return false
			}
		}
		return true
	}
	if diff.Equal(v, want) {
		return true
	}
	_, vs := v.(string)
	_, ws := want.(string)
	a, aok := scalarText(v)
	b, bok := scalarText(want)
	return vs != ws && aok && bok && a == b
}

// scalarText writes a string, number or bool as text.
func scalarText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

// planValue turns numbers in a condition built in Go, rather than read from
// YAML, into json.Number, the type plan JSON decodes numbers to, so the
// two compare.
func planValue(v any) any {
	switch x := v.(type) {
	case int:
		return json.Number(strconv.Itoa(x))
	case float64:
		return json.Number(strconv.FormatFloat(x, 'f', -1, 64))
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = planValue(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = planValue(e)
		}
		return out
	}
	return v
}

// oneLine folds text written over several lines in YAML into one.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// either joins items as "a, b or c".
func either(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}
