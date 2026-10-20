package consumers

import (
	"slices"
	"sort"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/graph"
	"github.com/Jeecabs/interragate/internal/model"
)

// path is what a use relies on while the apply runs. front holds the
// resources it names, or those that answer for its host and what a DNS
// record among them points at, or for an output those its value comes
// from. groups holds the target groups behind any load balancer in front,
// in order of their configuration addresses: the load balancer serves
// nothing while every one of them is empty.
type path struct {
	front  []*model.Change
	groups []targetGroup
}

// targetGroup is a target group a load balancer's listeners forward to,
// with what registers targets in it.
type targetGroup struct {
	id      string
	members []*model.Change
}

// pathOf works out what a use relies on.
func (a *Assessment) pathOf(m *Match) path {
	var p path
	seen := map[string]bool{}
	add := func(ch *model.Change) {
		if ch != nil && ch.Deposed == "" && !seen[ch.Address] {
			seen[ch.Address] = true
			p.front = append(p.front, ch)
		}
	}
	switch t := m.Use.Target.(type) {
	case config.OutputTarget:
		for _, ref := range a.in.Graph.Outputs[t.Name] {
			// A module referred to as a whole is waited for, not read.
			if !ref.Whole {
				a.instances(ref.Resource, add)
			}
		}
		return p
	case config.HostTarget:
		for _, ch := range m.Changes {
			add(ch)
			if dnsRecord(ch.Type) {
				if n := a.in.Graph.Nodes[ch.Resource]; n != nil {
					for _, up := range n.Up {
						a.instances(up, add)
					}
				}
			}
		}
	default:
		for _, ch := range m.Changes {
			add(ch)
		}
	}
	var ids []string
	members := map[string][]*model.Change{}
	for _, ch := range p.front {
		lb := a.in.Graph.Nodes[ch.Resource]
		if lb == nil || !loadBalancer(lb.Type) {
			continue
		}
		for _, l := range a.down(lb, listener) {
			for _, tg := range slices.Concat(a.up(l, targetGroupType), a.forwards(l)) {
				if _, done := members[tg.ID]; done {
					continue
				}
				ids = append(ids, tg.ID)
				members[tg.ID] = nil
				for _, reg := range a.down(tg, registration) {
					a.instances(reg.ID, func(ch *model.Change) {
						if ch.Deposed == "" {
							members[tg.ID] = append(members[tg.ID], ch)
						}
					})
				}
			}
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		p.groups = append(p.groups, targetGroup{id: id, members: members[id]})
	}
	return p
}

// instances passes every change to a configuration resource to add.
func (a *Assessment) instances(resource string, add func(*model.Change)) {
	for _, ch := range a.in.Report.Changes {
		if ch.Resource == resource {
			add(ch)
		}
	}
}

// forwards lists the target groups a listener's rules send traffic to.
func (a *Assessment) forwards(l *graph.Node) []*graph.Node {
	var out []*graph.Node
	for _, rule := range a.down(l, listenerRule) {
		out = append(out, a.up(rule, targetGroupType)...)
	}
	return out
}

// down lists the resources of a kind that refer to n.
func (a *Assessment) down(n *graph.Node, kind func(string) bool) []*graph.Node {
	var out []*graph.Node
	for _, id := range n.Down {
		if d := a.in.Graph.Nodes[id]; d != nil && kind(d.Type) {
			out = append(out, d)
		}
	}
	return out
}

// up lists the resources of a kind that n refers to.
func (a *Assessment) up(n *graph.Node, kind func(string) bool) []*graph.Node {
	var out []*graph.Node
	for _, id := range n.Up {
		if u := a.in.Graph.Nodes[id]; u != nil && kind(u.Type) {
			out = append(out, u)
		}
	}
	return out
}

// The kinds of resource a request to a host passes through.

func dnsRecord(typ string) bool {
	switch typ {
	case "aws_route53_record", "cloudflare_record", "cloudflare_dns_record", "google_dns_record_set",
		"azurerm_dns_a_record", "azurerm_dns_aaaa_record", "azurerm_dns_cname_record":
		return true
	}
	return false
}

func loadBalancer(typ string) bool { return typ == "aws_lb" || typ == "aws_alb" }

func listener(typ string) bool { return typ == "aws_lb_listener" || typ == "aws_alb_listener" }

func listenerRule(typ string) bool {
	return typ == "aws_lb_listener_rule" || typ == "aws_alb_listener_rule"
}

func targetGroupType(typ string) bool {
	return typ == "aws_lb_target_group" || typ == "aws_alb_target_group"
}

func registration(typ string) bool {
	switch typ {
	case "aws_lb_target_group_attachment", "aws_alb_target_group_attachment", "aws_autoscaling_attachment", "aws_autoscaling_group":
		return true
	}
	return false
}
