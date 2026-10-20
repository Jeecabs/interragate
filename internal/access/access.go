// Package access says what a change to an IAM policy or a firewall allows,
// in words: the permissions and network paths it adds and removes, rather
// than the JSON documents and rule blocks that encode them, where a list
// growing by one entry or a statement moving can hide what really changed.
package access

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
)

// Diff compares what before and after allow, for a resource of type typ;
// either may be nil, for a create or a destroy. skip names top-level
// attributes to leave out, such as ones whose planned value is unknown.
func Diff(typ string, before, after map[string]any, skip func(attr string) bool) []model.AccessLine {
	if skip == nil {
		skip = func(string) bool { return false }
	}
	keep := func(obj map[string]any) map[string]any {
		if obj == nil {
			return nil
		}
		out := map[string]any{}
		for k, v := range obj {
			if !skip(k) {
				out[k] = v
			}
		}
		return out
	}
	// A skipped attribute is left out on both sides, or every grant it
	// holds would look removed.
	before, after = keep(before), keep(after)
	if rules := networkRules(typ); rules != nil {
		return networkDiff(rules(before), rules(after))
	}
	return policyDiff(typ, statementsIn(before), statementsIn(after))
}

// ---------------------------------------------------------------- IAM

// grant is one statement's effect on one resource, for some actions.
type grant struct {
	effect    string // Allow or Deny
	who       string // the principal, in words; empty in an identity policy
	resource  string
	condition string // in words; empty without one
	trust     bool   // a role's trust policy: who may assume the role
	wide      bool
}

func (g grant) key() string {
	return strings.Join([]string{g.effect, g.who, g.resource, g.condition}, "\x00")
}

type statementSet struct {
	grants  map[string]grant
	actions map[string]map[string]bool // by grant key
}

func (s *statementSet) add(g grant, action string) {
	if s.grants == nil {
		s.grants, s.actions = map[string]grant{}, map[string]map[string]bool{}
	}
	k := g.key()
	if _, ok := s.grants[k]; !ok {
		s.grants[k] = g
		s.actions[k] = map[string]bool{}
	}
	s.actions[k][action] = true
}

// statementsIn reads the policy documents an object holds, in any
// attribute, including JSON in string attributes.
func statementsIn(obj map[string]any) *statementSet {
	set := &statementSet{}
	var walk func(attr string, v any)
	walk = func(attr string, v any) {
		switch x := v.(type) {
		case string:
			t := strings.TrimSpace(x)
			if !strings.HasPrefix(t, "{") || !strings.Contains(t, "Statement") {
				return
			}
			dec := json.NewDecoder(strings.NewReader(t))
			dec.UseNumber()
			var doc map[string]any
			if dec.Decode(&doc) != nil {
				return
			}
			var stmts []any
			switch s := doc["Statement"].(type) {
			case []any:
				stmts = s
			case map[string]any:
				stmts = []any{s}
			}
			for _, e := range stmts {
				if m, ok := e.(map[string]any); ok {
					readStatement(set, m, attr == "assume_role_policy")
				}
			}
		case map[string]any:
			for k, e := range x {
				walk(k, e)
			}
		case []any:
			for _, e := range x {
				walk(attr, e)
			}
		}
	}
	for k, v := range obj {
		walk(k, v)
	}
	return set
}

func readStatement(set *statementSet, m map[string]any, trust bool) {
	effect, _ := m["Effect"].(string)
	if effect != "Deny" {
		effect = "Allow"
	}
	who, anyone := principal(m["Principal"])
	if p, ok := m["NotPrincipal"]; ok {
		w, _ := principal(p)
		who, anyone = "anyone but "+w, true
	}
	cond := condition(m["Condition"])
	actions := strs(m["Action"])
	if na := strs(m["NotAction"]); len(na) > 0 {
		actions = []string{"every action but " + codes(na)}
	}
	resources := strs(m["Resource"])
	if nr := strs(m["NotResource"]); len(nr) > 0 {
		resources = []string{"every resource but " + codes(nr)}
	}
	if len(resources) == 0 {
		resources = []string{""} // a trust policy, or a resource policy that leaves it out
	}
	for _, r := range resources {
		for _, a := range actions {
			wide := effect == "Allow" && cond == "" && (anyone || a == "*" || strings.HasPrefix(a, "every action") || r == "*" && strings.HasSuffix(a, ":*"))
			set.add(grant{effect: effect, who: who, resource: r, condition: cond, trust: trust, wide: wide}, a)
		}
	}
}

// policyDiff turns the grants before and after into lines: one per set of
// actions that was added or removed together, with the resources it
// applies to.
func policyDiff(typ string, before, after *statementSet) []model.AccessLine {
	type line struct {
		op, effect, who, condition, actions string
		trust, wide                         bool
		resources                           []string
	}
	groups := map[string]*line{}
	var order []string
	emit := func(op string, g grant, acts []string) {
		if len(acts) == 0 {
			return
		}
		sort.Strings(acts)
		joined := strings.Join(acts, "\x00")
		k := strings.Join([]string{op, g.effect, g.who, g.condition, joined}, "\x01")
		l := groups[k]
		if l == nil {
			l = &line{op: op, effect: g.effect, who: g.who, condition: g.condition, actions: joined, trust: g.trust}
			groups[k] = l
			order = append(order, k)
		}
		l.wide = l.wide || g.wide
		l.resources = append(l.resources, g.resource)
	}
	diff := func(op string, from, to *statementSet) {
		keys := make([]string, 0, len(from.grants))
		for k := range from.grants {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			var acts []string
			for a := range from.actions[k] {
				if to.actions[k] == nil || !to.actions[k][a] {
					acts = append(acts, a)
				}
			}
			emit(op, from.grants[k], acts)
		}
	}
	diff("+", after, before)
	diff("-", before, after)

	var out []model.AccessLine
	for _, k := range order {
		l := groups[k]
		acts := strings.Split(l.actions, "\x00")
		var text string
		switch {
		case l.trust && l.actions == "sts:AssumeRole":
			text = fmt.Sprintf("Lets %s assume the role", l.who)
			if l.effect == "Deny" {
				text = fmt.Sprintf("Stops %s assuming the role", l.who)
			}
		case l.who != "":
			verb := "Lets"
			if l.effect == "Deny" {
				verb = "Stops"
			}
			text = fmt.Sprintf("%s %s use %s", verb, l.who, actionList(acts))
			if l.effect == "Deny" {
				text = fmt.Sprintf("%s %s using %s", verb, l.who, actionList(acts))
			}
		default:
			verb := "Allows"
			if l.effect == "Deny" {
				verb = "Denies"
			}
			text = verb + " " + actionList(acts)
		}
		if on := resourceList(l.resources); on != "" {
			text += " on " + on
		}
		if l.condition != "" {
			text += " when " + l.condition
		}
		out = append(out, model.AccessLine{Op: l.op, Kind: "iam", Text: text, Wide: l.wide && l.op == "+"})
	}
	return out
}

func actionList(acts []string) string {
	if len(acts) == 1 && acts[0] == "*" {
		return "every action"
	}
	var out []string
	for _, a := range acts {
		if strings.HasPrefix(a, "every action") {
			out = append(out, a)
		} else {
			out = append(out, "`"+a+"`")
		}
	}
	return list(out, 6)
}

func resourceList(rs []string) string {
	sort.Strings(rs)
	var out []string
	for _, r := range rs {
		switch {
		case r == "":
			continue
		case r == "*":
			out = append(out, "every resource")
		case strings.HasPrefix(r, "every resource"):
			out = append(out, r)
		default:
			out = append(out, "`"+r+"`")
		}
	}
	return list(out, 4)
}

// principal says who a statement's Principal names, and whether that is
// anyone at all.
func principal(p any) (string, bool) {
	switch x := p.(type) {
	case nil:
		return "", false
	case string:
		if x == "*" {
			return "anyone", true
		}
		return "`" + x + "`", false
	case map[string]any:
		var out []string
		anyone := false
		kinds := make([]string, 0, len(x))
		for k := range x {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			for _, v := range strs(x[k]) {
				switch {
				case v == "*":
					out = append(out, "anyone")
					anyone = true
				case k == "AWS" && strings.HasSuffix(v, ":root") && strings.HasPrefix(v, "arn:"):
					out = append(out, "account `"+strings.Split(v, ":")[4]+"`")
				case k == "AWS" && isAccountID(v):
					out = append(out, "account `"+v+"`")
				default:
					out = append(out, "`"+v+"`")
				}
			}
		}
		return list(out, 4), anyone
	}
	return "", false
}

func isAccountID(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// condition says a statement's Condition in words: "`aws:SourceIp` is
// `10.0.0.0/8`".
func condition(c any) string {
	m, ok := c.(map[string]any)
	if !ok || len(m) == 0 {
		return ""
	}
	ops := make([]string, 0, len(m))
	for op := range m {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	var parts []string
	for _, op := range ops {
		keys, ok := m[op].(map[string]any)
		if !ok {
			continue
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		base := strings.TrimSuffix(op[strings.Index(op, ":")+1:], "IfExists")
		for _, k := range names {
			var vals []string
			for _, v := range strs(keys[k]) {
				vals = append(vals, "`"+v+"`")
			}
			verb := map[string]string{
				"StringEquals": "is", "StringEqualsIgnoreCase": "is", "ArnEquals": "is", "IpAddress": "is in", "NumericEquals": "is", "Bool": "is", "DateEquals": "is",
				"StringLike": "matches", "ArnLike": "matches",
				"StringNotEquals": "is not", "StringNotEqualsIgnoreCase": "is not", "ArnNotEquals": "is not", "NotIpAddress": "is not in", "NumericNotEquals": "is not",
				"StringNotLike": "does not match", "ArnNotLike": "does not match",
				"NumericLessThan": "is under", "NumericGreaterThan": "is over", "DateLessThan": "is before", "DateGreaterThan": "is after",
				"Null": "is missing:",
			}[base]
			if verb == "" {
				verb = "`" + op + "`"
			}
			parts = append(parts, fmt.Sprintf("`%s` %s %s", k, verb, strings.Join(vals, " or ")))
		}
	}
	return strings.Join(parts, " and ")
}

// ---------------------------------------------------------------- network

// path is a way through a firewall: what it lets through, in which
// direction, between where.
type path struct {
	dir   string // Inbound or Outbound
	allow bool
	proto string // in words: TCP, UDP, ICMP, all traffic
	ports string // in words; empty for all traffic
	peer  string // where from (inbound) or to (outbound)
}

func networkRules(typ string) func(map[string]any) []path {
	switch typ {
	case "aws_security_group":
		return func(obj map[string]any) []path {
			var out []path
			for _, dir := range []string{"ingress", "egress"} {
				for _, r := range objects(obj[dir]) {
					out = append(out, awsRule(dir, r, "security_groups", "")...)
				}
			}
			return out
		}
	case "aws_security_group_rule":
		return func(obj map[string]any) []path {
			if obj == nil {
				return nil
			}
			return awsRule(str(obj["type"]), obj, "", "source_security_group_id")
		}
	case "aws_vpc_security_group_ingress_rule", "aws_vpc_security_group_egress_rule":
		dir := "ingress"
		if strings.HasSuffix(typ, "egress_rule") {
			dir = "egress"
		}
		return func(obj map[string]any) []path {
			if obj == nil {
				return nil
			}
			proto, ports := protoPorts(str(obj["ip_protocol"]), obj["from_port"], obj["to_port"])
			var peers []string
			for _, k := range []string{"cidr_ipv4", "cidr_ipv6", "referenced_security_group_id", "prefix_list_id"} {
				if v := str(obj[k]); v != "" {
					peers = append(peers, v)
				}
			}
			return paths(dirWord(dir), true, proto, ports, peers)
		}
	case "google_compute_firewall":
		return func(obj map[string]any) []path {
			if obj == nil {
				return nil
			}
			dir := "Inbound"
			peers := append(strs(obj["source_ranges"]), prefixed("tag:", strs(obj["source_tags"]))...)
			if strings.EqualFold(str(obj["direction"]), "EGRESS") {
				dir, peers = "Outbound", strs(obj["destination_ranges"])
			}
			var out []path
			for _, kind := range []string{"allow", "deny"} {
				for _, r := range objects(obj[kind]) {
					ports := strs(r["ports"])
					if len(ports) == 0 {
						ports = []string{""}
					}
					for _, p := range ports {
						proto, words := gcpProtoPorts(str(r["protocol"]), p)
						out = append(out, paths(dir, kind == "allow", proto, words, peers)...)
					}
				}
			}
			return out
		}
	case "azurerm_network_security_rule":
		return func(obj map[string]any) []path {
			if obj == nil {
				return nil
			}
			return azureRule(obj)
		}
	case "azurerm_network_security_group":
		return func(obj map[string]any) []path {
			var out []path
			for _, r := range objects(obj["security_rule"]) {
				out = append(out, azureRule(r)...)
			}
			return out
		}
	}
	return nil
}

func awsRule(dir string, r map[string]any, groupsKey, groupKey string) []path {
	proto, ports := protoPorts(str(r["protocol"]), r["from_port"], r["to_port"])
	var peers []string
	peers = append(peers, strs(r["cidr_blocks"])...)
	peers = append(peers, strs(r["ipv6_cidr_blocks"])...)
	peers = append(peers, strs(r["prefix_list_ids"])...)
	if groupsKey != "" {
		peers = append(peers, strs(r[groupsKey])...)
	}
	if groupKey != "" {
		if g := str(r[groupKey]); g != "" {
			peers = append(peers, g)
		}
	}
	if self, _ := r["self"].(bool); self {
		peers = append(peers, "itself")
	}
	return paths(dirWord(dir), true, proto, ports, peers)
}

func azureRule(r map[string]any) []path {
	dir := "Inbound"
	if strings.EqualFold(str(r["direction"]), "Outbound") {
		dir = "Outbound"
	}
	proto := strings.ToUpper(str(r["protocol"]))
	if proto == "*" || proto == "" {
		proto = "all traffic"
	}
	ports := strs(r["destination_port_ranges"])
	if p := str(r["destination_port_range"]); p != "" {
		ports = append(ports, p)
	}
	addrKey := "source_address_prefix"
	if dir == "Outbound" {
		addrKey = "destination_address_prefix"
	}
	peers := strs(r[addrKey+"es"])
	if p := str(r[addrKey]); p != "" {
		peers = append(peers, p)
	}
	allow := !strings.EqualFold(str(r["access"]), "Deny")
	var out []path
	for _, p := range ports {
		words := portWords(p)
		if p == "*" {
			words = "all ports"
		}
		if proto == "all traffic" {
			words = portsOrEmpty(p)
		}
		out = append(out, paths(dir, allow, proto, words, peers)...)
	}
	return out
}

func portsOrEmpty(p string) string {
	if p == "*" || p == "" {
		return ""
	}
	return portWords(p)
}

func paths(dir string, allow bool, proto, ports string, peers []string) []path {
	var out []path
	for _, p := range peers {
		if p == "*" || p == "Internet" {
			p = "0.0.0.0/0"
		}
		out = append(out, path{dir: dir, allow: allow, proto: proto, ports: ports, peer: p})
	}
	return out
}

func dirWord(awsDir string) string {
	if awsDir == "egress" {
		return "Outbound"
	}
	return "Inbound"
}

// protoPorts says an AWS rule's protocol and port range in words.
func protoPorts(proto string, from, to any) (string, string) {
	switch strings.ToLower(proto) {
	case "-1", "all", "":
		return "all traffic", ""
	case "tcp", "6":
		proto = "TCP"
	case "udp", "17":
		proto = "UDP"
	case "icmp", "1":
		return "ICMP", ""
	case "icmpv6", "58":
		return "ICMPv6", ""
	default:
		proto = strings.ToUpper(proto)
	}
	f, t := num(from), num(to)
	switch {
	case f <= 0 && (t <= 0 || t == 65535):
		return proto, "all ports"
	case f == t:
		return proto, portWords(fmt.Sprint(f))
	}
	return proto, fmt.Sprintf("%d–%d", f, t)
}

func gcpProtoPorts(proto, ports string) (string, string) {
	switch strings.ToLower(proto) {
	case "all":
		return "all traffic", ""
	case "icmp":
		return "ICMP", ""
	}
	if ports == "" {
		return strings.ToUpper(proto), "all ports"
	}
	return strings.ToUpper(proto), portWords(ports)
}

// wellKnown names the services behind common ports, so "22" reads as SSH.
var wellKnown = map[string]string{
	"22": "SSH", "80": "HTTP", "443": "HTTPS", "3389": "RDP", "3306": "MySQL", "5432": "PostgreSQL", "1433": "SQL Server",
	"1521": "Oracle", "6379": "Redis", "11211": "Memcached", "27017": "MongoDB", "9200": "Elasticsearch", "5439": "Redshift",
	"2049": "NFS", "25": "SMTP", "53": "DNS", "23": "Telnet", "21": "FTP", "445": "SMB", "5900": "VNC", "9092": "Kafka", "2379": "etcd",
}

func portWords(p string) string {
	p = strings.ReplaceAll(p, "-", "–")
	if name := wellKnown[p]; name != "" {
		return p + " (" + name + ")"
	}
	return p
}

// networkDiff turns the paths before and after into lines, one per
// direction, protocol and ports, listing the peers added or removed.
func networkDiff(before, after []path) []model.AccessLine {
	type key struct {
		op, dir, proto, ports string
		allow                 bool
	}
	in := func(ps []path) map[path]bool {
		m := map[path]bool{}
		for _, p := range ps {
			m[p] = true
		}
		return m
	}
	b, a := in(before), in(after)
	groups := map[key][]string{}
	var order []key
	add := func(op string, p path) {
		k := key{op, p.dir, p.proto, p.ports, p.allow}
		if groups[k] == nil {
			order = append(order, k)
		}
		groups[k] = append(groups[k], p.peer)
	}
	for _, p := range after {
		if !b[p] {
			add("+", p)
			b[p] = true // once each
		}
	}
	for _, p := range before {
		if !a[p] {
			add("-", p)
			a[p] = true
		}
	}
	var out []model.AccessLine
	for _, k := range order {
		peers := groups[k]
		sort.Strings(peers)
		wide := false
		var named []string
		for _, p := range peers {
			if p == "0.0.0.0/0" || p == "::/0" {
				wide = true
				named = append(named, "`"+p+"` (anywhere)")
				continue
			}
			if p == "itself" {
				named = append(named, "the group itself")
				continue
			}
			if tag, ok := strings.CutPrefix(p, "tag:"); ok {
				named = append(named, "instances tagged `"+tag+"`")
				continue
			}
			named = append(named, "`"+p+"`")
		}
		what := k.proto
		if k.ports != "" {
			what += " " + k.ports
		}
		dir := "from"
		if k.dir == "Outbound" {
			dir = "to"
		}
		// "Inbound TCP 22 (SSH) from …", "All inbound traffic from …",
		// "Blocks inbound TCP 22 (SSH) from …".
		lower := strings.ToLower(k.dir)
		text := fmt.Sprintf("%s %s %s %s", k.dir, what, dir, list(named, 5))
		if k.proto == "all traffic" {
			text = fmt.Sprintf("All %s traffic %s %s", lower, dir, list(named, 5))
		}
		if !k.allow {
			text = "Blocks " + strings.ToLower(text[:1]) + text[1:]
		}
		out = append(out, model.AccessLine{Op: k.op, Kind: "network", Text: text, Wide: wide && k.allow && k.op == "+" && k.dir == "Inbound"})
	}
	return out
}

// ---------------------------------------------------------------- values

func objects(v any) []map[string]any {
	var out []map[string]any
	if list, ok := v.([]any); ok {
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func strs(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			} else if e != nil {
				out = append(out, fmt.Sprint(e))
			}
		}
		return out
	}
	return nil
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func num(v any) int {
	switch x := v.(type) {
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}

func prefixed(p string, xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = p + x
	}
	return out
}

func codes(xs []string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "`" + x + "`"
	}
	return list(out, 4)
}

// list joins items as "a, b and c", eliding beyond max.
func list(items []string, max int) string {
	if len(items) > max {
		items = append(items[:max-1:max-1], fmt.Sprintf("%d more", len(items)-max+1))
	}
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
