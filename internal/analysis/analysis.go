// Package analysis turns a plan into a report: it classifies and explains
// every change, works out which changes follow from others, separates real
// drift from provider noise, runs the rules and writes the summary.
package analysis

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Jeecabs/interragate/internal/access"
	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/gitdiff"
	"github.com/Jeecabs/interragate/internal/graph"
	"github.com/Jeecabs/interragate/internal/kb"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
	"github.com/Jeecabs/interragate/internal/rules"
	"github.com/Jeecabs/interragate/internal/scan"
	"github.com/Jeecabs/interragate/internal/source"
)

// Options configure an analysis.
type Options struct {
	// SourceDir is the root module's directory. When set, .tf files are
	// read for locations, locals and lifecycle settings.
	SourceDir string
	// Source names where the plan came from, for display.
	Source string
	// Tool is "terraform" or "opentofu" when known.
	Tool string
	// Config tunes rules. May be nil.
	Config *config.Config
	// Generator identifies interragate in the report, e.g.
	// "interragate 0.1.0".
	Generator string
	// Now is the report timestamp; zero means time.Now.
	Now time.Time
	// Edits is how the configuration differs from a git base, to find the
	// edit behind each change. It needs SourceDir. May be nil.
	Edits *gitdiff.Diff
	// Scans are what configuration scanners reported, by scanner, to show
	// the issues on what the plan creates, replaces or edits. It needs
	// SourceDir.
	Scans []ScanResult
	// SkipApply leaves out the simulation of the apply (Report.Apply), for
	// commands that never show it, unless the configuration declares
	// services outside Terraform, whose outages come from it.
	SkipApply bool
}

// ScanResult is one scanner's issues, or why it could not run.
type ScanResult struct {
	Tool   string
	Issues []scan.Issue
	Err    error
}

type analyzer struct {
	info   model.PlanInfo
	plan   *plan.Plan
	opts   Options
	src    *source.Index
	graph  *graph.Graph
	raw    map[*model.Change]*plan.ResourceChange
	full   map[*model.Change]*diff.Node
	byAddr map[string]*model.Change
}

// Analyze builds the report for a plan.
func Analyze(p *plan.Plan, opts Options) *model.Report {
	a := &analyzer{plan: p, opts: opts, raw: map[*model.Change]*plan.ResourceChange{}, full: map[*model.Change]*diff.Node{}, byAddr: map[string]*model.Change{}}
	if opts.SourceDir != "" {
		if idx, err := source.Load(opts.SourceDir, p.Configuration); err == nil && idx.Modules[""] != nil && len(idx.Modules[""].Blocks) > 0 {
			a.src = idx
		}
	}
	var locals graph.Locals
	if a.src != nil {
		locals = a.src.Locals
	}
	a.graph = graph.Build(p.Configuration, locals)

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	a.info = a.planInfo()
	r := &model.Report{
		Schema:      model.SchemaVersion,
		Generator:   opts.Generator,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Plan:        a.info,
	}
	for _, rc := range p.ResourceChanges {
		ch := a.change(rc)
		r.Changes = append(r.Changes, ch)
		a.byAddr[ch.Address] = ch
	}
	r.Plan.Destroy = isDestroyPlan(r.Changes)
	for _, ch := range r.Changes {
		ch.Why = a.why(ch, r.Plan.Destroy)
	}
	r.Drift = a.drift()
	a.causes(r)
	r.Patterns = a.patterns(r)
	r.Outputs = a.outputs()
	r.Checks = a.checks()
	a.dependents(r)
	a.edits(r)
	a.owners(r)
	a.access(r)
	a.deploys(r)
	for _, ch := range r.Changes {
		ch.Summary = summaryLine(ch)
	}

	ctx := rules.NewContext(p, r, a.graph, a.src, opts.Config, a.raw)
	ctx.Scanned, r.Scans = a.scanned(r, opts.Scans)
	r.Findings, r.Suppressed = rules.Run(ctx)
	// The apply's simulation reads the findings that expect it to fail, and
	// the rules that read the simulation run once it is done. Services
	// outside Terraform need it even for a command that does not show it.
	if !opts.SkipApply || declares(opts.Config) {
		r.Apply = a.apply(r)
	}
	if r.Apply != nil {
		ctx.Timeline = timeline(r.Apply)
		late, suppressed := rules.RunAfterApply(ctx)
		r.Findings = append(r.Findings, late...)
		r.Suppressed = append(r.Suppressed, suppressed...)
		sort.SliceStable(r.Findings, func(i, j int) bool { return r.Findings[i].Severity > r.Findings[j].Severity })
	}
	for _, f := range append(slices.Clone(r.Findings), r.Suppressed...) {
		if f.About != "" {
			if r.RuleDocs == nil {
				r.RuleDocs = map[string]string{}
			}
			r.RuleDocs[f.Rule] = f.About
		}
	}
	a.attach(r)
	r.Graph = a.graphModel(r)
	r.Summary = summarize(r)
	r.Risk = risk(r)
	r.Narrative = narrative(r)
	a.consumers(r, ctx)
	emptyNotNull(r)
	return redact(r, secrets(p))
}

// emptyNotNull makes empty lists encode as [] rather than null, which is
// friendlier to tools reading the JSON report.
func emptyNotNull(r *model.Report) {
	if r.Changes == nil {
		r.Changes = []*model.Change{}
	}
	if r.Findings == nil {
		r.Findings = []*model.Finding{}
	}
	if r.Drift == nil {
		r.Drift = []*model.Drift{}
	}
	if r.Outputs == nil {
		r.Outputs = []*model.Output{}
	}
	if r.Narrative == nil {
		r.Narrative = []model.Sentence{}
	}
	if r.Graph.Nodes == nil {
		r.Graph.Nodes = []*model.GraphNode{}
	}
	if r.Graph.Edges == nil {
		r.Graph.Edges = [][2]string{}
	}
}

// configured returns the arguments the configuration sets for the resource
// behind an instance address.
func (a *analyzer) configured(address string) map[string]bool {
	out := map[string]bool{}
	addr, err := plan.ParseAddress(address)
	if err != nil || a.plan.Configuration == nil {
		return out
	}
	mod := a.plan.Configuration.RootModule
	for _, step := range addr.Module {
		if mod == nil {
			return out
		}
		call := mod.ModuleCalls[step.Name]
		if call == nil {
			return out
		}
		mod = call.Module
	}
	if mod == nil {
		return out
	}
	for _, r := range mod.Resources {
		if r.Type == addr.Type && r.Name == addr.Name && r.Mode == addr.Mode {
			for k := range r.Expressions {
				out[k] = true
			}
		}
	}
	return out
}

func (a *analyzer) planInfo() model.PlanInfo {
	p := a.plan
	info := model.PlanInfo{
		Tool:          a.opts.Tool,
		Version:       p.TerraformVersion,
		FormatVersion: p.FormatVersion,
		Timestamp:     p.Timestamp,
		Source:        a.opts.Source,
		Applyable:     p.Applyable,
		Complete:      p.Complete,
		Errored:       p.Errored,
		SourceRead:    a.src != nil,
	}
	if info.Tool == "" {
		info.Tool = guessTool(p)
	}
	for _, rc := range p.ResourceChanges {
		if full := strings.TrimSuffix(strings.TrimPrefix(rc.ProviderName, "provider[\""), "\"]"); strings.Count(full, "/") == 2 {
			if info.Providers == nil {
				info.Providers = map[string]string{}
			}
			info.Providers[providerShortName(full)] = full
		}
	}
	if a.opts.SourceDir != "" {
		if abs, err := filepath.Abs(a.opts.SourceDir); err == nil {
			info.Dir = filepath.Base(abs)
		}
	}
	return info
}

// guessTool tells OpenTofu from Terraform by the registry the providers
// come from, which survives in every plan.
func guessTool(p *plan.Plan) string {
	if p.Configuration != nil {
		for _, pc := range p.Configuration.ProviderConfig {
			switch {
			case strings.HasPrefix(pc.FullName, "registry.opentofu.org/"):
				return "opentofu"
			case strings.HasPrefix(pc.FullName, "registry.terraform.io/"):
				return "terraform"
			}
		}
	}
	for _, rc := range p.ResourceChanges {
		switch {
		case strings.HasPrefix(rc.ProviderName, "registry.opentofu.org/"):
			return "opentofu"
		case strings.HasPrefix(rc.ProviderName, "registry.terraform.io/"):
			return "terraform"
		}
	}
	return ""
}

// Classify maps Terraform's action list to one action and, for replacements,
// the order in which the old and new objects are handled.
func Classify(actions []string) (model.Action, string) {
	switch strings.Join(actions, ",") {
	case "create":
		return model.Create, ""
	case "update":
		return model.Update, ""
	case "delete,create":
		return model.Replace, "delete-first"
	case "create,delete":
		return model.Replace, "create-first"
	case "delete":
		return model.Delete, ""
	case "read":
		return model.Read, ""
	case "forget":
		return model.Forget, ""
	case "create,forget", "forget,create":
		return model.Replace, "forget"
	case "no-op", "":
		return model.NoOp, ""
	}
	return model.Action(strings.Join(actions, "+")), ""
}

var tagAttrs = map[string]bool{"tags": true, "tags_all": true, "labels": true, "terraform_labels": true, "effective_labels": true}

var identifying = map[string]bool{"id": true, "name": true, "arn": true}

func (a *analyzer) change(rc *plan.ResourceChange) *model.Change {
	addr := plan.AddressOf(rc)
	action, order := Classify(rc.Change.Actions)
	address := rc.Address
	if rc.Deposed != "" {
		address += deposedSuffix(rc.Deposed)
	}
	ch := &model.Change{
		Address:     address,
		Resource:    addr.Config(),
		PrevAddress: rc.PreviousAddress,
		Module:      addr.ModuleString(),
		Mode:        addr.Mode,
		Type:        rc.Type,
		Name:        rc.Name,
		Key:         addr.Key,
		Provider:    providerShortName(rc.ProviderName),
		Action:      action,
		Order:       order,
		Moved:       rc.PreviousAddress != "" && rc.PreviousAddress != rc.Address,
		Deposed:     rc.Deposed,
		Reason:      rc.ActionReason,
	}
	if imp := rc.Change.Importing; imp != nil {
		ch.Importing = true
		ch.ImportID = imp.ID
	}
	info := kb.Lookup(rc.Type)
	ch.Noun, ch.Category, ch.Stateful = info.Noun, string(info.Category), info.Stateful
	if a.opts.Config != nil {
		for _, t := range a.opts.Config.Stateful {
			if t == rc.Type {
				ch.Stateful = true
			}
		}
	}
	if ch.Mode == "data" {
		ch.Stateful = false
	}
	a.raw[ch] = rc

	hasBefore := action != model.Create && action != model.Read
	hasAfter := action != model.Delete && action != model.Forget
	if ch.Importing && action == model.NoOp {
		hasBefore = false
	}
	full := diff.Compute(diff.Input{
		Before: rc.Change.Before, After: rc.Change.After,
		AfterUnknown:    rc.Change.AfterUnknown,
		BeforeSensitive: rc.Change.BeforeSensitive, AfterSensitive: rc.Change.AfterSensitive,
		ReplacePaths: rc.Change.ReplacePaths,
		HasBefore:    hasBefore, HasAfter: hasAfter,
	})
	a.full[ch] = full
	leaves := full.Leaves()
	for _, l := range leaves {
		ch.Changed++
		if l.Unknown {
			ch.Unknown++
		}
	}
	if action == model.Replace {
		full.Walk(func(n *diff.Node) bool {
			if n.Forces {
				ch.ForcedBy = append(ch.ForcedBy, n.PathString())
			}
			return true
		})
		if len(ch.ForcedBy) == 0 {
			for _, p := range rc.Change.ReplacePaths {
				ch.ForcedBy = append(ch.ForcedBy, diff.PathString(normalizePath(p)))
			}
		}
	}
	if action == model.Update {
		ch.Cosmetic = cosmetic(leaves)
	}
	if action != model.NoOp || ch.Importing {
		pruned := full.Clone()
		pruned.Prune(func(n *diff.Node) bool { return identifying[n.Key] && len(n.Path()) == 1 })
		ch.Diff = pruned
	}
	if a.src != nil {
		local := addr.Type + "." + addr.Name
		if addr.Mode == "data" {
			local = "data." + local
		}
		if b := a.src.Block(addr.ModuleConfigString(), local); b != nil {
			ch.Location = &model.Location{File: b.File, Line: b.Line, EndLine: b.EndLine}
		}
	}
	return ch
}

// cosmetic says why an update can safely be skimmed: it only touches tags,
// or only flips values between null and empty or reformats JSON.
func cosmetic(leaves []*diff.Node) string {
	if len(leaves) == 0 {
		return ""
	}
	tags := false
	for _, l := range leaves {
		switch {
		case l.Unknown:
			return ""
		case tagAttrs[l.Root()]:
			tags = true
		case l.Trivial:
		default:
			return ""
		}
	}
	if tags {
		return "tags"
	}
	return "formatting"
}

func normalizePath(p []any) []any {
	out := make([]any, len(p))
	for i, s := range p {
		switch x := s.(type) {
		case string:
			out[i] = x
		default:
			if n, ok := plan.KeyValue([]byte(diff.String(x))); ok {
				out[i] = n
			} else {
				out[i] = diff.String(x)
			}
		}
	}
	return out
}

// deposedSuffix marks a deposed object's address in Terraform's own wording,
// which keeps it unique next to the current object of the same name.
func deposedSuffix(key string) string {
	return " (deposed object " + key + ")"
}

func providerShortName(full string) string {
	full = strings.TrimPrefix(full, "provider[\"")
	full = strings.TrimSuffix(full, "\"]")
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[i+1:]
	}
	return full
}

func isDestroyPlan(changes []*model.Change) bool {
	managed := 0
	for _, ch := range changes {
		if ch.Mode == "data" {
			continue
		}
		managed++
		if ch.Action != model.Delete || ch.Reason != "" {
			return false
		}
	}
	return managed > 0
}

func (a *analyzer) outputs() []*model.Output {
	var out []*model.Output
	for _, name := range diff.SortedKeys(a.plan.OutputChanges) {
		oc := a.plan.OutputChanges[name]
		action, _ := Classify(oc.Actions)
		o := &model.Output{Name: name, Action: action}
		o.Sensitive = diff.Flag(oc.BeforeSensitive) || diff.Flag(oc.AfterSensitive)
		if action != model.NoOp {
			n := diff.Compute(diff.Input{
				Before: map[string]any{"value": oc.Before}, After: map[string]any{"value": oc.After},
				AfterUnknown:    map[string]any{"value": oc.AfterUnknown},
				BeforeSensitive: map[string]any{"value": oc.BeforeSensitive},
				AfterSensitive:  map[string]any{"value": oc.AfterSensitive},
				HasBefore:       action != model.Create, HasAfter: action != model.Delete,
			})
			n.Prune(nil)
			if len(n.Children) == 1 {
				o.Diff = n.Children[0]
				o.Diff.Key = ""
			}
		}
		out = append(out, o)
	}
	return out
}

func (a *analyzer) checks() []*model.Check {
	var out []*model.Check
	for _, c := range a.plan.Checks {
		mc := &model.Check{Address: c.Address.ToDisplay, Kind: c.Address.Kind, Status: c.Status}
		for _, inst := range c.Instances {
			for _, p := range inst.Problems {
				mc.Problems = append(mc.Problems, p.Message)
			}
		}
		out = append(out, mc)
	}
	return out
}

// dependents counts, for every change that destroys an object, how many
// resources depend on it.
func (a *analyzer) dependents(r *model.Report) {
	for _, ch := range r.Changes {
		if ch.Action == model.Delete || ch.Action == model.Replace {
			ch.Dependents = len(a.graph.Downstream(ch.Resource))
		}
	}
}

// attach links findings to changes and derives each change's severity.
func (a *analyzer) attach(r *model.Report) {
	for _, f := range r.Findings {
		for _, addr := range f.Addresses {
			if ch := a.byAddr[addr]; ch != nil {
				ch.Findings = append(ch.Findings, f.ID)
				if f.Severity > ch.Severity {
					ch.Severity = f.Severity
				}
			}
		}
	}
	for _, ch := range r.Changes {
		if ch.Severity == model.None {
			switch ch.Action {
			case model.Delete, model.Replace:
				ch.Severity = model.Low
			case model.Create, model.Update, model.Forget:
				ch.Severity = model.Info
			}
		}
	}
}

func summarize(r *model.Report) model.Summary {
	var s model.Summary
	for _, ch := range r.Changes {
		switch ch.Action {
		case model.Create:
			if ch.Importing {
				s.Import++
			} else {
				s.Create++
			}
		case model.Update:
			s.Update++
			if ch.Importing {
				s.Import++
			}
		case model.Replace:
			s.Replace++
			if ch.Importing {
				s.Import++
			}
		case model.Delete:
			s.Delete++
		case model.Read:
			s.Read++
		case model.Forget:
			s.Forget++
		case model.NoOp:
			switch {
			case ch.Importing:
				s.Import++
			case ch.Moved:
				s.Move++
			default:
				s.NoOp++
			}
		}
		if ch.Moved && ch.Action != model.NoOp {
			s.Move++
		}
		if ch.Cosmetic != "" {
			s.Cosmetic++
		}
		if ch.Indirect {
			s.Indirect++
		}
	}
	for _, d := range r.Drift {
		if !d.Noise {
			s.Drifted++
		}
	}
	for _, o := range r.Outputs {
		if o.Action != model.NoOp {
			s.Outputs++
		}
	}
	return s
}

func sortedUnique(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// access says what each change to an IAM policy or a firewall allows and
// stops allowing. Attributes whose planned value is not known yet, or that
// are sensitive, are left out.
func (a *analyzer) access(r *model.Report) {
	for _, ch := range r.Changes {
		rc := a.raw[ch]
		if ch.Mode == "data" || rc == nil {
			continue
		}
		before, _ := rc.Change.Before.(map[string]any)
		after, _ := rc.Change.After.(map[string]any)
		switch ch.Action {
		case model.Create:
			before = nil
		case model.Delete:
			after = nil
		case model.Update, model.Replace:
		default:
			continue
		}
		ch.Access = access.Diff(ch.Type, before, after, func(attr string) bool {
			return anyTrue(diff.Child(rc.Change.AfterUnknown, attr)) || anyTrue(diff.Child(rc.Change.AfterSensitive, attr)) || anyTrue(diff.Child(rc.Change.BeforeSensitive, attr))
		})
	}
}

// anyTrue reports whether a mask such as after_unknown flags anything.
func anyTrue(mask any) bool {
	switch x := mask.(type) {
	case bool:
		return x
	case []any:
		for _, e := range x {
			if anyTrue(e) {
				return true
			}
		}
	case map[string]any:
		for _, e := range x {
			if anyTrue(e) {
				return true
			}
		}
	}
	return false
}
