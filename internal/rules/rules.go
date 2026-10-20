// Package rules holds interragate's plan-aware checks.
//
// Rules look at what a plan will do, not just at the configuration, so they
// only flag what the change introduces: opening a port that was already open
// is not news, opening it in this plan is.
package rules

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/consumers"
	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/graph"
	"github.com/Jeecabs/interragate/internal/kb"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
	"github.com/Jeecabs/interragate/internal/source"
)

// Finding categories.
const (
	DataLoss     = "data-loss"
	Security     = "security"
	Availability = "availability"
	Correctness  = "correctness"
	Refactor     = "refactoring"
	Drift        = "drift"
	Hygiene      = "hygiene"
	// Policy findings come from the team's own rules in .interragate.yaml.
	Policy = "policy"
	// Scanner findings come from trivy, checkov or tflint.
	Scanner = "scanner"
	// Consumers findings are about services outside Terraform that
	// .interragate.yaml says use what it manages.
	Consumers = "consumers"
)

// Rule is one check.
type Rule struct {
	ID       string
	Title    string
	Severity model.Severity
	Category string
	// Doc explains what the rule looks for and why it matters.
	Doc string
	// AfterApply is set for a rule that reads interragate's simulation of
	// the apply, which runs once the other rules' findings say where the
	// apply is expected to fail. RunAfterApply runs these rules.
	AfterApply bool
	Check      func(*Context) []*model.Finding
}

// All returns every rule interragate ships, ordered by category then ID.
// Each family of rules is defined in a file of its own; this is the one
// list of them.
func All() []*Rule {
	out := slices.Concat(
		dataRules(),
		failureRules(),
		inUseRules(),
		roleRules(),
		fightRules(),
		securityRules(),
		crossAccountRules(),
		provisionerRules(),
		availabilityRules(),
		rolloutRules(),
		runtimeRules(),
		refactorRules(),
		untiedRules(),
		consumerRules(),
		driftRules(),
		hygieneRules(),
		scannerRules(),
	)
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := categoryRank(out[i].Category), categoryRank(out[j].Category); a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// categoryRank orders categories the way interragate lists rules: what
// loses data first, scanners last.
func categoryRank(category string) int {
	switch category {
	case DataLoss:
		return 0
	case Correctness:
		return 1
	case Security:
		return 2
	case Availability:
		return 3
	case Consumers:
		return 4
	case Refactor:
		return 5
	case Drift:
		return 6
	case Hygiene:
		return 7
	case Scanner:
		return 8
	}
	return 9
}

// IDs lists the IDs of every rule interragate ships, which the team's own
// policies may not take.
func IDs() []string {
	all := All()
	ids := make([]string, 0, len(all))
	for _, r := range all {
		ids = append(ids, r.ID)
	}
	return ids
}

// Lookup finds a rule by ID.
func Lookup(id string) *Rule {
	for _, r := range All() {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// Context is what a rule can see.
type Context struct {
	Plan   *plan.Plan
	Report *model.Report
	Graph  *graph.Graph
	Source *source.Index
	Config *config.Config

	// Scanned holds, by scanner, the findings made from what it reported
	// on the changes in the plan.
	Scanned map[string][]*model.Finding
	// Timeline is the simulated apply and where it is expected to stop,
	// for the rules that run after it.
	Timeline *consumers.Timeline

	byAddr     map[string]*model.Change
	raw        map[*model.Change]*plan.ResourceChange
	before     map[*model.Change]map[string]any
	after      map[*model.Change]map[string]any
	instances  map[string][]*model.Change
	drift      map[string]*model.Drift
	shiftCache *[]*shift
	consumers  *consumers.Assessment
}

// NewContext indexes a report for rules. raw maps each change to the plan
// entry it was built from.
func NewContext(p *plan.Plan, r *model.Report, g *graph.Graph, src *source.Index, cfg *config.Config, raw map[*model.Change]*plan.ResourceChange) *Context {
	c := &Context{Plan: p, Report: r, Graph: g, Source: src, Config: cfg,
		byAddr: map[string]*model.Change{}, raw: raw, instances: map[string][]*model.Change{}, drift: map[string]*model.Drift{},
		before: map[*model.Change]map[string]any{}, after: map[*model.Change]map[string]any{}}
	for _, ch := range r.Changes {
		c.byAddr[ch.Address] = ch
		c.instances[ch.Resource] = append(c.instances[ch.Resource], ch)
		// Rules only ever see masked values, so no message can quote a
		// value Terraform marks sensitive.
		if rc := raw[ch]; rc != nil {
			if m, ok := diff.Masked(rc.Change.Before, rc.Change.BeforeSensitive, diff.SensitivePlaceholder).(map[string]any); ok {
				c.before[ch] = m
			}
			if m, ok := diff.Masked(rc.Change.After, rc.Change.AfterSensitive, diff.SensitivePlaceholder).(map[string]any); ok {
				c.after[ch] = m
			}
		}
	}
	for _, d := range r.Drift {
		c.drift[d.Address] = d
	}
	return c
}

// Run evaluates every enabled rule, then the configuration's policies,
// except the rules that read the simulated apply. Findings matched by an
// ignore entry are returned separately as suppressed.
func Run(ctx *Context) (findings, suppressed []*model.Finding) {
	return run(ctx, false)
}

// RunAfterApply evaluates the enabled rules that read the simulated apply,
// which ctx.Timeline must hold.
func RunAfterApply(ctx *Context) (findings, suppressed []*model.Finding) {
	return run(ctx, true)
}

func run(ctx *Context, afterApply bool) (findings, suppressed []*model.Finding) {
	seen := map[string]int{}
	for _, rule := range append(All(), Policies(ctx.Config)...) {
		if rule.AfterApply != afterApply || ctx.Config.Disabled(rule.ID) {
			continue
		}
		for _, f := range rule.Check(ctx) {
			f.Rule = rule.ID
			f.About = rule.Doc
			if f.Severity == model.None {
				f.Severity = rule.Severity
			}
			if sev, ok := ctx.Config.SeverityFor(rule.ID); ok {
				f.Severity = sev
			}
			if f.Category == "" {
				f.Category = rule.Category
			}
			if f.Title == "" {
				f.Title = rule.Title
			}
			f.ID = rule.ID
			if len(f.Addresses) > 0 {
				f.ID += ":" + f.Addresses[0]
			}
			if n := seen[f.ID]; n > 0 {
				seen[f.ID]++
				f.ID = fmt.Sprintf("%s#%d", f.ID, n+1)
			} else {
				seen[f.ID] = 1
			}
			if reason, ok := ctx.Config.Ignored(rule.ID, f.Addresses); ok {
				f.Reason = reason
				suppressed = append(suppressed, f)
				continue
			}
			findings = append(findings, f)
		}
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Severity > findings[j].Severity })
	return findings, suppressed
}

// Changes returns changes with one of the given actions, in plan order.
func (c *Context) Changes(actions ...model.Action) []*model.Change {
	var out []*model.Change
	for _, ch := range c.Report.Changes {
		for _, a := range actions {
			if ch.Action == a {
				out = append(out, ch)
				break
			}
		}
	}
	return out
}

// Change finds a change by address.
func (c *Context) Change(addr string) *model.Change { return c.byAddr[addr] }

// Instances returns every change to a configuration resource.
func (c *Context) Instances(resource string) []*model.Change { return c.instances[resource] }

// DriftFor returns drift recorded for an address.
func (c *Context) DriftFor(addr string) *model.Drift { return c.drift[addr] }

// Raw returns the plan entry behind a change.
func (c *Context) Raw(ch *model.Change) *plan.ResourceChange { return c.raw[ch] }

// Before returns the object as it is now (refreshed), or nil. Sensitive
// values are replaced by a placeholder.
func (c *Context) Before(ch *model.Change) map[string]any { return c.before[ch] }

// After returns the planned object, or nil. Unknown attributes are absent
// and sensitive values are replaced by a placeholder.
func (c *Context) After(ch *model.Change) map[string]any { return c.after[ch] }

// Unknown reports whether an attribute of the planned object is unknown.
func (c *Context) Unknown(ch *model.Change, path ...any) bool {
	if rc := c.raw[ch]; rc != nil {
		return diff.Flag(rc.Change.AfterUnknown, path...)
	}
	return false
}

// Info returns what is known about a change's resource type, honouring
// extra stateful types from the configuration file.
func (c *Context) Info(ch *model.Change) kb.Info {
	info := kb.Lookup(ch.Type)
	if c.Config != nil {
		for _, t := range c.Config.Stateful {
			if t == ch.Type {
				info.Stateful = true
				if info.Loss == "" {
					info.Loss = "the data it holds"
				}
			}
		}
	}
	return info
}

// Setting returns a numeric rule setting, or def.
func (c *Context) Setting(rule, key string, def int) int {
	v, ok := c.Config.Setting(rule, key)
	if !ok {
		return def
	}
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	return def
}

// Managed reports whether a change is to a managed resource (not data).
func Managed(ch *model.Change) bool { return ch.Mode != "data" }

// Attr reads a possibly nested attribute such as "settings.tier", looking
// through single-element lists (nested blocks).
func Attr(obj map[string]any, dotted string) (any, bool) {
	var cur any = obj
	for _, part := range strings.Split(dotted, ".") {
		if l, ok := cur.([]any); ok {
			if len(l) == 0 {
				return nil, false
			}
			cur = l[0]
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// Bool reads a boolean attribute.
func Bool(obj map[string]any, dotted string) (value, ok bool) {
	v, found := Attr(obj, dotted)
	if !found {
		return false, false
	}
	b, isBool := v.(bool)
	return b, isBool
}

// String reads a string attribute.
func String(obj map[string]any, dotted string) string {
	v, _ := Attr(obj, dotted)
	s, _ := v.(string)
	return s
}

// Strings reads a list of strings, or a single string, as a slice.
func Strings(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Code wraps an address or value in backticks for the narrative markup.
// Backticks inside the value are replaced so the markup always pairs up.
func Code(s string) string { return "`" + Plain(s) + "`" }

// Plain makes a value from the plan safe to embed in prose: no backticks
// (which delimit code), no line breaks, and a bounded length.
func Plain(s string) string {
	s = strings.NewReplacer("`", "'", "\n", " ", "\r", " ").Replace(s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return s
}

// Quote renders a value from the plan as a quoted string for prose.
func Quote(s string) string { return strconv.Quote(Plain(s)) }

// Value renders a decoded value from the plan for prose.
func Value(v any) string { return Plain(diff.String(v)) }

// Article returns a noun phrase with "a" or "an", chosen by sound: "an S3
// bucket", "a KMS key", "an EC2 instance", "a NAT gateway".
func Article(noun string) string {
	if noun == "" {
		return noun
	}
	first := strings.Fields(noun)[0]
	if a, ok := spokenAsWord[first]; ok {
		return a + " " + noun
	}
	if isInitialism(first) {
		// Letter names that start with a vowel sound: A, E, F, H, I, L, M, N, O, R, S, X.
		if strings.ContainsRune("AEFHILMNORSX", rune(first[0])) {
			return "an " + noun
		}
		return "a " + noun
	}
	lower := strings.ToLower(first)
	if strings.HasPrefix(lower, "us") || strings.HasPrefix(lower, "uni") || strings.HasPrefix(lower, "uti") {
		return "a " + noun
	}
	if strings.ContainsRune("aeiou", rune(lower[0])) {
		return "an " + noun
	}
	return "a " + noun
}

var spokenAsWord = map[string]string{"NAT": "a", "SAML": "a", "OAuth": "an"}

func isInitialism(w string) bool {
	if len(w) < 2 {
		return false
	}
	upper := func(b byte) bool { return (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') }
	return upper(w[0]) && upper(w[1])
}

// Plural renders a count with a noun.
func Plural(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// List renders items as "a, b and c", eliding beyond max.
func List(items []string, max int) string {
	if len(items) > max {
		rest := len(items) - max
		items = append(append([]string(nil), items[:max]...), fmt.Sprintf("%d more", rest))
	}
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func finding(ch *model.Change, title, msg string) *model.Finding {
	return &model.Finding{Title: title, Message: msg, Addresses: []string{ch.Address}}
}
