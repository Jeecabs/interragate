package access

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
)

func obj(t *testing.T, s string) map[string]any {
	t.Helper()
	if s == "" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func policy(doc string) string { return `{"policy":` + strconv.Quote(doc) + `}` }

func lines(ls []model.AccessLine) string {
	var out []string
	for _, l := range ls {
		w := ""
		if l.Wide {
			w = "!"
		}
		out = append(out, l.Op+w+" "+l.Text)
	}
	return strings.Join(out, "\n")
}

func check(t *testing.T, typ, before, after, want string) {
	t.Helper()
	got := lines(Diff(typ, obj(t, before), obj(t, after), nil))
	if got != want {
		t.Errorf("%s:\ngot:\n%s\nwant:\n%s", typ, got, want)
	}
}

func TestPolicies(t *testing.T) {
	read := `{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::orders/*"}`
	write := `{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],"Resource":"arn:aws:s3:::orders/*"}`
	list := `{"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::orders"}`

	// Reordering statements, or splitting one in two, allows nothing new.
	check(t, "aws_iam_policy", policy(`{"Statement":[`+read+`,`+list+`]}`), policy(`{"Statement":[`+list+`,`+read+`]}`), "")
	check(t, "aws_iam_policy", policy(`{"Statement":[`+write+`]}`),
		policy(`{"Statement":[`+read+`,{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::orders/*"}]}`), "")

	check(t, "aws_iam_policy", policy(`{"Statement":[`+read+`]}`), policy(`{"Statement":[`+write+`]}`),
		"+ Allows `s3:PutObject` on `arn:aws:s3:::orders/*`")
	check(t, "aws_iam_policy", policy(`{"Statement":[`+write+`]}`), policy(`{"Statement":{"Effect":"Allow","Action":"s3:*","Resource":"*"}}`),
		"+! Allows `s3:*` on every resource\n- Allows `s3:GetObject` and `s3:PutObject` on `arn:aws:s3:::orders/*`")
	check(t, "aws_iam_policy", "", policy(`{"Statement":[{"Effect":"Allow","NotAction":"iam:*","Resource":"*"}]}`),
		"+! Allows every action but `iam:*` on every resource")
	check(t, "aws_iam_policy", policy(`{"Statement":[`+read+`]}`), "", "- Allows `s3:GetObject` on `arn:aws:s3:::orders/*`")
}

func TestResourcePolicies(t *testing.T) {
	check(t, "aws_s3_bucket_policy", "", policy(`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::data/*",
		"Condition":{"IpAddress":{"aws:SourceIp":["10.0.0.0/8","192.168.0.0/16"]}}}]}`),
		"+ Lets account `111122223333` use `s3:GetObject` on `arn:aws:s3:::data/*` when `aws:SourceIp` is in `10.0.0.0/8` or `192.168.0.0/16`")
	check(t, "aws_s3_bucket_policy", "", policy(`{"Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::site/*"}]}`),
		"+! Lets anyone use `s3:GetObject` on `arn:aws:s3:::site/*`")
	check(t, "aws_s3_bucket_policy", "", policy(`{"Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:*","Resource":"arn:aws:s3:::site/*","Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}`),
		"+ Stops anyone using `s3:*` on `arn:aws:s3:::site/*` when `aws:SecureTransport` is `false`")

	trust := func(svc string) string {
		return `{"assume_role_policy":` + strconv.Quote(`{"Statement":[{"Effect":"Allow","Principal":{"Service":"`+svc+`"},"Action":"sts:AssumeRole"}]}`) + `}`
	}
	check(t, "aws_iam_role", trust("ec2.amazonaws.com"), trust("lambda.amazonaws.com"),
		"+ Lets `lambda.amazonaws.com` assume the role\n- Lets `ec2.amazonaws.com` assume the role")
}

func TestNetwork(t *testing.T) {
	sg := func(cidrs string) string {
		return `{"ingress":[{"from_port":22,"to_port":22,"protocol":"tcp","cidr_blocks":` + cidrs + `,"ipv6_cidr_blocks":[],"security_groups":[],"self":false}],
			"egress":[{"from_port":0,"to_port":0,"protocol":"-1","cidr_blocks":["0.0.0.0/0"]}]}`
	}
	check(t, "aws_security_group", sg(`["10.0.0.0/8"]`), sg(`["10.0.0.0/8","0.0.0.0/0"]`), "+! Inbound TCP 22 (SSH) from `0.0.0.0/0` (anywhere)")
	check(t, "aws_security_group", sg(`["10.0.0.0/8"]`), sg(`["172.16.0.0/12"]`), "+ Inbound TCP 22 (SSH) from `172.16.0.0/12`\n- Inbound TCP 22 (SSH) from `10.0.0.0/8`")
	check(t, "aws_security_group_rule", "", `{"type":"egress","from_port":1024,"to_port":65535,"protocol":"udp","cidr_blocks":["10.0.0.0/8"]}`,
		"+ Outbound UDP 1024–65535 to `10.0.0.0/8`")
	check(t, "aws_vpc_security_group_ingress_rule", "", `{"ip_protocol":"tcp","from_port":5432,"to_port":5432,"referenced_security_group_id":"sg-0abc"}`,
		"+ Inbound TCP 5432 (PostgreSQL) from `sg-0abc`")
	check(t, "aws_vpc_security_group_ingress_rule", `{"ip_protocol":"-1","cidr_ipv4":"10.0.0.0/8"}`, "", "- All inbound traffic from `10.0.0.0/8`")

	check(t, "google_compute_firewall", `{"direction":"INGRESS","source_ranges":["10.0.0.0/8"],"allow":[{"protocol":"tcp","ports":["443"]}]}`,
		`{"direction":"INGRESS","source_ranges":["10.0.0.0/8"],"source_tags":["bastion"],"allow":[{"protocol":"tcp","ports":["443","8000-8080"]}]}`,
		"+ Inbound TCP 443 (HTTPS) from instances tagged `bastion`\n+ Inbound TCP 8000–8080 from `10.0.0.0/8` and instances tagged `bastion`")
	check(t, "azurerm_network_security_rule", "", `{"direction":"Inbound","access":"Allow","protocol":"Tcp","destination_port_range":"3389","source_address_prefix":"*"}`,
		"+! Inbound TCP 3389 (RDP) from `0.0.0.0/0` (anywhere)")
	check(t, "azurerm_network_security_rule", "", `{"direction":"Inbound","access":"Deny","protocol":"*","destination_port_range":"*","source_address_prefix":"Internet"}`,
		"+ Blocks all inbound traffic from `0.0.0.0/0` (anywhere)")
}

// An attribute the plan does not know yet is left out on both sides.
func TestSkip(t *testing.T) {
	before := obj(t, `{"ingress":[{"from_port":22,"to_port":22,"protocol":"tcp","cidr_blocks":["10.0.0.0/8"]}]}`)
	if got := Diff("aws_security_group", before, map[string]any{}, func(a string) bool { return a == "ingress" }); len(got) != 0 {
		t.Errorf("unknown ingress: %v", got)
	}
}
