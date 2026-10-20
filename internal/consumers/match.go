package consumers

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/model"
)

// Match is what one declared use names in the plan.
type Match struct {
	Service *config.Consumer
	Use     config.Use
	// Changes are the resources the use names, or those that answer for its
	// host before or after the plan.
	Changes []*model.Change
	// Output is the output it names.
	Output *model.Output
	// Host is the host it names, when it names one.
	Host config.Host
	// Flaw says why the plan cannot be checked against the use although it
	// has what the use names, such as an attribute the resource lacks.
	Flaw string
}

// Found reports whether the plan has what the use names, as declared.
func (m *Match) Found() bool { return (len(m.Changes) > 0 || m.Output != nil) && m.Flaw == "" }

// Missing says what a use names that the plan does not have, as declared,
// and why when the plan has part of it.
func (m *Match) Missing() string {
	if m.Flaw != "" {
		return m.Use.Target.String() + " (" + m.Flaw + ")"
	}
	return m.Use.Target.String()
}

// match finds what each declared use names in the plan, in the file's
// order. A use a service declares twice, such as a host written once as a
// URL and once on its own, counts once.
func (a *Assessment) match() []*Match {
	var out []*Match
	for i := range a.in.Services {
		svc := &a.in.Services[i]
		seen := make(map[string]bool, len(svc.Uses))
		for _, u := range svc.Uses {
			if seen[u.Target.String()] {
				continue
			}
			seen[u.Target.String()] = true
			m := &Match{Service: svc, Use: u}
			switch t := u.Target.(type) {
			case config.ResourceTarget:
				m.Changes = a.resources(t.Address)
				if !t.Attribute.IsZero() && len(m.Changes) > 0 && !a.haveAttribute(m.Changes, t.Attribute) {
					m.Flaw = "it has no attribute " + strings.SplitN(t.Attribute.String(), ".", 2)[0]
				}
			case config.ReachTarget:
				m.Changes = a.resources(t.Firewall)
			case config.OutputTarget:
				m.Output = a.output(t.Name)
			case config.HostTarget:
				m.Host = t.Host
				m.Changes = a.answering(t.Host)
			default:
				panic(fmt.Sprintf("consumers: a use names a target of unknown type %T", t))
			}
			out = append(out, m)
		}
	}
	return out
}

// resources finds the managed resources a pattern names: by an instance's
// address, its address before a move, or its configuration address, so
// aws_subnet.public covers every instance. A deposed object is left out:
// destroying it ends a replacement that is already counted.
func (a *Assessment) resources(p config.AddressPattern) []*model.Change {
	var out []*model.Change
	for _, ch := range a.in.Report.Changes {
		if ch.Mode == "data" || ch.Deposed != "" {
			continue
		}
		if p.Matches(ch.Address) || p.Matches(ch.Resource) || ch.PrevAddress != "" && p.Matches(ch.PrevAddress) {
			out = append(out, ch)
		}
	}
	return out
}

// haveAttribute reports whether one of the resources has the attribute a
// path starts with, before or after the plan, even if it is null or known
// only after apply. A path into a missing attribute is most likely a typo.
func (a *Assessment) haveAttribute(chs []*model.Change, path config.AttributePath) bool {
	steps := path.Steps()
	if len(steps) == 0 || steps[0].Any {
		return true
	}
	for _, ch := range chs {
		rc := a.in.View.Raw(ch)
		if rc == nil {
			continue
		}
		for _, v := range []any{rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown} {
			if obj, ok := v.(map[string]any); ok {
				if _, ok := obj[steps[0].Key]; ok {
					return true
				}
			}
		}
	}
	return false
}

func (a *Assessment) output(name string) *model.Output {
	for _, o := range a.in.Report.Outputs {
		if o.Name == name {
			return o
		}
	}
	return nil
}

// answering finds the resources that answer for a host before or after the
// plan: a DNS record that resolves it, a CDN, load balancer or API domain
// that carries its name, or a certificate for it.
func (a *Assessment) answering(h config.Host) []*model.Change {
	var out []*model.Change
	for _, ch := range a.in.Report.Changes {
		if ch.Mode == "data" || ch.Deposed != "" {
			continue
		}
		if a.answers(ch, h, true) || a.answers(ch, h, false) {
			out = append(out, ch)
		}
	}
	return out
}

// answers reports whether a resource answers for a host before the plan,
// or after it. A wildcard DNS record answers only for a host with no
// records of its own then, as DNS resolves it.
func (a *Assessment) answers(ch *model.Change, h config.Host, before bool) bool {
	names := hostNames(ch.Type, a.state(ch, before))
	switch {
	case slices.ContainsFunc(names, h.Is):
		return true
	case !slices.ContainsFunc(names, h.AnsweredBy):
		return false
	}
	return !dnsRecord(ch.Type) || !a.hasRecords(h, before)
}

// hasRecords reports whether the plan has DNS records of any type under a
// host's own name before the plan, or after it.
func (a *Assessment) hasRecords(h config.Host, before bool) bool {
	for _, ch := range a.in.Report.Changes {
		if ch.Mode == "data" || ch.Deposed != "" || !dnsRecord(ch.Type) {
			continue
		}
		if slices.ContainsFunc(recordNames(ch.Type, a.state(ch, before)), h.Is) {
			return true
		}
	}
	return false
}

// state is a resource's values before the plan, or after it: nil when it
// does not exist then.
func (a *Assessment) state(ch *model.Change, before bool) map[string]any {
	rc := a.in.View.Raw(ch)
	if rc == nil {
		return nil
	}
	v := rc.Change.After
	if before {
		v = rc.Change.Before
	}
	obj, _ := v.(map[string]any)
	return obj
}

// hostNames lists the host names a resource answers for, by its type. A
// DNS record answers for its name only if it resolves it.
func hostNames(typ string, obj map[string]any) []string {
	if obj == nil {
		return nil
	}
	if dnsRecord(typ) {
		if !resolving(typ, obj) {
			return nil
		}
		return recordNames(typ, obj)
	}
	switch typ {
	case "aws_cloudfront_distribution":
		return texts(obj["aliases"], obj["domain_name"])
	case "aws_acm_certificate":
		return texts(obj["domain_name"], obj["subject_alternative_names"])
	case "aws_lb", "aws_alb", "aws_elb":
		return texts(obj["dns_name"])
	case "aws_api_gateway_domain_name", "aws_apigatewayv2_domain_name":
		return texts(obj["domain_name"])
	case "vercel_project_domain":
		return texts(obj["domain"])
	}
	return nil
}

// recordNames lists the names a DNS record carries, whatever its type.
func recordNames(typ string, obj map[string]any) []string {
	switch typ {
	case "aws_route53_record":
		return texts(obj["name"], obj["fqdn"])
	case "cloudflare_record", "cloudflare_dns_record":
		return texts(obj["name"], obj["hostname"])
	case "google_dns_record_set":
		return texts(obj["name"])
	case "azurerm_dns_a_record", "azurerm_dns_aaaa_record", "azurerm_dns_cname_record":
		return texts(obj["fqdn"])
	}
	return nil
}

// resolving reports whether a DNS record resolves its name to where it is
// served: an address or another name.
func resolving(typ string, obj map[string]any) bool {
	switch typ {
	case "azurerm_dns_a_record", "azurerm_dns_aaaa_record", "azurerm_dns_cname_record":
		return true
	case "aws_route53_record":
		if first(obj["alias"]) != nil {
			return true
		}
	}
	switch strings.ToUpper(text(obj["type"])) {
	case "A", "AAAA", "CNAME":
		return true
	}
	return false
}

// text reads a value as a string, or "".
func text(v any) string {
	s, _ := v.(string)
	return s
}

// texts reads strings and lists of strings into one list.
func texts(vs ...any) []string {
	var out []string
	for _, v := range vs {
		switch x := v.(type) {
		case string:
			if x != "" {
				out = append(out, x)
			}
		case []any:
			for _, e := range x {
				if s, ok := e.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// first reads a nested block, which the plan writes as a list of one
// object, or nil.
func first(v any) map[string]any {
	if l, ok := v.([]any); ok && len(l) > 0 {
		m, _ := l[0].(map[string]any)
		return m
	}
	return nil
}
