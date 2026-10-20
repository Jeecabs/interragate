package rules

import (
	"fmt"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
)

// fightRules flag resources that two parts of the configuration manage
// against each other.
func fightRules() []*Rule {
	return []*Rule{
		{
			ID: "rules-fight", Title: "Inline rules and rule resources undo each other", Severity: model.Medium, Category: Correctness,
			Doc: "Flags a security group or route table whose plan removes an inline rule that a separate rule resource adds " +
				"(aws_security_group_rule, aws_vpc_security_group_ingress_rule, aws_route). The two take turns: each apply puts one back " +
				"and the next plan takes it out again, so the plan never comes up clean.",
			Check: checkRulesFight,
		},
	}
}

// A fight is an inline list on a parent resource and the resource types
// that manage the same entries separately.
type fight struct {
	attr     string   // the inline list, e.g. "ingress"
	children []string // resource types that add entries to it
	noun     string   // what an entry is called
}

var fights = map[string][]fight{
	"aws_security_group": {
		{"ingress", []string{"aws_security_group_rule", "aws_vpc_security_group_ingress_rule"}, "ingress rule"},
		{"egress", []string{"aws_security_group_rule", "aws_vpc_security_group_egress_rule"}, "egress rule"},
	},
	"aws_route_table": {
		{"route", []string{"aws_route"}, "route"},
	},
}

func checkRulesFight(c *Context) []*model.Finding {
	var out []*model.Finding
	if c.Graph == nil {
		return nil
	}
	for _, ch := range c.Changes(model.Update) {
		node := c.Graph.Nodes[ch.Resource]
		if node == nil {
			continue
		}
		for _, f := range fights[ch.Type] {
			removed := removedEntries(c.Before(ch)[f.attr], c.After(ch)[f.attr])
			if len(removed) == 0 {
				continue
			}
			// The separate resources that put one of those entries back.
			var who []string
			for _, id := range node.Down {
				n := c.Graph.Nodes[id]
				if n == nil || !hasString(f.children, n.Type) {
					continue
				}
				for _, child := range c.Instances(id) {
					obj := c.After(child)
					if obj == nil {
						obj = c.Before(child)
					}
					if child.Action == model.Delete || !addsEntry(n.Type, f.attr, obj, removed) {
						continue
					}
					if !hasString(who, child.Address) {
						who = append(who, child.Address)
					}
				}
			}
			if len(who) == 0 {
				continue
			}
			codes := make([]string, len(who))
			for i, a := range who {
				codes[i] = Code(a)
			}
			fd := finding(ch, "", fmt.Sprintf("%s removes %s that %s %s separately. Inline %ss and separate rule resources for the same %s undo each other: each apply puts one back and the next plan takes it out again.",
				Code(ch.Address), oneOrMany(len(removed), "an "+f.noun, fmt.Sprintf("%d %ss", len(removed), f.noun)), List(codes, 3), oneOrMany(len(who), "adds", "add"),
				f.noun, c.Info(ch).Noun))
			fd.Addresses = append([]string{ch.Address}, who...)
			fd.Short = "never plans clean: inline and separate rules fight"
			fd.Paths = []string{f.attr}
			fd.Remediation = fmt.Sprintf("Manage the %ss one way: move the inline %s blocks into rule resources, or the rule resources into the %s block.", f.noun, f.attr, f.attr)
			out = append(out, fd)
		}
	}
	return out
}

func oneOrMany(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func hasString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// removedEntries lists the entries of an inline list that the plan drops.
func removedEntries(before, after any) []map[string]any {
	bl, _ := before.([]any)
	al, _ := after.([]any)
	var out []map[string]any
	for _, b := range bl {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		kept := false
		for _, a := range al {
			if am, ok := a.(map[string]any); ok && sameEntry(bm, am) {
				kept = true
				break
			}
		}
		if !kept {
			out = append(out, bm)
		}
	}
	return out
}

// sameEntry compares the parts of a rule or route that identify it.
func sameEntry(a, b map[string]any) bool {
	for _, k := range []string{"from_port", "to_port", "protocol", "cidr_blocks", "ipv6_cidr_blocks", "security_groups", "self", "cidr_block", "ipv6_cidr_block", "destination_prefix_list_id"} {
		if fmt.Sprint(a[k]) != fmt.Sprint(b[k]) {
			return false
		}
	}
	return true
}

// addsEntry reports whether a separate rule resource adds one of the
// removed entries back.
func addsEntry(typ, attr string, obj map[string]any, removed []map[string]any) bool {
	if obj == nil {
		return false
	}
	switch typ {
	case "aws_security_group_rule":
		if String(obj, "type") != attr {
			return false
		}
		for _, r := range removed {
			if samePorts(r, obj, "protocol") && sharePeer(peers(r), peers(obj)) {
				return true
			}
		}
	case "aws_vpc_security_group_ingress_rule", "aws_vpc_security_group_egress_rule":
		for _, r := range removed {
			if samePorts(r, obj, "ip_protocol") && sharePeer(peers(r), peers(obj)) {
				return true
			}
		}
	case "aws_route":
		for _, r := range removed {
			if d := String(obj, "destination_cidr_block"); d != "" && d == String(r, "cidr_block") {
				return true
			}
		}
	}
	return false
}

func samePorts(entry, rule map[string]any, protoKey string) bool {
	norm := func(p any) string {
		s := strings.ToLower(fmt.Sprint(p))
		if s == "all" {
			return "-1"
		}
		return s
	}
	if norm(entry["protocol"]) != norm(rule[protoKey]) {
		return false
	}
	// A rule for every protocol covers every port, whatever its port
	// numbers say (0, or none at all on the newer rule resources).
	if norm(entry["protocol"]) == "-1" {
		return true
	}
	return atoi(entry["from_port"]) == atoi(rule["from_port"]) && atoi(entry["to_port"]) == atoi(rule["to_port"])
}

// peers lists where a rule lets traffic come from or go to, from an inline
// block or any of the rule resources: addresses, security groups, prefix
// lists, or the group itself.
func peers(obj map[string]any) []string {
	var out []string
	for _, k := range []string{"cidr_blocks", "ipv6_cidr_blocks", "cidr_ipv4", "cidr_ipv6"} {
		for _, c := range Strings(obj[k]) {
			out = append(out, "cidr:"+c)
		}
	}
	for _, k := range []string{"security_groups", "source_security_group_id", "referenced_security_group_id"} {
		for _, g := range Strings(obj[k]) {
			out = append(out, "sg:"+g)
		}
	}
	for _, k := range []string{"prefix_list_ids", "prefix_list_id"} {
		for _, p := range Strings(obj[k]) {
			out = append(out, "pl:"+p)
		}
	}
	if self, _ := obj["self"].(bool); self {
		out = append(out, "self")
	}
	return out
}

// sharePeer reports whether two rules let in any of the same peers.
func sharePeer(a, b []string) bool {
	for _, x := range a {
		if hasString(b, x) {
			return true
		}
	}
	return false
}
