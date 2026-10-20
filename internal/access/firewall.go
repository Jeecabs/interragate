package access

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Protocol is a network protocol a firewall rule lets through.
type Protocol int

// Protocols. The zero value is not one.
const (
	ProtocolUnknown Protocol = iota
	// ProtocolAny is every protocol, as a rule for all traffic allows.
	ProtocolAny
	ProtocolTCP
	ProtocolUDP
	ProtocolICMP
)

// parseProtocol reads a protocol as AWS and Google Cloud write it: by name
// or by number, with -1 or all for every protocol.
func parseProtocol(name string) Protocol {
	switch strings.ToLower(name) {
	case "-1", "all", "":
		return ProtocolAny
	case "tcp", "6":
		return ProtocolTCP
	case "udp", "17":
		return ProtocolUDP
	case "icmp", "1":
		return ProtocolICMP
	}
	return ProtocolUnknown
}

func (p Protocol) String() string {
	switch p {
	case ProtocolUnknown:
		return "unknown"
	case ProtocolAny:
		return "all"
	case ProtocolTCP:
		return "tcp"
	case ProtocolUDP:
		return "udp"
	case ProtocolICMP:
		return "icmp"
	}
	return fmt.Sprintf("Protocol(%d)", int(p))
}

// covers reports whether a rule for protocol p lets through protocol q.
func (p Protocol) covers(q Protocol) bool { return p == ProtocolAny || p == q }

// PortRange is a range of ports, both ends included.
type PortRange struct{ From, To int }

// everyPort is the range a rule without ports covers.
func everyPort() PortRange { return PortRange{From: 0, To: 65535} }

func (r PortRange) String() string {
	if r.From == r.To {
		return strconv.Itoa(r.From)
	}
	return strconv.Itoa(r.From) + "-" + strconv.Itoa(r.To)
}

// Port is a way in that a service uses: a protocol and a range of ports.
type Port struct {
	Protocol Protocol
	Range    PortRange
}

// ParsePort reads a port, such as 8443, 8443/tcp or 1000-2000/udp. The
// protocol is tcp unless given.
func ParsePort(s string) (Port, error) {
	bad := fmt.Errorf("%q is not a port; use one such as 8443/tcp or 1000-2000/udp", s)
	ports, name, _ := strings.Cut(strings.ToLower(strings.TrimSpace(s)), "/")
	var proto Protocol
	switch name {
	case "tcp", "":
		proto = ProtocolTCP
	case "udp":
		proto = ProtocolUDP
	case "icmp":
		proto = ProtocolICMP
	default:
		return Port{}, bad
	}
	r, ok := parseRange(ports)
	if !ok {
		return Port{}, bad
	}
	return Port{Protocol: proto, Range: r}, nil
}

// parseRange reads a port or a range of them, such as 443 or 1000-2000.
func parseRange(s string) (PortRange, bool) {
	lo, hi, isRange := strings.Cut(s, "-")
	from, err := strconv.Atoi(lo)
	if err != nil {
		return PortRange{}, false
	}
	to := from
	if isRange {
		if to, err = strconv.Atoi(hi); err != nil {
			return PortRange{}, false
		}
	}
	if from < 0 || to > 65535 || to < from {
		return PortRange{}, false
	}
	return PortRange{From: from, To: to}, true
}

func (p Port) String() string { return p.Range.String() + "/" + p.Protocol.String() }

// Source is where a firewall rule lets traffic in from: an address range,
// or a group that no range can be checked against, such as a security
// group, a prefix list, a network tag or the firewall itself.
type Source struct {
	Range netip.Prefix // valid for an address range
	Group string       // otherwise
}

// SourceOf reads a source as a firewall rule names it.
func SourceOf(s string) Source {
	if p, err := netip.ParsePrefix(s); err == nil {
		return Source{Range: p.Masked()}
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return Source{Range: netip.PrefixFrom(a, a.BitLen())}
	}
	return Source{Group: s}
}

// ParseRange reads an address or an address range, such as 203.0.113.7 or
// 198.51.100.0/24, as a source.
func ParseRange(s string) (Source, error) {
	src := SourceOf(strings.TrimSpace(s))
	if !src.Range.IsValid() {
		return Source{}, errors.New("is not an address or range; use one such as 198.51.100.0/24")
	}
	return src, nil
}

func (s Source) String() string {
	if s.Range.IsValid() {
		return s.Range.String()
	}
	return s.Group
}

// Contains reports whether the source lets in everything other does: a
// range that holds the other range, or the same group.
func (s Source) Contains(other Source) bool {
	switch {
	case s.Range.IsValid() && other.Range.IsValid():
		return s.Range.Addr().Is4() == other.Range.Addr().Is4() && s.Range.Bits() <= other.Range.Bits() && s.Range.Contains(other.Range.Addr())
	case !s.Range.IsValid() && !other.Range.IsValid():
		return s.Group == other.Group
	}
	return false
}

// Opening is one way in that a firewall allows.
type Opening struct {
	Protocol Protocol
	Ports    PortRange
	Sources  []Source
}

// Firewall is what a firewall resource lets in.
type Firewall []Opening

// ReadFirewall reads what a firewall resource lets in: an AWS security
// group or one of its rules, or a Google Cloud firewall. ok is false for any
// other type. A Google firewall's deny rules and priorities are not
// weighed, so what it reads is what the allow rules open.
func ReadFirewall(typ string, obj map[string]any) (fw Firewall, ok bool) {
	switch typ {
	case "aws_security_group":
		for _, r := range objects(obj["ingress"]) {
			fw = append(fw, awsOpening(r, strs(r["security_groups"])))
		}
	case "aws_security_group_rule":
		if obj != nil && strings.EqualFold(str(obj["type"]), "ingress") {
			var groups []string
			if g := str(obj["source_security_group_id"]); g != "" {
				groups = append(groups, g)
			}
			fw = append(fw, awsOpening(obj, groups))
		}
	case "aws_vpc_security_group_ingress_rule":
		if obj != nil {
			o := Opening{Protocol: parseProtocol(str(obj["ip_protocol"]))}
			o.Ports = portsOf(o.Protocol, obj["from_port"], obj["to_port"])
			for _, k := range []string{"cidr_ipv4", "cidr_ipv6", "referenced_security_group_id", "prefix_list_id"} {
				if v := str(obj[k]); v != "" {
					o.Sources = append(o.Sources, SourceOf(v))
				}
			}
			fw = append(fw, o)
		}
	case "google_compute_firewall":
		if obj == nil || strings.EqualFold(str(obj["direction"]), "EGRESS") {
			return nil, true
		}
		var sources []Source
		for _, r := range strs(obj["source_ranges"]) {
			sources = append(sources, SourceOf(r))
		}
		for _, t := range strs(obj["source_tags"]) {
			sources = append(sources, Source{Group: "tag:" + t})
		}
		for _, r := range objects(obj["allow"]) {
			proto := parseProtocol(str(r["protocol"]))
			ranges := strs(r["ports"])
			if len(ranges) == 0 {
				fw = append(fw, Opening{Protocol: proto, Ports: everyPort(), Sources: sources})
			}
			for _, p := range ranges {
				if r, ok := parseRange(p); ok {
					fw = append(fw, Opening{Protocol: proto, Ports: r, Sources: sources})
				}
			}
		}
	default:
		return nil, false
	}
	return fw, true
}

// FirewallAttributes are the attributes ReadFirewall reads for a type.
// When one is known only after apply, so is what the firewall lets in.
func FirewallAttributes(typ string) []string {
	switch typ {
	case "aws_security_group":
		return []string{"ingress"}
	case "aws_security_group_rule":
		return []string{"type", "protocol", "from_port", "to_port", "cidr_blocks", "ipv6_cidr_blocks", "prefix_list_ids", "source_security_group_id", "self"}
	case "aws_vpc_security_group_ingress_rule":
		return []string{"ip_protocol", "from_port", "to_port", "cidr_ipv4", "cidr_ipv6", "referenced_security_group_id", "prefix_list_id"}
	case "google_compute_firewall":
		return []string{"direction", "allow", "source_ranges", "source_tags"}
	}
	return nil
}

func awsOpening(r map[string]any, groups []string) Opening {
	o := Opening{Protocol: parseProtocol(str(r["protocol"]))}
	o.Ports = portsOf(o.Protocol, r["from_port"], r["to_port"])
	for _, k := range []string{"cidr_blocks", "ipv6_cidr_blocks", "prefix_list_ids"} {
		for _, v := range strs(r[k]) {
			o.Sources = append(o.Sources, SourceOf(v))
		}
	}
	for _, g := range groups {
		o.Sources = append(o.Sources, Source{Group: g})
	}
	if self, _ := r["self"].(bool); self {
		o.Sources = append(o.Sources, Source{Group: "self"})
	}
	return o
}

// portsOf reads a rule's port range. A rule for every protocol, or one that
// leaves its ports open, covers every port.
func portsOf(proto Protocol, from, to any) PortRange {
	f, t := num(from), num(to)
	if proto == ProtocolAny || f <= 0 && (t <= 0 || t == 65535) {
		return everyPort()
	}
	return PortRange{From: f, To: max(f, t)}
}

// Admits reports whether the firewall lets traffic in on every port of
// port, from everywhere from covers. Openings for one source may add up:
// 1000-1500 and 1501-2000 admit 1200-1800.
func (f Firewall) Admits(port Port, from Source) bool {
	var spans []PortRange
	for _, o := range f {
		if !o.Protocol.covers(port.Protocol) || !slices.ContainsFunc(o.Sources, func(s Source) bool { return s.Contains(from) }) {
			continue
		}
		if port.Protocol == ProtocolICMP {
			return true
		}
		spans = append(spans, o.Ports)
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].From < spans[j].From })
	next := port.Range.From
	for _, s := range spans {
		if s.From > next {
			break
		}
		next = max(next, s.To+1)
		if next > port.Range.To {
			return true
		}
	}
	return false
}

// SourcesOf lists where the firewall lets port in from, in full.
func (f Firewall) SourcesOf(port Port) []Source {
	var out []Source
	for _, o := range f {
		if !o.Protocol.covers(port.Protocol) {
			continue
		}
		for _, s := range o.Sources {
			if !slices.Contains(out, s) && f.Admits(port, s) {
				out = append(out, s)
			}
		}
	}
	return out
}
