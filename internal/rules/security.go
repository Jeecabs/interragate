package rules

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
)

// securityRules flag access this change opens: to the internet, to other
// accounts, to everyone.
func securityRules() []*Rule {
	return []*Rule{
		{
			ID: "public-ingress", Title: "Opens a port to the internet", Severity: model.High, Category: Security,
			Doc: "Flags security group, firewall and network security rules that this plan adds or widens to accept traffic from anywhere " +
				"(0.0.0.0/0 or ::/0). Remote access and database ports are critical; HTTP and HTTPS are not reported.",
			Check: checkPublicIngress,
		},
		{
			ID: "iam-wildcard", Title: "Grants wildcard permissions", Severity: model.High, Category: Security,
			Doc: "Reads IAM policy documents, including JSON held in strings, and flags statements this plan adds that allow every action " +
				"(\"*\"), every action of a service (\"s3:*\") on every resource, NotAction, or iam:PassRole on any role.",
			Check: checkIAMWildcard,
		},
		{
			ID: "public-principal", Title: "Lets anyone use a resource", Severity: model.High, Category: Security,
			Doc: "Flags policy statements this plan adds that allow Principal \"*\" without a Condition: a public bucket, queue, topic or key, " +
				"or a role any AWS account can assume.",
			Check: checkPublicPrincipal,
		},
		{
			ID: "admin-access", Title: "Grants administrator access", Severity: model.High, Category: Security,
			Doc:   "Flags attaching AdministratorAccess, granting roles/owner or roles/editor, Owner role assignments and cluster-admin bindings.",
			Check: checkAdminAccess,
		},
		{
			ID: "public-bucket", Title: "Makes storage public", Severity: model.High, Category: Security,
			Doc:   "Flags turning off S3 public access blocks, public ACLs, allUsers bindings on buckets and public blob containers.",
			Check: checkPublicBucket,
		},
		{
			ID: "public-database", Title: "Exposes a database publicly", Severity: model.High, Category: Security,
			Doc:   "Flags databases made publicly accessible and database firewall rules that allow every address.",
			Check: checkPublicDatabase,
		},
		{
			ID: "encryption-disabled", Title: "Stores data unencrypted", Severity: model.Medium, Category: Security,
			Doc:   "Flags new or changed storage with encryption at rest turned off.",
			Check: checkEncryptionDisabled,
		},
		{
			ID: "secret-output", Title: "Secret-looking output is not sensitive", Severity: model.Medium, Category: Security,
			Doc:   "Flags root outputs whose name suggests a secret but which are not marked sensitive, so the value is printed in plan and apply output.",
			Check: checkSecretOutput,
		},
		{
			ID: "plaintext-parameter", Title: "Secret stored as plain text", Severity: model.Medium, Category: Security,
			Doc:   "Flags SSM parameters whose name suggests a secret but whose type is String rather than SecureString.",
			Check: checkPlaintextParameter,
		},
	}
}

// Ports whose exposure to the internet is almost never intended.
var sensitivePorts = map[int]string{
	20: "FTP", 21: "FTP", 22: "SSH", 23: "Telnet", 135: "RPC", 139: "NetBIOS", 445: "SMB", 1433: "SQL Server",
	1521: "Oracle", 2049: "NFS", 2375: "the Docker API", 2376: "the Docker API", 2379: "etcd", 3306: "MySQL",
	3389: "RDP", 5432: "PostgreSQL", 5601: "Kibana", 5900: "VNC", 5984: "CouchDB", 6379: "Redis", 6443: "the Kubernetes API",
	8200: "Vault", 8500: "Consul", 9042: "Cassandra", 9092: "Kafka", 9200: "Elasticsearch", 10250: "the kubelet",
	11211: "Memcached", 27017: "MongoDB",
}

type ingress struct {
	from, to int // -1 for "all"
	proto    string
	source   string
	desc     string
}

func (r ingress) key() string { return fmt.Sprintf("%d-%d/%s/%s", r.from, r.to, r.proto, r.source) }

func isPublicCIDR(s string) bool {
	switch strings.TrimSpace(s) {
	case "0.0.0.0/0", "::/0", "*", "Internet", "Any", "0.0.0.0":
		return true
	}
	return false
}

func atoi(v any) int {
	switch x := v.(type) {
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			f, _ := x.Float64()
			return int(f)
		}
		return int(n)
	case float64:
		return int(x)
	case int:
		return x
	case string:
		n, err := strconv.Atoi(x)
		if err != nil {
			return -1
		}
		return n
	}
	return -1
}

// portRanges parses "22", "1000-2000" and "*".
func portRanges(specs []string) [][2]int {
	var out [][2]int
	for _, s := range specs {
		s = strings.TrimSpace(s)
		if s == "" || s == "*" {
			out = append(out, [2]int{-1, -1})
			continue
		}
		if lo, hi, ok := strings.Cut(s, "-"); ok {
			out = append(out, [2]int{atoi(lo), atoi(hi)})
			continue
		}
		n := atoi(s)
		out = append(out, [2]int{n, n})
	}
	return out
}

// publicIngress extracts the rules of an object that accept traffic from
// anywhere.
func publicIngress(typ string, obj map[string]any) []ingress {
	if obj == nil {
		return nil
	}
	var out []ingress
	awsRule := func(m map[string]any, cidrKeys ...string) {
		proto := strings.ToLower(fmt.Sprint(m["protocol"]))
		if p, ok := m["ip_protocol"]; ok {
			proto = strings.ToLower(fmt.Sprint(p))
		}
		for _, k := range cidrKeys {
			for _, cidr := range Strings(m[k]) {
				if isPublicCIDR(cidr) {
					out = append(out, ingress{from: atoi(m["from_port"]), to: atoi(m["to_port"]), proto: proto, source: cidr, desc: fmt.Sprint(valueOr(m["description"], ""))})
				}
			}
		}
	}
	switch typ {
	case "aws_security_group", "aws_default_security_group":
		if l, ok := obj["ingress"].([]any); ok {
			for _, e := range l {
				if m, ok := e.(map[string]any); ok {
					awsRule(m, "cidr_blocks", "ipv6_cidr_blocks")
				}
			}
		}
	case "aws_security_group_rule":
		if String(obj, "type") == "ingress" {
			awsRule(obj, "cidr_blocks", "ipv6_cidr_blocks")
		}
	case "aws_vpc_security_group_ingress_rule":
		awsRule(obj, "cidr_ipv4", "cidr_ipv6")
	case "google_compute_firewall":
		if d := String(obj, "direction"); d != "" && d != "INGRESS" {
			return nil
		}
		if disabled, _ := Bool(obj, "disabled"); disabled {
			return nil
		}
		var public []string
		for _, r := range Strings(obj["source_ranges"]) {
			if isPublicCIDR(r) {
				public = append(public, r)
			}
		}
		if len(public) == 0 {
			return nil
		}
		allows, _ := obj["allow"].([]any)
		for _, a := range allows {
			m, ok := a.(map[string]any)
			if !ok {
				continue
			}
			proto := strings.ToLower(String(m, "protocol"))
			ports := Strings(m["ports"])
			if len(ports) == 0 {
				ports = []string{"*"}
			}
			for _, pr := range portRanges(ports) {
				out = append(out, ingress{from: pr[0], to: pr[1], proto: proto, source: public[0]})
			}
		}
	case "azurerm_network_security_rule":
		out = append(out, azureRule(obj)...)
	case "azurerm_network_security_group":
		if l, ok := obj["security_rule"].([]any); ok {
			for _, e := range l {
				if m, ok := e.(map[string]any); ok {
					out = append(out, azureRule(m)...)
				}
			}
		}
	}
	return out
}

func azureRule(m map[string]any) []ingress {
	if !strings.EqualFold(String(m, "direction"), "Inbound") || !strings.EqualFold(String(m, "access"), "Allow") {
		return nil
	}
	sources := append(Strings(m["source_address_prefix"]), Strings(m["source_address_prefixes"])...)
	public := ""
	for _, s := range sources {
		if isPublicCIDR(s) {
			public = s
		}
	}
	if public == "" {
		return nil
	}
	ports := append(Strings(m["destination_port_range"]), Strings(m["destination_port_ranges"])...)
	proto := strings.ToLower(String(m, "protocol"))
	var out []ingress
	for _, pr := range portRanges(ports) {
		out = append(out, ingress{from: pr[0], to: pr[1], proto: proto, source: public, desc: String(m, "description")})
	}
	return out
}

func valueOr(v, def any) any {
	if v == nil {
		return def
	}
	return v
}

// classify names what a public rule exposes and how bad it is.
func (r ingress) classify() (what string, sev model.Severity, report bool) {
	allProto := r.proto == "-1" || r.proto == "all" || r.proto == "*"
	allPorts := allProto || r.from < 0 || (r.from <= 1 && r.to >= 65535)
	if r.proto == "icmp" || r.proto == "icmpv6" || r.proto == "1" || r.proto == "58" {
		return "", model.None, false
	}
	if allPorts {
		return "every port", model.Critical, true
	}
	var hits []int
	for p := range sensitivePorts {
		if p >= r.from && p <= r.to {
			hits = append(hits, p)
		}
	}
	sort.Ints(hits)
	proto := r.proto
	if proto == "" || proto == "*" {
		proto = "tcp"
	}
	if len(hits) > 0 {
		name := sensitivePorts[hits[0]]
		if r.from == r.to {
			return fmt.Sprintf("%s (%d/%s)", name, r.from, proto), model.Critical, true
		}
		return fmt.Sprintf("ports %d-%d/%s, including %s", r.from, r.to, proto, name), model.Critical, true
	}
	if (r.from == 80 || r.from == 443) && r.from == r.to {
		return "", model.None, false
	}
	if r.from == 80 && r.to == 443 {
		return "", model.None, false
	}
	if r.from == r.to {
		return fmt.Sprintf("port %d/%s", r.from, proto), model.High, true
	}
	return fmt.Sprintf("ports %d-%d/%s", r.from, r.to, proto), model.High, true
}

func checkPublicIngress(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Create, model.Update, model.Replace) {
		after := publicIngress(ch.Type, c.After(ch))
		if len(after) == 0 {
			continue
		}
		existing := map[string]bool{}
		if ch.Action != model.Create {
			for _, r := range publicIngress(ch.Type, c.Before(ch)) {
				existing[r.key()] = true
			}
		}
		for _, r := range after {
			if existing[r.key()] {
				continue
			}
			what, sev, report := r.classify()
			if !report {
				continue
			}
			who := "the internet"
			if r.source != "0.0.0.0/0" && r.source != "::/0" {
				who = "any address (" + r.source + ")"
			}
			msg := fmt.Sprintf("Opens %s to %s on %s.", what, who, Code(ch.Address))
			if r.desc != "" && r.desc != "<nil>" {
				msg += fmt.Sprintf(" The rule is described as %s.", Quote(r.desc))
			}
			f := finding(ch, "Opens "+what+" to the internet", msg)
			f.Severity = sev
			f.Short = "opens " + shortPort(what) + " to the internet"
			f.Remediation = "Limit the source to known addresses (a VPN or office range, or another security group). For remote access, prefer SSM Session Manager, IAP or a bastion over an open port."
			out = append(out, f)
		}
	}
	return out
}

func shortPort(what string) string {
	if i := strings.Index(what, " ("); i > 0 {
		return what[:i]
	}
	if i := strings.Index(what, ", including"); i > 0 {
		return what[:i]
	}
	return what
}

// statement is one IAM policy statement.
type statement struct {
	path   string
	sid    string
	raw    map[string]any
	effect string
}

func (s statement) list(key string) []string { return Strings(s.raw[key]) }

func (s statement) has(key string) bool { _, ok := s.raw[key]; return ok }

// policyStatements finds IAM-style policy documents anywhere in an object,
// including JSON held in string attributes, and returns their statements
// keyed by where they were found.
func policyStatements(obj map[string]any) map[string][]statement {
	out := map[string][]statement{}
	var walk func(path string, v any)
	walk = func(path string, v any) {
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
					out[path] = append(out[path], statement{path: path, sid: fmt.Sprint(valueOr(m["Sid"], "")), raw: m, effect: fmt.Sprint(m["Effect"])})
				}
			}
		case map[string]any:
			for _, k := range diff.SortedKeys(x) {
				p := k
				if path != "" {
					p = path + "." + k
				}
				walk(p, x[k])
			}
		case []any:
			for i, e := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), e)
			}
		}
	}
	walk("", obj)
	return out
}

// newStatements returns statements in the planned object that the current
// object does not already have.
func (c *Context) newStatements(ch *model.Change) []statement {
	after := policyStatements(c.After(ch))
	if len(after) == 0 {
		return nil
	}
	before := map[string][]statement{}
	if ch.Action != model.Create {
		before = policyStatements(c.Before(ch))
	}
	var out []statement
	for _, path := range diff.SortedKeys(after) {
		for _, s := range after[path] {
			found := false
			for _, b := range before[path] {
				if diff.Equal(b.raw, s.raw) {
					found = true
					break
				}
			}
			if !found {
				out = append(out, s)
			}
		}
	}
	return out
}

var serviceWildcard = regexp.MustCompile(`^[a-zA-Z0-9-]+:\*$`)

func checkIAMWildcard(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Create, model.Update, model.Replace) {
		if !Managed(ch) {
			continue
		}
		for _, s := range c.newStatements(ch) {
			if !strings.EqualFold(s.effect, "Allow") || s.has("Principal") && !s.has("Action") {
				continue
			}
			actions := s.list("Action")
			resources := s.list("Resource")
			anyResource := s.has("NotResource")
			for _, r := range resources {
				if r == "*" {
					anyResource = true
				}
			}
			var all, services, passRole bool
			var svc []string
			for _, a := range actions {
				switch {
				case a == "*" || a == "*:*":
					all = true
				case serviceWildcard.MatchString(a):
					services = true
					svc = append(svc, a)
				case strings.EqualFold(a, "iam:PassRole"):
					passRole = true
				}
			}
			label := ""
			if s.sid != "" {
				label = fmt.Sprintf(" (%s)", Quote(s.sid))
			}
			where := "a policy"
			if s.path != "" {
				where = Code(s.path)
			}
			var msg, short string
			sev := model.High
			switch {
			case all && anyResource:
				msg = fmt.Sprintf("Adds a statement%s to %s on %s that allows every action on every resource: full administrator access.", label, where, Code(ch.Address))
				short = "grants full admin access"
				sev = model.Critical
			case services && anyResource:
				msg = fmt.Sprintf("Adds a statement%s to %s on %s that allows %s on every resource.", label, where, Code(ch.Address), codeList(svc))
				short = "grants " + svc[0] + " on every resource"
			case all:
				msg = fmt.Sprintf("Adds a statement%s to %s on %s that allows every action on %s.", label, where, Code(ch.Address), codeList(resources))
				short = "grants every action"
				sev = model.Medium
			case s.has("NotAction"):
				msg = fmt.Sprintf("Adds an Allow statement%s with NotAction to %s on %s: it grants every action except the ones listed.", label, where, Code(ch.Address))
				short = "grants everything but a few actions"
				sev = model.Medium
			case passRole && anyResource:
				msg = fmt.Sprintf("Adds a statement%s to %s on %s that allows iam:PassRole on any role, which lets the holder act as any role in the account.", label, where, Code(ch.Address))
				short = "allows passing any role"
				sev = model.Medium
			default:
				continue
			}
			f := finding(ch, "Grants wildcard permissions", msg)
			f.Severity = sev
			f.Short = short
			f.Paths = []string{s.path}
			f.Remediation = "Grant only the actions the workload uses, on the specific resource ARNs it needs."
			out = append(out, f)
		}
	}
	return out
}

func publicPrincipal(p any) bool {
	switch x := p.(type) {
	case string:
		return x == "*"
	case map[string]any:
		for _, v := range x {
			for _, s := range Strings(v) {
				if s == "*" {
					return true
				}
			}
		}
	}
	return false
}

func checkPublicPrincipal(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Create, model.Update, model.Replace) {
		if !Managed(ch) {
			continue
		}
		if ch.Type == "aws_lambda_permission" {
			if String(c.After(ch), "principal") == "*" && String(c.After(ch), "source_arn") == "" && (ch.Action == model.Create || String(c.Before(ch), "principal") != "*") {
				f := finding(ch, "Anyone can invoke the function", fmt.Sprintf("%s lets anyone invoke the function (principal = \"*\" with no source_arn).", Code(ch.Address)))
				f.Short = "lets anyone invoke a function"
				f.Remediation = "Set principal to the service or account that calls the function, and restrict it with source_arn or source_account."
				out = append(out, f)
			}
			continue
		}
		for _, s := range c.newStatements(ch) {
			if !strings.EqualFold(s.effect, "Allow") || !publicPrincipal(s.raw["Principal"]) || s.has("Condition") {
				continue
			}
			actions := codeList(s.list("Action"))
			if actions == "" {
				actions = "anything"
			}
			var msg, short string
			sev := model.High
			if strings.HasSuffix(s.path, "assume_role_policy") || strings.Contains(strings.Join(s.list("Action"), ","), "sts:AssumeRole") {
				msg = fmt.Sprintf("Lets any AWS account assume %s: the trust policy allows Principal \"*\" with no condition.", Code(ch.Address))
				short = "lets any AWS account assume a role"
				sev = model.Critical
			} else {
				msg = fmt.Sprintf("Makes %s public: a statement allows %s to anyone (Principal \"*\") with no condition.", Code(ch.Address), actions)
				short = "makes " + Article(ch.Noun) + " public"
			}
			f := finding(ch, "Lets anyone use the resource", msg)
			f.Severity = sev
			f.Short = short
			f.Paths = []string{s.path}
			f.Remediation = "Name the accounts, roles or services that need access, or add a Condition (for example aws:SourceArn or aws:PrincipalOrgID)."
			out = append(out, f)
		}
	}
	return out
}

var adminPolicies = map[string]model.Severity{"AdministratorAccess": model.High, "IAMFullAccess": model.High, "PowerUserAccess": model.Medium}

func checkAdminAccess(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Create, model.Update, model.Replace) {
		after, before := c.After(ch), c.Before(ch)
		changed := func(attr string) bool { return ch.Action == model.Create || !diff.Equal(after[attr], before[attr]) }
		add := func(sev model.Severity, what string) {
			f := finding(ch, "Grants administrator access", fmt.Sprintf("%s grants %s.", Code(ch.Address), what))
			f.Severity = sev
			f.Short = "grants " + what
			f.Remediation = "Grant a narrower role or policy that covers what the principal actually does."
			out = append(out, f)
		}
		switch {
		case strings.HasPrefix(ch.Type, "aws_iam_") && strings.HasSuffix(ch.Type, "policy_attachment"):
			arn := String(after, "policy_arn")
			for name, sev := range adminPolicies {
				if strings.HasSuffix(arn, ":policy/"+name) && changed("policy_arn") {
					add(sev, name)
				}
			}
		case strings.HasPrefix(ch.Type, "google_") && strings.Contains(ch.Type, "_iam_"):
			role := String(after, "role")
			if (role == "roles/owner" || role == "roles/editor") && changed("role") {
				add(model.High, role)
			}
		case ch.Type == "azurerm_role_assignment":
			role := String(after, "role_definition_name")
			if (role == "Owner" || role == "User Access Administrator" || role == "Contributor") && changed("role_definition_name") {
				add(model.High, "the "+role+" role")
			}
		case ch.Type == "kubernetes_cluster_role_binding" || ch.Type == "kubernetes_cluster_role_binding_v1":
			if String(after, "role_ref.name") == "cluster-admin" && (ch.Action == model.Create || String(before, "role_ref.name") != "cluster-admin") {
				add(model.High, "cluster-admin")
			}
		}
	}
	return out
}

func checkPublicBucket(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Create, model.Update, model.Replace) {
		after, before := c.After(ch), c.Before(ch)
		switch ch.Type {
		case "aws_s3_bucket_public_access_block", "aws_s3_account_public_access_block":
			var off []string
			for _, flag := range []string{"block_public_acls", "block_public_policy", "ignore_public_acls", "restrict_public_buckets"} {
				av, ok := Bool(after, flag)
				if !ok || av {
					continue
				}
				if bv, ok := Bool(before, flag); ch.Action != model.Create && ok && !bv {
					continue
				}
				off = append(off, flag)
			}
			if len(off) == 0 {
				continue
			}
			scope := "the bucket"
			sev := model.High
			if ch.Type == "aws_s3_account_public_access_block" {
				scope = "every bucket in the account"
				sev = model.Critical
			}
			f := finding(ch, "Weakens the S3 public access block", fmt.Sprintf("Turns off %s on %s, so a bucket policy or ACL can make %s public.", codeList(off), Code(ch.Address), scope))
			f.Severity = sev
			f.Short = "weakens an S3 public access block"
			f.Paths = off
			f.Remediation = "Keep all four settings on unless the bucket is meant to serve public content, and prefer CloudFront with an origin access control for that."
			out = append(out, f)
		case "aws_s3_bucket_acl", "aws_s3_bucket":
			acl := String(after, "acl")
			if (acl == "public-read" || acl == "public-read-write" || acl == "authenticated-read") && (ch.Action == model.Create || String(before, "acl") != acl) {
				f := finding(ch, "Public bucket ACL", fmt.Sprintf("Sets the %s ACL on %s.", Code(acl), Code(ch.Address)))
				f.Short = "makes a bucket public"
				f.Paths = []string{"acl"}
				f.Remediation = "Use the private ACL (or disable ACLs with BucketOwnerEnforced) and grant access through a policy."
				out = append(out, f)
			}
		case "azurerm_storage_container":
			t := String(after, "container_access_type")
			if (t == "blob" || t == "container") && (ch.Action == model.Create || String(before, "container_access_type") != t) {
				f := finding(ch, "Public blob container", fmt.Sprintf("Sets %s on %s, so anyone can read its blobs.", Code("container_access_type = "+strconv.Quote(t)), Code(ch.Address)))
				f.Short = "makes a blob container public"
				f.Remediation = "Use private access and hand out SAS tokens or use a CDN in front of it."
				out = append(out, f)
			}
		default:
			if !strings.HasPrefix(ch.Type, "google_storage_bucket_iam") {
				continue
			}
			members := append(Strings(after["member"]), Strings(after["members"])...)
			had := map[string]bool{}
			if ch.Action != model.Create && String(before, "role") == String(after, "role") {
				for _, m := range append(Strings(before["member"]), Strings(before["members"])...) {
					had[m] = true
				}
			}
			for _, m := range members {
				if (m == "allUsers" || m == "allAuthenticatedUsers") && !had[m] {
					f := finding(ch, "Public Cloud Storage bucket", fmt.Sprintf("Grants %s to %s on %s.", Code(String(after, "role")), Code(m), Code(ch.Address)))
					f.Short = "makes a bucket public"
					f.Remediation = "Grant the role to specific users, groups or service accounts instead."
					out = append(out, f)
					break
				}
			}
		}
	}
	return out
}

func checkPublicDatabase(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Create, model.Update, model.Replace) {
		after, before := c.After(ch), c.Before(ch)
		switch ch.Type {
		case "aws_db_instance", "aws_rds_cluster_instance", "aws_redshift_cluster", "aws_dms_replication_instance":
			if on, _ := Bool(after, "publicly_accessible"); on {
				if was, _ := Bool(before, "publicly_accessible"); was && ch.Action != model.Create {
					continue
				}
				f := finding(ch, "Database is publicly accessible", fmt.Sprintf("Makes %s publicly accessible: it gets a public address, guarded only by its security groups and password.", Code(ch.Address)))
				f.Short = "makes a database publicly accessible"
				f.Paths = []string{"publicly_accessible"}
				f.Remediation = "Keep databases in private subnets with publicly_accessible = false and reach them through the application, a bastion or a VPN."
				out = append(out, f)
			}
		case "google_sql_database_instance":
			nets, _ := Attr(after, "settings.ip_configuration.authorized_networks")
			oldNets, _ := Attr(before, "settings.ip_configuration.authorized_networks")
			wasPublic := false
			for _, n := range asList(oldNets) {
				m, _ := n.(map[string]any)
				if isPublicCIDR(String(m, "value")) {
					wasPublic = true
				}
			}
			if wasPublic && ch.Action != model.Create {
				continue
			}
			for _, n := range asList(nets) {
				m, _ := n.(map[string]any)
				if isPublicCIDR(String(m, "value")) {
					f := finding(ch, "Database is open to the internet", fmt.Sprintf("Authorizes %s on %s.", Code(String(m, "value")), Code(ch.Address)))
					f.Short = "opens a database to the internet"
					f.Remediation = "Use private IP or the Cloud SQL Auth Proxy rather than authorizing every address."
					out = append(out, f)
					break
				}
			}
		case "azurerm_postgresql_flexible_server_firewall_rule", "azurerm_mssql_firewall_rule", "azurerm_mysql_flexible_server_firewall_rule", "azurerm_postgresql_firewall_rule":
			if String(after, "start_ip_address") == "0.0.0.0" && String(after, "end_ip_address") == "255.255.255.255" && ch.Action == model.Create {
				f := finding(ch, "Database is open to the internet", fmt.Sprintf("%s allows every IPv4 address.", Code(ch.Address)))
				f.Short = "opens a database to the internet"
				f.Remediation = "Allow specific address ranges, or use private endpoints."
				out = append(out, f)
			}
		}
	}
	return out
}

var encryptionAttrs = map[string]string{
	"aws_db_instance": "storage_encrypted", "aws_rds_cluster": "storage_encrypted", "aws_docdb_cluster": "storage_encrypted",
	"aws_neptune_cluster": "storage_encrypted", "aws_ebs_volume": "encrypted", "aws_efs_file_system": "encrypted",
	"aws_redshift_cluster": "encrypted", "aws_elasticache_replication_group": "at_rest_encryption_enabled",
	"aws_sqs_queue": "sqs_managed_sse_enabled",
}

func checkEncryptionDisabled(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Create, model.Update, model.Replace) {
		attr, ok := encryptionAttrs[ch.Type]
		if !ok {
			continue
		}
		on, ok := Bool(c.After(ch), attr)
		if !ok || on {
			continue
		}
		if ch.Type == "aws_sqs_queue" && String(c.After(ch), "kms_master_key_id") != "" {
			continue
		}
		if was, ok := Bool(c.Before(ch), attr); ok && !was && ch.Action == model.Update {
			continue
		}
		verb := "Creates"
		if ch.Action != model.Create {
			verb = "Leaves"
		}
		if was, _ := Bool(c.Before(ch), attr); was {
			verb = "Turns off encryption on"
		}
		msg := fmt.Sprintf("%s %s with %s = false, so its data is not encrypted at rest.", verb, Code(ch.Address), attr)
		if verb == "Turns off encryption on" {
			msg = fmt.Sprintf("Turns off encryption on %s (%s = false).", Code(ch.Address), attr)
		}
		f := finding(ch, "Stores data unencrypted", msg)
		f.Short = "stores data unencrypted"
		f.Paths = []string{attr}
		f.Remediation = fmt.Sprintf("Set %s = true (most services can use the AWS-managed key at no extra cost).", attr)
		out = append(out, f)
	}
	return out
}

var secretName = regexp.MustCompile(`(?i)(password|passwd|secret|token|private_?key|api_?key|credential|access_?key|connection_?string)`)

func checkSecretOutput(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, o := range c.Report.Outputs {
		if o.Sensitive || (o.Action != model.Create && o.Action != model.Update) || !secretName.MatchString(o.Name) {
			continue
		}
		if cfg := c.Plan.Configuration; cfg != nil && cfg.RootModule != nil {
			if co := cfg.RootModule.Outputs[o.Name]; co != nil && co.Sensitive {
				continue
			}
		}
		out = append(out, &model.Finding{
			Title:       "Secret-looking output is not sensitive",
			Short:       "prints a secret-looking output",
			Message:     fmt.Sprintf("Output %s looks like a secret but is not marked sensitive, so its value is printed by plan and apply.", Code(o.Name)),
			Addresses:   []string{"output." + o.Name},
			Remediation: "Add sensitive = true to the output (it can still be read with terraform output -raw).",
		})
	}
	return out
}

func checkPlaintextParameter(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Create, model.Update, model.Replace) {
		if ch.Type != "aws_ssm_parameter" {
			continue
		}
		after := c.After(ch)
		if String(after, "type") == "SecureString" || !secretName.MatchString(String(after, "name")) {
			continue
		}
		f := finding(ch, "Secret stored as plain text", fmt.Sprintf("%s stores %s as a plain %s parameter, readable by anyone with ssm:GetParameter.", Code(ch.Address), Code(String(after, "name")), String(after, "type")))
		f.Short = "stores a secret in plain text"
		f.Paths = []string{"type"}
		f.Remediation = "Use type = \"SecureString\" so the value is encrypted with KMS."
		out = append(out, f)
	}
	return out
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
