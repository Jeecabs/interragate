package access

import "testing"

func TestFirewallAdmits(t *testing.T) {
	sg := map[string]any{"ingress": []any{
		map[string]any{"protocol": "tcp", "from_port": 443.0, "to_port": 443.0, "cidr_blocks": []any{"0.0.0.0/0"}, "ipv6_cidr_blocks": []any{"::/0"}},
		map[string]any{"protocol": "6", "from_port": 8443.0, "to_port": 8443.0, "cidr_blocks": []any{"198.51.100.0/24"}},
		map[string]any{"protocol": "tcp", "from_port": 1000.0, "to_port": 1500.0, "cidr_blocks": []any{"10.0.0.0/8"}},
		map[string]any{"protocol": "tcp", "from_port": 1501.0, "to_port": 2000.0, "cidr_blocks": []any{"10.0.0.0/8"}},
		map[string]any{"protocol": "udp", "from_port": 53.0, "to_port": 53.0, "security_groups": []any{"sg-123"}},
	}}
	fw, ok := ReadFirewall("aws_security_group", sg)
	if !ok || len(fw) != 5 {
		t.Fatalf("firewall = %+v, %v", fw, ok)
	}
	port := func(s string) Port {
		p, err := ParsePort(s)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, c := range []struct {
		port, from string
		want       bool
	}{
		{"443", "203.0.113.9", true},
		{"443", "2001:db8::1", true},
		{"8443/tcp", "198.51.100.7", true},
		{"8443/tcp", "203.0.113.0/24", false},
		{"8443/udp", "198.51.100.7", false},
		{"1200-1800", "10.1.2.0/24", true}, // two rules together
		{"1200-2100", "10.1.2.0/24", false},
		{"53/udp", "sg-123", true},
		{"53/udp", "198.51.100.7", false}, // a security group cannot be checked against a range
	} {
		if got := fw.Admits(port(c.port), SourceOf(c.from)); got != c.want {
			t.Errorf("Admits(%s from %s) = %v, want %v", c.port, c.from, got, c.want)
		}
	}
	if got := fw.SourcesOf(port("8443/tcp")); len(got) != 1 || got[0].String() != "198.51.100.0/24" {
		t.Errorf("sources of 8443 = %v", got)
	}

	all, _ := ReadFirewall("aws_security_group", map[string]any{"ingress": []any{
		map[string]any{"protocol": "-1", "from_port": 0.0, "to_port": 0.0, "cidr_blocks": []any{"0.0.0.0/0"}},
	}})
	if !all.Admits(port("9999/udp"), SourceOf("198.51.100.7")) {
		t.Error("a rule for all traffic should admit any port")
	}
	if fw, ok := ReadFirewall("aws_security_group", nil); !ok || len(fw) != 0 {
		t.Errorf("a destroyed group lets nothing in: %+v, %v", fw, ok)
	}
	if _, ok := ReadFirewall("aws_instance", map[string]any{}); ok {
		t.Error("an instance is not a firewall")
	}
	egress, _ := ReadFirewall("aws_security_group_rule", map[string]any{"type": "egress", "protocol": "tcp", "from_port": 443.0, "to_port": 443.0, "cidr_blocks": []any{"0.0.0.0/0"}})
	if len(egress) != 0 {
		t.Errorf("an egress rule lets nothing in: %+v", egress)
	}
	gcp, _ := ReadFirewall("google_compute_firewall", map[string]any{"source_ranges": []any{"0.0.0.0/0"}, "allow": []any{
		map[string]any{"protocol": "tcp", "ports": []any{"80", "8000-8100"}},
	}})
	if !gcp.Admits(port("8080"), SourceOf("198.51.100.7")) || gcp.Admits(port("443"), SourceOf("198.51.100.7")) {
		t.Errorf("gcp = %+v", gcp)
	}
}

func TestParsePort(t *testing.T) {
	for in, want := range map[string]string{"8443": "8443/tcp", "8443/TCP": "8443/tcp", "53/udp": "53/udp", "1000-2000/udp": "1000-2000/udp"} {
		if got, err := ParsePort(in); err != nil || got.String() != want {
			t.Errorf("ParsePort(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"", "x", "2000-1000", "70000", "443/http", "443/6", "443/all"} {
		if _, err := ParsePort(in); err == nil {
			t.Errorf("ParsePort(%q) did not fail", in)
		}
	}
}
