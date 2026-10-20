package consumers

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Jeecabs/interragate/internal/access"
	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
)

// Impact is one thing a plan does to what a service outside Terraform
// uses: a *Loss, a *Shift, a *Repoint or a *Closure.
type Impact interface {
	// Kind is the effect it has on the service.
	Kind() model.EffectKind
	// Of is the use it affects.
	Of() *Match
	// Subject is the address it is about, or output.NAME for an output.
	Subject() string
	// Update is how a changed value reaches the service.
	Update() model.UpdatePath
	// ServiceSide says the impact from the service's side, for its own
	// list. tool is Terraform or OpenTofu.
	ServiceSide(tool string) string
	// key is the same for impacts that one finding reports together.
	key() string
}

// on is what every impact has: the use it affects and the change behind it.
type on struct {
	use    *Match
	change *model.Change // nil for an output
	at     string
}

func (o *on) Of() *Match               { return o.use }
func (o *on) Subject() string          { return o.at }
func (o *on) Update() model.UpdatePath { return UpdatePathOf(o.use.Service, o.use.Use) }

// Loss is what a service uses going: a resource destroyed, an output
// removed, or a host the plan stops answering for.
type Loss struct {
	on
	// Unanswered is set when the plan stops a resource answering for the
	// host without destroying it, as when a record is renamed.
	Unanswered bool
}

func (*Loss) Kind() model.EffectKind { return model.EffectLoses }
func (l *Loss) key() string          { return "loses|" + l.at }

// Shift is a value a service holds changing, or becoming known only after
// apply, so that it may change.
type Shift struct {
	on
	// Path is the attribute, or empty for an output.
	Path string
	// Before and After are the value before and after, for prose, when the
	// plan shows both and neither is sensitive.
	Before, After string
	// Unknown is set when the new value is known only after apply.
	Unknown bool
	// Identity is set when the service needs the resource itself, and a
	// replacement gives it a new Path, such as its ARN.
	Identity bool
}

func (*Shift) Kind() model.EffectKind { return model.EffectValue }
func (s *Shift) key() string          { return "value|" + s.at + "|" + s.Path }

// Repoint is the plan pointing a host a service calls somewhere else.
type Repoint struct {
	on
	// Unknown is set when where it points is known only after apply.
	Unknown bool
}

func (*Repoint) Kind() model.EffectKind { return model.EffectValue }
func (r *Repoint) key() string          { return "repoint|" + r.at }

// Update is live: a service finds a host's new address by itself, as DNS
// caches expire.
func (*Repoint) Update() model.UpdatePath { return model.UpdateLive }

// Closure is the plan closing a way in that a service uses.
type Closure struct {
	on
	Port access.Port
	// Lost are where the firewall no longer lets the port in from.
	Lost []access.Source
	// Drifted is set when what let the service in was added outside
	// Terraform, so the plan undoes a change someone made for a reason.
	Drifted bool
}

func (*Closure) Kind() model.EffectKind { return model.EffectReach }
func (c *Closure) key() string {
	sources := make([]string, len(c.Lost))
	for i, s := range c.Lost {
		sources[i] = s.String()
	}
	return "reach|" + c.at + "|" + c.Port.String() + "|" + strings.Join(sources, ",")
}

// impactsOf works out what the plan does to what a use names.
func (a *Assessment) impactsOf(m *Match) []Impact {
	if !m.Found() {
		return nil
	}
	var out []Impact
	add := func(im Impact) {
		if im != nil {
			out = append(out, im)
		}
	}
	switch t := m.Use.Target.(type) {
	case config.ResourceTarget:
		for _, ch := range m.Changes {
			add(a.resourceImpact(m, ch, t))
		}
	case config.ReachTarget:
		for _, ch := range m.Changes {
			add(a.closure(m, ch, t))
		}
	case config.OutputTarget:
		add(a.outputImpact(m))
	case config.HostTarget:
		for _, ch := range m.Changes {
			add(a.hostImpact(m, ch))
		}
	default:
		panic(fmt.Sprintf("consumers: a use names a target of unknown type %T", t))
	}
	return out
}

// resourceImpact works out what the plan does to a resource a service
// uses: destroys it, or changes the value it holds. Without an attribute,
// the service needs the resource itself, which an update keeps and a
// replacement swaps for a new one.
func (a *Assessment) resourceImpact(m *Match, ch *model.Change, t config.ResourceTarget) Impact {
	at := on{use: m, change: ch, at: ch.Address}
	switch ch.Action {
	case model.Delete:
		return &Loss{on: at}
	case model.Update, model.Replace:
	default:
		// Created, read, forgotten without being destroyed, or left alone:
		// nothing the service used goes.
		return nil
	}
	if !t.Attribute.IsZero() {
		if s := a.shift(at, t.Attribute.String(), t.Attribute.Steps()); s != nil {
			return s
		}
		return nil
	}
	if ch.Action != model.Replace {
		return nil
	}
	for _, name := range []string{"arn", "id"} {
		if s := a.shift(at, name, []diff.Step{{Key: name}}); s != nil {
			s.Identity = true
			return s
		}
	}
	return nil
}

// shift works out how a resource's attribute changes, or returns nil when
// it does not. A value known only after apply counts, as terraform plan
// shows it.
func (a *Assessment) shift(at on, path string, steps []diff.Step) *Shift {
	rc := a.in.View.Raw(at.change)
	if rc == nil {
		return nil
	}
	before := diff.Select(rc.Change.Before, nil, rc.Change.BeforeSensitive, steps)
	after := diff.Select(rc.Change.After, rc.Change.AfterUnknown, rc.Change.AfterSensitive, steps)
	if _, changed := diff.Differs(before, after); !changed {
		return nil
	}
	s := &Shift{on: at, Path: path}
	s.Unknown = len(after) == 1 && diff.Marked(after[0].Unknown)
	if len(before) == 1 && len(after) == 1 && !before[0].Opaque() && !after[0].Opaque() && before[0].Value != nil && after[0].Value != nil {
		s.Before, s.After = shown(before[0].Value), shown(after[0].Value)
	}
	return s
}

// outputImpact works out what the plan does to an output a service reads.
func (a *Assessment) outputImpact(m *Match) Impact {
	o := m.Output
	at := on{use: m, at: "output." + o.Name}
	switch o.Action {
	case model.Delete:
		return &Loss{on: at}
	case model.Update:
	default:
		return nil
	}
	oc := a.in.Plan.OutputChanges[o.Name]
	if oc == nil {
		return nil
	}
	before := diff.Select(oc.Before, nil, oc.BeforeSensitive, nil)
	after := diff.Select(oc.After, oc.AfterUnknown, oc.AfterSensitive, nil)
	if _, changed := diff.Differs(before, after); !changed {
		return nil
	}
	s := &Shift{on: at, Unknown: diff.Marked(oc.AfterUnknown)}
	if !o.Sensitive && !s.Unknown && oc.Before != nil && oc.After != nil {
		s.Before, s.After = shown(oc.Before), shown(oc.After)
	}
	return s
}

// hostImpact works out what the plan does to a resource that answers for a
// host a service calls: destroys it, stops it answering for the host, or
// points the host somewhere else. Something new that answers for the host
// takes nothing away.
func (a *Assessment) hostImpact(m *Match, ch *model.Change) Impact {
	at := on{use: m, change: ch, at: ch.Address}
	before, after := a.answers(ch, m.Host, true), a.answers(ch, m.Host, false)
	switch {
	case !before || ch.Action == model.Forget:
		return nil
	case ch.Action == model.Delete:
		return &Loss{on: at}
	case !after:
		return &Loss{on: at, Unanswered: true}
	case ch.Action != model.Update && ch.Action != model.Replace:
		return nil
	}
	for _, steps := range targetsOf(ch.Type) {
		if s := a.shift(at, "", steps); s != nil {
			return &Repoint{on: at, Unknown: s.Unknown}
		}
	}
	return nil
}

// targetsOf lists the paths at which a DNS record says where its name
// points. A record's other settings, such as an alias's health checks,
// do not move a host.
func targetsOf(typ string) [][]diff.Step {
	step := func(keys ...string) []diff.Step {
		out := make([]diff.Step, len(keys))
		for i, k := range keys {
			out[i] = diff.Step{Key: k}
		}
		return out
	}
	switch typ {
	case "aws_route53_record":
		return [][]diff.Step{step("records"), step("alias", "name"), step("alias", "zone_id")}
	case "cloudflare_record":
		return [][]diff.Step{step("value"), step("content")}
	case "cloudflare_dns_record":
		return [][]diff.Step{step("content")}
	case "google_dns_record_set":
		return [][]diff.Step{step("rrdatas")}
	case "azurerm_dns_a_record", "azurerm_dns_aaaa_record":
		return [][]diff.Step{step("records"), step("target_resource_id")}
	case "azurerm_dns_cname_record":
		return [][]diff.Step{step("record"), step("target_resource_id")}
	}
	return nil
}

// closure works out whether a firewall stops letting a service in on its
// port: from the range it declares, or else from any range or group the
// firewall let the port in from before. What the firewall will let in is
// unknown, and nothing is said, when the plan knows it only after apply.
func (a *Assessment) closure(m *Match, ch *model.Change, t config.ReachTarget) Impact {
	rc := a.in.View.Raw(ch)
	if rc == nil || ch.Action == model.Forget || ch.Action == model.Create || ch.Action == model.NoOp {
		return nil
	}
	for _, attr := range access.FirewallAttributes(ch.Type) {
		if diff.Marked(diff.Child(rc.Change.AfterUnknown, attr)) {
			return nil
		}
	}
	at := on{use: m, change: ch, at: ch.Address}
	before, _ := rc.Change.Before.(map[string]any)
	after, _ := rc.Change.After.(map[string]any)
	if ch.Action == model.Delete {
		after = nil
	}
	was, ok := access.ReadFirewall(ch.Type, before)
	if !ok {
		// Not a firewall interragate reads: the service needs it to exist.
		if ch.Action == model.Delete {
			return &Loss{on: at}
		}
		return nil
	}
	now, _ := access.ReadFirewall(ch.Type, after)
	var lost []access.Source
	if t.From.Range.IsValid() {
		if was.Admits(t.Port, t.From) && !now.Admits(t.Port, t.From) {
			lost = []access.Source{t.From}
		}
	} else {
		for _, s := range was.SourcesOf(t.Port) {
			if !now.Admits(t.Port, s) {
				lost = append(lost, s)
			}
		}
	}
	if len(lost) == 0 {
		return nil
	}
	c := &Closure{on: at, Port: t.Port, Lost: lost}
	if d := a.in.View.DriftFor(ch.Address); d != nil && !d.Noise {
		c.Drifted = slices.ContainsFunc(d.Reverted, func(p string) bool { return strings.HasPrefix(p, "ingress") })
	}
	return c
}

// levelFor is the severity of an impact on its service: the service's own,
// but no more than low for a change that reaches the service by itself,
// such as a host's new address or a value it reads as it runs.
func levelFor(im Impact) model.Severity {
	level := levelOf(im.Of().Service)
	if im.Kind() == model.EffectValue && im.Update() == model.UpdateLive {
		return min(level, model.Low)
	}
	return level
}
