package analysis

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

// rc describes one resource change for a synthetic plan. Values are JSON.
type rc struct {
	addr    string
	actions string // "create", "update", "delete,create", ...
	before  string
	after   string
	reason  string
	unknown string
}

func synth(t *testing.T, changes ...rc) *plan.Plan {
	t.Helper()
	return synthWith(t, "", changes...)
}

// synthWith makes a synthetic plan with a configuration section (JSON),
// for rules that follow references between resources.
func synthWith(t *testing.T, configuration string, changes ...rc) *plan.Plan {
	t.Helper()
	var rcs []map[string]any
	for _, c := range changes {
		addr, err := plan.ParseAddress(c.addr)
		if err != nil {
			t.Fatal(err)
		}
		entry := map[string]any{
			"address": c.addr, "mode": addr.Mode, "type": addr.Type, "name": addr.Name,
			"provider_name": "registry.terraform.io/hashicorp/" + strings.SplitN(addr.Type, "_", 2)[0],
			"change": map[string]any{
				"actions":       strings.Split(c.actions, ","),
				"before":        raw(c.before),
				"after":         raw(c.after),
				"after_unknown": raw(or(c.unknown, "{}")),
			},
		}
		if c.reason != "" {
			entry["action_reason"] = c.reason
		}
		rcs = append(rcs, entry)
	}
	fields := map[string]any{
		"format_version": "1.2", "terraform_version": "1.16.0",
		"planned_values": map[string]any{}, "resource_changes": rcs,
	}
	if configuration != "" {
		fields["configuration"] = raw(configuration)
	}
	doc, _ := json.Marshal(fields)
	p, err := plan.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func raw(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("null")
	}
	return json.RawMessage(s)
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func run(t *testing.T, cfg *config.Config, changes ...rc) *model.Report {
	t.Helper()
	return Analyze(synth(t, changes...), Options{Config: cfg})
}

func only(t *testing.T, r *model.Report, rule string) *model.Finding {
	t.Helper()
	fs := findingsFor(r, rule)
	if len(fs) != 1 {
		t.Fatalf("want one %s finding, got %d: %v", rule, len(fs), ruleList(r))
	}
	return fs[0]
}

func none(t *testing.T, r *model.Report, rule string) {
	t.Helper()
	if fs := findingsFor(r, rule); len(fs) > 0 {
		t.Fatalf("unexpected %s: %s", rule, fs[0].Message)
	}
}

func TestPublicIngressRules(t *testing.T) {
	r := run(t, nil,
		rc{addr: "aws_vpc_security_group_ingress_rule.rdp", actions: "create", after: `{"cidr_ipv4":"0.0.0.0/0","from_port":3389,"to_port":3389,"ip_protocol":"tcp"}`},
		rc{addr: "aws_vpc_security_group_ingress_rule.https", actions: "create", after: `{"cidr_ipv4":"0.0.0.0/0","from_port":443,"to_port":443,"ip_protocol":"tcp"}`},
		rc{addr: "aws_vpc_security_group_ingress_rule.office", actions: "create", after: `{"cidr_ipv4":"10.0.0.0/8","from_port":22,"to_port":22,"ip_protocol":"tcp"}`},
		rc{addr: "aws_vpc_security_group_ingress_rule.all", actions: "create", after: `{"cidr_ipv6":"::/0","ip_protocol":"-1"}`},
		rc{addr: "google_compute_firewall.app", actions: "create", after: `{"direction":"INGRESS","source_ranges":["0.0.0.0/0"],"allow":[{"protocol":"tcp","ports":["8080"]}]}`},
	)
	got := map[string]model.Severity{}
	for _, f := range findingsFor(r, "public-ingress") {
		got[f.Addresses[0]] = f.Severity
	}
	want := map[string]model.Severity{
		"aws_vpc_security_group_ingress_rule.rdp": model.Critical,
		"aws_vpc_security_group_ingress_rule.all": model.Critical,
		"google_compute_firewall.app":             model.High,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("public-ingress = %v, want %v", got, want)
	}
}

func TestPublicIngressOnlyWhatChanges(t *testing.T) {
	sg := `{"ingress":[{"cidr_blocks":["0.0.0.0/0"],"ipv6_cidr_blocks":[],"from_port":22,"to_port":22,"protocol":"tcp","description":"ssh"}],"tags":{"a":"1"}}`
	r := run(t, nil, rc{addr: "aws_security_group.old", actions: "update", before: sg, after: strings.Replace(sg, `"a":"1"`, `"a":"2"`, 1)})
	none(t, r, "public-ingress")
}

func TestIAMWildcards(t *testing.T) {
	policy := func(stmts string) string {
		b, _ := json.Marshal(`{"Version":"2012-10-17","Statement":[` + stmts + `]}`)
		return string(b)
	}
	admin := `{"Effect":"Allow","Action":"*","Resource":"*"}`
	read := `{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::b/*"]}`
	r := run(t, nil,
		rc{addr: "aws_iam_policy.admin", actions: "update", before: `{"policy":` + policy(read) + `}`, after: `{"policy":` + policy(read+","+admin) + `}`},
		rc{addr: "aws_iam_policy.same", actions: "update", before: `{"policy":` + policy(admin) + `,"name":"a"}`, after: `{"policy":` + policy(admin) + `,"name":"b"}`},
		rc{addr: "aws_s3_bucket_policy.public", actions: "create", after: `{"policy":` + policy(`{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*"}`) + `}`},
		rc{addr: "aws_iam_role.open", actions: "create", after: `{"assume_role_policy":` + policy(`{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"sts:AssumeRole"}`) + `}`},
	)
	f := only(t, r, "iam-wildcard")
	if f.Addresses[0] != "aws_iam_policy.admin" || f.Severity != model.Critical {
		t.Errorf("iam-wildcard = %s %v", f.Severity, f.Addresses)
	}
	sev := map[string]model.Severity{}
	for _, f := range findingsFor(r, "public-principal") {
		sev[f.Addresses[0]] = f.Severity
	}
	if sev["aws_s3_bucket_policy.public"] != model.High || sev["aws_iam_role.open"] != model.Critical {
		t.Errorf("public-principal = %v", sev)
	}
}

func TestAdminAccess(t *testing.T) {
	r := run(t, nil,
		rc{addr: "aws_iam_role_policy_attachment.admin", actions: "create", after: `{"policy_arn":"arn:aws:iam::aws:policy/AdministratorAccess","role":"ci"}`},
		rc{addr: "google_project_iam_member.owner", actions: "create", after: `{"role":"roles/owner","member":"user:a@example.com"}`},
		rc{addr: "aws_iam_role_policy_attachment.read", actions: "create", after: `{"policy_arn":"arn:aws:iam::aws:policy/ReadOnlyAccess","role":"ci"}`},
	)
	if n := len(findingsFor(r, "admin-access")); n != 2 {
		t.Fatalf("admin-access findings = %d: %v", n, ruleList(r))
	}
}

func TestStatefulDestroySeverity(t *testing.T) {
	r := run(t, nil,
		rc{addr: "aws_db_instance.snap", actions: "delete", reason: "delete_because_no_resource_config", before: `{"skip_final_snapshot":false,"final_snapshot_identifier":"final","deletion_protection":false}`},
		rc{addr: "aws_db_instance.nosnap", actions: "delete", before: `{"skip_final_snapshot":true,"deletion_protection":false}`},
		rc{addr: "aws_s3_bucket.kept", actions: "delete", before: `{"bucket":"b","force_destroy":false}`},
		rc{addr: "aws_kms_key.k", actions: "delete", before: `{"deletion_window_in_days":30}`},
		rc{addr: "aws_secretsmanager_secret.now", actions: "delete", before: `{"recovery_window_in_days":0}`},
	)
	want := map[string]model.Severity{
		"aws_db_instance.snap":          model.High,
		"aws_db_instance.nosnap":        model.Critical,
		"aws_s3_bucket.kept":            model.High,
		"aws_kms_key.k":                 model.High,
		"aws_secretsmanager_secret.now": model.Critical,
	}
	for _, f := range findingsFor(r, "stateful-destroy") {
		if want[f.Addresses[0]] != f.Severity {
			t.Errorf("%s: %s, want %s (%s)", f.Addresses[0], f.Severity, want[f.Addresses[0]], f.Message)
		}
		delete(want, f.Addresses[0])
	}
	if len(want) > 0 {
		t.Errorf("missing findings for %v", want)
	}
	snap := expectFinding(t, r, "stateful-destroy", "aws_db_instance.snap", model.High)
	if snap != nil && !strings.Contains(snap.Snippet, "removed {") {
		t.Error("a block removed from config should suggest a removed block")
	}
}

func TestProtectedDelete(t *testing.T) {
	r := run(t, nil,
		rc{addr: "aws_lb.web", actions: "delete", before: `{"enable_deletion_protection":true}`},
		rc{addr: "aws_lb.open", actions: "delete", before: `{"enable_deletion_protection":false}`},
		rc{addr: "aws_cognito_user_pool.users", actions: "delete,create", before: `{"deletion_protection":"ACTIVE"}`, after: `{"deletion_protection":"INACTIVE"}`},
	)
	if n := len(findingsFor(r, "protected-delete")); n != 2 {
		t.Fatalf("protected-delete findings = %d: %v", n, ruleList(r))
	}
}

func TestDisruptiveUpdate(t *testing.T) {
	r := run(t, nil,
		rc{addr: "aws_instance.app", actions: "update", before: `{"instance_type":"t3.small","ami":"a"}`, after: `{"instance_type":"t3.large","ami":"a"}`},
		rc{addr: "aws_db_instance.db", actions: "update", before: `{"instance_class":"db.t4g.small","apply_immediately":true}`, after: `{"instance_class":"db.t4g.large","apply_immediately":true}`},
	)
	got := findingsFor(r, "disruptive-update")
	if len(got) != 2 {
		t.Fatalf("disruptive-update = %v", ruleList(r))
	}
	for _, f := range got {
		if f.Addresses[0] == "aws_db_instance.db" && !strings.Contains(f.Message, "as soon as this applies") {
			t.Errorf("apply_immediately should be called out: %s", f.Message)
		}
	}
}

func TestDNSChange(t *testing.T) {
	r := run(t, nil,
		rc{addr: "aws_route53_record.gone", actions: "delete", before: `{"name":"api.example.com","type":"A","records":["1.2.3.4"],"ttl":60}`},
		rc{addr: "aws_route53_record.moved", actions: "update", before: `{"name":"www.example.com","type":"CNAME","records":["a.example.net"],"ttl":300}`, after: `{"name":"www.example.com","type":"CNAME","records":["b.example.net"],"ttl":300}`},
	)
	sev := map[string]model.Severity{}
	for _, f := range findingsFor(r, "dns-change") {
		sev[f.Addresses[0]] = f.Severity
		if f.Addresses[0] == "aws_route53_record.moved" && !strings.Contains(f.Message, "b.example.net") {
			t.Errorf("message should show the new target: %s", f.Message)
		}
	}
	if sev["aws_route53_record.gone"] != model.High || sev["aws_route53_record.moved"] != model.Medium {
		t.Errorf("dns-change = %v", sev)
	}
}

// Resolvers hold the old answer for the old TTL, and lowering it in the
// same apply comes too late.
func TestDNSChangeTTL(t *testing.T) {
	r := run(t, nil, rc{addr: "aws_route53_record.api", actions: "update",
		before: `{"name":"api.example.com","type":"A","records":["1.2.3.4"],"ttl":86400}`,
		after:  `{"name":"api.example.com","type":"A","records":["5.6.7.8"],"ttl":60}`})
	f := only(t, r, "dns-change")
	for _, want := range []string{"for up to a day, the record's TTL.", "Lowering the TTL to a minute in the same apply does not shorten that"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message lacks %q: %s", want, f.Message)
		}
	}
}

func TestMassDestroyAndThreshold(t *testing.T) {
	var changes []rc
	for i := 0; i < 25; i++ {
		changes = append(changes, rc{addr: fmt.Sprintf("aws_instance.n%d", i), actions: "delete", before: `{"ami":"a"}`})
	}
	r := run(t, nil, changes...)
	if f := only(t, r, "mass-destroy"); len(f.Addresses) != 25 {
		t.Errorf("addresses = %d", len(f.Addresses))
	}
	cfg := &config.Config{Rules: map[string]map[string]any{"mass-destroy": {"threshold": 50}}}
	// Still half of everything managed, so it fires regardless of the threshold.
	r = run(t, cfg, changes...)
	only(t, r, "mass-destroy")
}

func TestConfigSuppressionAndOverrides(t *testing.T) {
	cfg := &config.Config{
		Path:     ".interragate.yaml",
		Disable:  []string{"encryption-disabled"},
		Severity: map[string]string{"stateful-destroy": "low"},
		Ignore:   []config.Ignore{{Rule: "public-ingress", Address: "aws_vpc_security_group_ingress_rule.bastion*", Reason: "bastion is behind an allowlist"}},
	}
	r := run(t, cfg,
		rc{addr: "aws_ebs_volume.v", actions: "create", after: `{"encrypted":false,"size":10}`},
		rc{addr: "aws_s3_bucket.b", actions: "delete", before: `{"force_destroy":true}`},
		rc{addr: "aws_vpc_security_group_ingress_rule.bastion_ssh", actions: "create", after: `{"cidr_ipv4":"0.0.0.0/0","from_port":22,"to_port":22,"ip_protocol":"tcp"}`},
	)
	none(t, r, "encryption-disabled")
	none(t, r, "public-ingress")
	if f := only(t, r, "stateful-destroy"); f.Severity != model.Low {
		t.Errorf("severity override: %s", f.Severity)
	}
	if len(r.Suppressed) != 1 || r.Suppressed[0].Reason != "bastion is behind an allowlist" {
		t.Errorf("suppressed = %+v", r.Suppressed)
	}
}

func TestRequiredTags(t *testing.T) {
	cfg := &config.Config{RequiredTags: []string{"Owner", "Env"}}
	r := run(t, cfg,
		rc{addr: "aws_s3_bucket.tagged", actions: "create", after: `{"tags_all":{"Owner":"a","Env":"prod"}}`},
		rc{addr: "aws_s3_bucket.bare", actions: "create", after: `{"tags_all":{"Owner":"a"}}`},
		rc{addr: "aws_s3_bucket.later", actions: "create", after: `{}`, unknown: `{"tags_all":true}`},
	)
	f := only(t, r, "required-tags")
	if len(f.Addresses) != 1 || f.Addresses[0] != "aws_s3_bucket.bare" || !strings.Contains(f.Message, "Env") {
		t.Errorf("required-tags = %v %s", f.Addresses, f.Message)
	}
}

func TestRenameAcrossModules(t *testing.T) {
	inst := `{"ami":"ami-1","instance_type":"t3.small","subnet_id":"s-1"}`
	bucket := `{"bucket":"logs-1","force_destroy":false,"acl":"private"}`
	r := run(t, nil,
		rc{addr: "module.old.aws_instance.web", actions: "delete", reason: "delete_because_no_module", before: inst},
		rc{addr: "module.old.aws_s3_bucket.logs", actions: "delete", reason: "delete_because_no_module", before: bucket},
		rc{addr: "module.new.aws_instance.web", actions: "create", after: inst, unknown: `{"id":true,"arn":true}`},
		rc{addr: "module.new.aws_s3_bucket.logs", actions: "create", after: bucket, unknown: `{"id":true}`},
	)
	f := only(t, r, "missing-moved-block")
	if !strings.Contains(f.Snippet, "from = module.old") || !strings.Contains(f.Snippet, "to   = module.new") {
		t.Errorf("module move snippet:\n%s", f.Snippet)
	}
}

func TestRenameNeedsSameName(t *testing.T) {
	r := run(t, nil,
		rc{addr: "aws_s3_bucket.a", actions: "delete", reason: "delete_because_no_resource_config", before: `{"bucket":"alpha","acl":"private","force_destroy":false}`},
		rc{addr: "aws_s3_bucket.b", actions: "create", after: `{"bucket":"beta","acl":"private","force_destroy":false}`},
	)
	none(t, r, "missing-moved-block")
}

func TestNoChanges(t *testing.T) {
	r := run(t, nil, rc{addr: "aws_s3_bucket.a", actions: "no-op", before: `{"bucket":"a"}`, after: `{"bucket":"a"}`})
	if r.Risk.Level != model.None || !strings.Contains(r.Risk.Headline, "No changes") {
		t.Errorf("risk = %s %q", r.Risk.Level, r.Risk.Headline)
	}
	if len(r.Findings) != 0 {
		t.Errorf("findings = %v", ruleList(r))
	}
}

func TestGCPRulesOnlyFlagNewExposure(t *testing.T) {
	sql := func(tier string) string {
		return `{"settings":[{"tier":"` + tier + `","ip_configuration":[{"authorized_networks":[{"value":"0.0.0.0/0"}]}]}]}`
	}
	r := run(t, nil,
		rc{addr: "google_sql_database_instance.db", actions: "update", before: sql("db-f1-micro"), after: sql("db-g1-small")},
		rc{addr: "google_storage_bucket_iam_binding.b", actions: "update", before: `{"role":"roles/storage.objectViewer","members":["allUsers"]}`, after: `{"role":"roles/storage.objectViewer","members":["allUsers","user:a@example.com"]}`},
	)
	none(t, r, "public-database")
	none(t, r, "public-bucket")
	r = run(t, nil, rc{addr: "google_storage_bucket_iam_member.new", actions: "create", after: `{"role":"roles/storage.objectViewer","member":"allUsers"}`})
	only(t, r, "public-bucket")
}

func TestDNSRename(t *testing.T) {
	r := run(t, nil, rc{addr: "aws_route53_record.api", actions: "create,delete",
		before: `{"name":"api.example.com","type":"A","records":["1.2.3.4"],"ttl":60}`,
		after:  `{"name":"api-v2.example.com","type":"A","records":["1.2.3.4"],"ttl":60}`})
	f := only(t, r, "dns-change")
	if f.Severity != model.High || !strings.Contains(f.Message, "api-v2.example.com") {
		t.Errorf("rename = %s %s", f.Severity, f.Message)
	}
}

func TestNameTaken(t *testing.T) {
	r := run(t, nil,
		rc{addr: "aws_security_group.web", actions: "create,delete", before: `{"name":"web","vpc_id":"vpc-1"}`, after: `{"name":"web","vpc_id":"vpc-1"}`},
		rc{addr: "aws_security_group.moved", actions: "create,delete", before: `{"name":"db","vpc_id":"vpc-1"}`, after: `{"name":"db","vpc_id":"vpc-2"}`},
		rc{addr: "aws_security_group.fresh", actions: "create,delete", before: `{"name":"api","vpc_id":"vpc-1"}`, after: `{"vpc_id":"vpc-1"}`, unknown: `{"name":true}`},
		rc{addr: "aws_iam_role.gap", actions: "delete,create", before: `{"name":"app"}`, after: `{"name":"app"}`},
		rc{addr: "aws_secretsmanager_secret.db", actions: "delete,create", before: `{"name":"prod/db","recovery_window_in_days":30}`, after: `{"name":"prod/db","recovery_window_in_days":30}`},
		rc{addr: "aws_secretsmanager_secret.now", actions: "delete,create", before: `{"name":"prod/now","recovery_window_in_days":0}`, after: `{"name":"prod/now","recovery_window_in_days":0}`},
	)
	got := findingsFor(r, "name-taken")
	var addrs []string
	for _, f := range got {
		addrs = append(addrs, f.Addresses[0])
	}
	if fmt.Sprint(addrs) != "[aws_security_group.web aws_secretsmanager_secret.db]" {
		t.Fatalf("name-taken flagged %v", addrs)
	}
	if !strings.Contains(got[0].Remediation, "name_prefix") {
		t.Errorf("remediation should suggest name_prefix: %s", got[0].Remediation)
	}
	if !strings.Contains(got[1].Message, "recovery window") {
		t.Errorf("secret message: %s", got[1].Message)
	}
}

func TestEngineVersions(t *testing.T) {
	r := run(t, nil,
		rc{addr: "aws_db_instance.auto", actions: "update", before: `{"engine":"postgres","engine_version":"14.10","auto_minor_version_upgrade":true}`, after: `{"engine":"postgres","engine_version":"14.7","auto_minor_version_upgrade":true}`},
		rc{addr: "aws_db_instance.up", actions: "update", before: `{"engine":"postgres","engine_version":"14.10"}`, after: `{"engine":"postgres","engine_version":"15.4","parameter_group_name":"pg14"}`},
		rc{addr: "aws_db_instance.allowed", actions: "update", before: `{"engine":"mysql","engine_version":"5.7.44"}`, after: `{"engine":"mysql","engine_version":"8.0.35","allow_major_version_upgrade":true}`},
		rc{addr: "aws_db_instance.minor", actions: "update", before: `{"engine":"mysql","engine_version":"8.0.28"}`, after: `{"engine":"mysql","engine_version":"8.0.35"}`},
		rc{addr: "aws_rds_cluster.aurora", actions: "update", before: `{"engine":"aurora-mysql","engine_version":"8.0.mysql_aurora.3.05.2"}`, after: `{"engine":"aurora-mysql","engine_version":"8.0.mysql_aurora.3.04.0"}`},
		rc{addr: "aws_opensearch_domain.search", actions: "update", before: `{"engine_version":"Elasticsearch_7.10"}`, after: `{"engine_version":"OpenSearch_1.3"}`},
	)
	var down []string
	for _, f := range findingsFor(r, "engine-downgrade") {
		down = append(down, f.Addresses[0])
	}
	if fmt.Sprint(down) != "[aws_db_instance.auto aws_rds_cluster.aurora]" {
		t.Fatalf("engine-downgrade flagged %v", down)
	}
	f := findingsFor(r, "engine-downgrade")[0]
	if !strings.Contains(f.Message, "auto_minor_version_upgrade") || !strings.Contains(f.Remediation, "or to just `14`") {
		t.Errorf("downgrade finding: %s / %s", f.Message, f.Remediation)
	}
	up := only(t, r, "major-upgrade-not-allowed")
	if up.Addresses[0] != "aws_db_instance.up" || !strings.Contains(up.Remediation, "`pg14`") {
		t.Errorf("major upgrade finding: %s %s", up.Addresses[0], up.Remediation)
	}
}

func TestStorageShrink(t *testing.T) {
	r := run(t, nil,
		rc{addr: "aws_db_instance.main", actions: "update", before: `{"allocated_storage":100,"max_allocated_storage":500}`, after: `{"allocated_storage":50,"max_allocated_storage":500}`},
		rc{addr: "aws_ebs_volume.data", actions: "update", before: `{"size":200}`, after: `{"size":100}`},
		rc{addr: "aws_ebs_volume.grow", actions: "update", before: `{"size":100}`, after: `{"size":200}`},
		rc{addr: "aws_instance.app", actions: "update", before: `{"root_block_device":[{"volume_size":30}]}`, after: `{"root_block_device":[{"volume_size":20}]}`},
	)
	var got []string
	for _, f := range findingsFor(r, "storage-shrink") {
		got = append(got, f.Addresses[0])
		if f.Fails != "update" {
			t.Errorf("%s: fails = %q", f.Addresses[0], f.Fails)
		}
	}
	if fmt.Sprint(got) != "[aws_db_instance.main aws_ebs_volume.data aws_instance.app]" {
		t.Fatalf("storage-shrink flagged %v", got)
	}
	if f := findingsFor(r, "storage-shrink")[0]; !strings.Contains(f.Message, "autoscaling") || !strings.Contains(f.Remediation, "ignore_changes") {
		t.Errorf("autoscaling note missing: %s / %s", f.Message, f.Remediation)
	}
}

func TestEKSVersionSkip(t *testing.T) {
	r := run(t, nil,
		rc{addr: "aws_eks_cluster.next", actions: "update", before: `{"version":"1.29"}`, after: `{"version":"1.30"}`},
		rc{addr: "aws_eks_cluster.jump", actions: "update", before: `{"version":"1.28"}`, after: `{"version":"1.30"}`},
		rc{addr: "aws_eks_cluster.back", actions: "update", before: `{"version":"1.30"}`, after: `{"version":"1.29"}`},
	)
	fs := findingsFor(r, "eks-version-skip")
	if len(fs) != 2 || fs[0].Addresses[0] != "aws_eks_cluster.jump" || fs[1].Addresses[0] != "aws_eks_cluster.back" {
		t.Fatalf("eks-version-skip = %v", ruleList(r))
	}
	if !strings.Contains(fs[0].Remediation, "`1.29` first") || !strings.Contains(fs[1].Message, "cannot downgrade") {
		t.Errorf("messages: %s / %s", fs[0].Remediation, fs[1].Message)
	}
}

func TestNoRollout(t *testing.T) {
	cfg := func(version string) string {
		v := `"version":{"constant_value":"` + version + `"}`
		if version == "" {
			v = `"version":{"references":["aws_launch_template.web.latest_version","aws_launch_template.web"]}`
		}
		return `{"root_module":{"resources":[
			{"address":"aws_launch_template.web","mode":"managed","type":"aws_launch_template","name":"web","expressions":{}},
			{"address":"aws_autoscaling_group.web","mode":"managed","type":"aws_autoscaling_group","name":"web",
			 "expressions":{"launch_template":[{"id":{"references":["aws_launch_template.web.id","aws_launch_template.web"]},` + v + `}]}}]}}`
	}
	lt := rc{addr: "aws_launch_template.web", actions: "update", before: `{"image_id":"ami-1","update_default_version":false}`, after: `{"image_id":"ami-2","update_default_version":false}`, unknown: `{"latest_version":true}`}
	asg := func(version, refresh string) rc {
		v := `,"version":"` + version + `"`
		if version == "" {
			v = ""
		}
		obj := `{"launch_template":[{"id":"lt-1"` + v + `}],"instance_refresh":` + refresh + `}`
		return rc{addr: "aws_autoscaling_group.web", actions: "no-op", before: obj, after: obj}
	}
	analyze := func(cfgVersion string, group rc) *model.Report {
		return Analyze(synthWith(t, cfg(cfgVersion), lt, group), Options{})
	}

	f := only(t, analyze("$Latest", asg("$Latest", "[]")), "no-rollout")
	if !strings.Contains(f.Message, "running instances keep the old template") || f.Severity != model.Low {
		t.Errorf("$Latest: %s (%s)", f.Message, f.Severity)
	}
	none(t, analyze("$Latest", asg("$Latest", `[{"strategy":"Rolling"}]`)), "no-rollout")
	f = only(t, analyze("$Default", asg("$Default", "[]")), "no-rollout")
	if !strings.Contains(f.Message, "not even new ones") || f.Severity != model.Medium {
		t.Errorf("$Default: %s (%s)", f.Message, f.Severity)
	}
	f = only(t, analyze("3", asg("3", "[]")), "no-rollout")
	if !strings.Contains(f.Message, "launches version 3") {
		t.Errorf("pinned: %s", f.Message)
	}
	// No version at all, as the provider stores it, is the default.
	f = only(t, analyze("", asg("", "[]")), "no-rollout")
	empty := rc{addr: "aws_autoscaling_group.web", actions: "no-op", before: `{"launch_template":[{"id":"lt-1","version":""}],"instance_refresh":[]}`,
		after: `{"launch_template":[{"id":"lt-1","version":""}],"instance_refresh":[]}`}
	f = only(t, analyze("$Default", empty), "no-rollout")
	if !strings.Contains(f.Message, "its default version") || strings.Contains(f.Message, "version ,") {
		t.Errorf("empty version: %s", f.Message)
	}
	// A description makes no new version, so there is nothing to roll out.
	desc := rc{addr: "aws_launch_template.web", actions: "update", before: `{"description":"a"}`, after: `{"description":"b"}`}
	none(t, Analyze(synthWith(t, cfg("$Default"), desc, asg("$Default", "[]")), Options{}), "no-rollout")
	// A replaced template is new, and its only version has the new settings.
	repl := rc{addr: "aws_launch_template.web", actions: "create,delete", before: `{"id":"lt-1","name":"web"}`, after: `{"name":"web"}`, unknown: `{"id":true,"latest_version":true}`}
	f = only(t, Analyze(synthWith(t, cfg("$Default"), repl, asg("$Default", "[]")), Options{}), "no-rollout")
	if f.Severity != model.Low {
		t.Errorf("replaced template: %s %s", f.Severity, f.Message)
	}
	// A group is paired with the template whose ID it holds.
	other := rc{addr: "aws_launch_template.web", actions: "update", before: `{"id":"lt-9","image_id":"ami-1"}`, after: `{"image_id":"ami-2"}`, unknown: `{"latest_version":true}`}
	none(t, Analyze(synthWith(t, cfg("$Latest"), other, asg("$Latest", "[]")), Options{}), "no-rollout")
}

func TestCrossAccountAccess(t *testing.T) {
	trust := func(principals string, cond string) string {
		doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":` + principals + `}` + cond + `}]}`
		b, _ := json.Marshal(doc)
		return string(b)
	}
	r := run(t, nil,
		// The home account, from a resource's own ARN.
		rc{addr: "aws_s3_bucket.home", actions: "update", before: `{"arn":"arn:aws:s3:::x","bucket":"x"}`, after: `{"arn":"arn:aws:s3:::x","bucket":"y"}`},
		rc{addr: "aws_iam_role.app", actions: "update",
			before: `{"arn":"arn:aws:iam::111111111111:role/app","assume_role_policy":` + trust(`"arn:aws:iam::111111111111:root"`, "") + `}`,
			after:  `{"arn":"arn:aws:iam::111111111111:role/app","assume_role_policy":` + trust(`["arn:aws:iam::111111111111:root","arn:aws:iam::999999999999:root"]`, "") + `}`},
		rc{addr: "aws_iam_role.vendor", actions: "create",
			after: `{"assume_role_policy":` + trust(`"888888888888"`, `,"Condition":{"StringEquals":{"sts:ExternalId":"abc"}}`) + `}`},
		rc{addr: "aws_iam_role.same", actions: "create",
			after: `{"assume_role_policy":` + trust(`"arn:aws:iam::111111111111:role/ci"`, "") + `}`},
	)
	fs := findingsFor(r, "cross-account-access")
	if len(fs) != 2 {
		t.Fatalf("cross-account-access = %v", ruleList(r))
	}
	if fs[0].Addresses[0] != "aws_iam_role.app" || fs[0].Severity != model.High || !strings.Contains(fs[0].Message, "Lets AWS account 999999999999 assume") {
		t.Errorf("app: %s %s %s", fs[0].Addresses[0], fs[0].Severity, fs[0].Message)
	}
	if fs[1].Addresses[0] != "aws_iam_role.vendor" || fs[1].Severity != model.Low || !strings.Contains(fs[1].Message, "external ID") {
		t.Errorf("vendor: %s %s %s", fs[1].Addresses[0], fs[1].Severity, fs[1].Message)
	}
}

// AWS delivers load balancer access logs from an account of its own, which
// a log bucket's policy has to let in.
func TestCrossAccountELBLogs(t *testing.T) {
	doc, _ := json.Marshal(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::127311923021:root"},"Action":"s3:PutObject","Resource":"arn:aws:s3:::logs/*"}]}`)
	r := run(t, nil,
		rc{addr: "aws_iam_role.app", actions: "no-op", before: `{"arn":"arn:aws:iam::111122223333:role/app"}`, after: `{"arn":"arn:aws:iam::111122223333:role/app"}`},
		rc{addr: "aws_s3_bucket_policy.logs", actions: "create", after: `{"bucket":"logs","policy":` + string(doc) + `}`},
	)
	none(t, r, "cross-account-access")
}

func TestDeprecatedRuntime(t *testing.T) {
	r := run(t, nil,
		rc{addr: "aws_lambda_function.old", actions: "create", after: `{"runtime":"python3.8","function_name":"old"}`},
		rc{addr: "aws_lambda_function.new", actions: "create", after: `{"runtime":"python3.13","function_name":"new"}`},
		rc{addr: "aws_lambda_function.tags", actions: "update", before: `{"runtime":"nodejs16.x","tags":{"a":"1"}}`, after: `{"runtime":"nodejs16.x","tags":{"a":"2"}}`},
	)
	f := only(t, r, "deprecated-runtime")
	if f.Addresses[0] != "aws_lambda_function.old" || !strings.Contains(f.Remediation, "`python3.13`") {
		t.Errorf("deprecated-runtime: %s %s", f.Addresses[0], f.Remediation)
	}
}

func TestRulesFight(t *testing.T) {
	cfg := `{"root_module":{"resources":[
		{"address":"aws_security_group.app","mode":"managed","type":"aws_security_group","name":"app","expressions":{}},
		{"address":"aws_security_group_rule.ssh","mode":"managed","type":"aws_security_group_rule","name":"ssh",
		 "expressions":{"security_group_id":{"references":["aws_security_group.app.id","aws_security_group.app"]}}}]}}`
	https := `{"from_port":443,"to_port":443,"protocol":"tcp","cidr_blocks":["0.0.0.0/0"]}`
	ssh := `{"from_port":22,"to_port":22,"protocol":"tcp","cidr_blocks":["10.0.0.0/8"]}`
	r := Analyze(synthWith(t, cfg,
		rc{addr: "aws_security_group.app", actions: "update", before: `{"ingress":[` + https + `,` + ssh + `]}`, after: `{"ingress":[` + https + `]}`},
		rc{addr: "aws_security_group_rule.ssh", actions: "no-op",
			before: `{"type":"ingress","from_port":22,"to_port":22,"protocol":"tcp","cidr_blocks":["10.0.0.0/8"]}`,
			after:  `{"type":"ingress","from_port":22,"to_port":22,"protocol":"tcp","cidr_blocks":["10.0.0.0/8"]}`},
	), Options{})
	f := only(t, r, "rules-fight")
	if !strings.Contains(f.Message, "removes an ingress rule that `aws_security_group_rule.ssh` adds separately") {
		t.Errorf("message: %s", f.Message)
	}

	// Removing a rule nothing else adds back is just a change.
	r = Analyze(synthWith(t, cfg,
		rc{addr: "aws_security_group.app", actions: "update", before: `{"ingress":[` + https + `,` + ssh + `]}`, after: `{"ingress":[` + ssh + `]}`},
		rc{addr: "aws_security_group_rule.ssh", actions: "no-op",
			before: `{"type":"ingress","from_port":22,"to_port":22,"protocol":"tcp","cidr_blocks":["10.0.0.0/8"]}`,
			after:  `{"type":"ingress","from_port":22,"to_port":22,"protocol":"tcp","cidr_blocks":["10.0.0.0/8"]}`},
	), Options{})
	none(t, r, "rules-fight")

	// Rules from other security groups match only when the group does.
	fromLB := `{"from_port":8080,"to_port":8080,"protocol":"tcp","security_groups":["sg-lb"]}`
	other := `{"type":"ingress","from_port":8080,"to_port":8080,"protocol":"tcp","source_security_group_id":"sg-bastion"}`
	r = Analyze(synthWith(t, cfg,
		rc{addr: "aws_security_group.app", actions: "update", before: `{"ingress":[` + https + `,` + fromLB + `]}`, after: `{"ingress":[` + https + `]}`},
		rc{addr: "aws_security_group_rule.ssh", actions: "no-op", before: other, after: other},
	), Options{})
	none(t, r, "rules-fight")

	// An all-protocols rule matches whatever ports each side records.
	cfg2 := strings.ReplaceAll(strings.ReplaceAll(cfg, "aws_security_group_rule.ssh", "aws_vpc_security_group_ingress_rule.all"), `"type":"aws_security_group_rule","name":"ssh"`, `"type":"aws_vpc_security_group_ingress_rule","name":"all"`)
	all := `{"from_port":0,"to_port":0,"protocol":"-1","cidr_blocks":["10.0.0.0/8"]}`
	vpcRule := `{"ip_protocol":"-1","cidr_ipv4":"10.0.0.0/8"}`
	r = Analyze(synthWith(t, cfg2,
		rc{addr: "aws_security_group.app", actions: "update", before: `{"ingress":[` + https + `,` + all + `]}`, after: `{"ingress":[` + https + `]}`},
		rc{addr: "aws_vpc_security_group_ingress_rule.all", actions: "no-op", before: vpcRule, after: vpcRule},
	), Options{})
	only(t, r, "rules-fight")
}

func TestRunsCommand(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"main.tf": `resource "terraform_data" "migrate" {
  provisioner "local-exec" {
    command = "./migrate.sh ${var.env}"
  }
  provisioner "local-exec" {
    when    = destroy
    command = "./rollback.sh"
  }
}

resource "terraform_data" "teardown" {
  provisioner "local-exec" {
    when    = destroy
    command = "./teardown.sh"
  }
}
`})
	p := synth(t,
		rc{addr: "terraform_data.migrate", actions: "create", after: `{"id":"x"}`},
		rc{addr: "terraform_data.teardown", actions: "create", after: `{"id":"y"}`},
	)
	r := Analyze(p, Options{SourceDir: dir})
	f := only(t, r, "runs-command")
	if f.Addresses[0] != "terraform_data.migrate" || f.Message != "Creating `terraform_data.migrate` runs a command on the machine that applies the plan: `./migrate.sh ${var.env}`." {
		t.Errorf("runs-command: %s", f.Message)
	}
}

// A role recreated under its own name: what the plan leaves pointing at it.
func TestRoleRecreated(t *testing.T) {
	role := rc{addr: "aws_iam_role.app", actions: "delete,create", reason: "replace_because_tainted",
		before: `{"name":"app","path":"/","arn":"arn:aws:iam::111122223333:role/app","force_detach_policies":false}`,
		after:  `{"name":"app","path":"/","force_detach_policies":false}`, unknown: `{"arn":true,"id":true}`}
	attach := rc{addr: "aws_iam_role_policy_attachment.s3", actions: "no-op",
		before: `{"role":"app","policy_arn":"arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess"}`, after: `{"role":"app","policy_arn":"arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess"}`}
	profile := rc{addr: "aws_iam_instance_profile.app", actions: "no-op", before: `{"name":"app","role":"app"}`, after: `{"name":"app","role":"app"}`}
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:role/app"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::data/*"}]}`
	bucket := rc{addr: "aws_s3_bucket_policy.data", actions: "no-op", before: `{"bucket":"data","policy":` + strconv.Quote(policy) + `}`, after: `{"bucket":"data","policy":` + strconv.Quote(policy) + `}`}

	r := run(t, nil, role, attach, profile, bucket)
	f := only(t, r, "role-delete-conflict")
	if f.Fails != "destroy" || f.Addresses[0] != "aws_iam_role.app" || !strings.Contains(f.Message, "AWS refuses to delete a role that has policies attached") {
		t.Errorf("conflict: fails %q, addresses %v: %s", f.Fails, f.Addresses, f.Message)
	}
	f = only(t, r, "role-recreated")
	for _, want := range []string{"removes it from instance profile `aws_iam_instance_profile.app`", "unique ID", "`aws_s3_bucket_policy.data`"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("recreated lacks %q: %s", want, f.Message)
		}
	}
	if f.Severity != model.High {
		t.Errorf("recreated severity %s", f.Severity)
	}

	// force_detach_policies lets the delete through, and leaves the new
	// role without the policies.
	forced := role
	forced.before = strings.Replace(role.before, `"force_detach_policies":false`, `"force_detach_policies":true`, 1)
	r = run(t, nil, forced, attach)
	none(t, r, "role-delete-conflict")
	if f := only(t, r, "role-recreated"); !strings.Contains(f.Message, "the new role starts without its permissions") {
		t.Errorf("force detach: %s", f.Message)
	}

	// A profile the plan updates with the new role's id is set again.
	reset := rc{addr: "aws_iam_instance_profile.app", actions: "update", before: `{"name":"app","role":"app"}`, after: `{"name":"app"}`, unknown: `{"role":true}`}
	r = run(t, nil, role, reset)
	if f := only(t, r, "role-recreated"); f.Severity != model.Low {
		t.Errorf("nothing here refers to the old role, so only the note about elsewhere: %s %s", f.Severity, f.Message)
	}
}

// A security group or target group replaced by deleting it first, while
// what the plan keeps still uses it.
func TestReplaceInUse(t *testing.T) {
	sg := rc{addr: "aws_security_group.app", actions: "delete,create", before: `{"id":"sg-0123","name":"app","description":"old"}`,
		after: `{"name":"app","description":"new"}`, unknown: `{"id":true,"arn":true}`}
	inst := rc{addr: "aws_instance.web", actions: "update", before: `{"vpc_security_group_ids":["sg-0123"],"instance_type":"t3.micro"}`,
		after: `{"instance_type":"t3.micro"}`, unknown: `{"vpc_security_group_ids":true}`}
	lt := rc{addr: "aws_launch_template.web", actions: "update", before: `{"vpc_security_group_ids":["sg-0123"]}`, after: `{}`, unknown: `{"vpc_security_group_ids":true}`}
	r := run(t, nil, sg, inst, lt)
	f := only(t, r, "replace-in-use")
	if f.Fails != "destroy" || len(f.Addresses) != 2 || f.Addresses[1] != "aws_instance.web" ||
		!strings.Contains(f.Message, "(DependencyViolation)") || !strings.Contains(f.Message, "15 minutes") || !strings.Contains(f.Remediation, "name_prefix") {
		t.Errorf("addresses %v, fails %q: %s / %s", f.Addresses, f.Fails, f.Message, f.Remediation)
	}

	// Created first, the new group takes over before the old one goes.
	cbd := sg
	cbd.actions = "create,delete"
	none(t, run(t, nil, cbd, inst), "replace-in-use")
	// A holder replaced too is destroyed first.
	gone := inst
	gone.actions = "delete,create"
	none(t, run(t, nil, sg, gone), "replace-in-use")

	tg := rc{addr: "aws_lb_target_group.web", actions: "delete,create", before: `{"arn":"arn:aws:elasticloadbalancing:us-east-1:111122223333:targetgroup/web/abc","port":80}`,
		after: `{"port":8080}`, unknown: `{"arn":true}`}
	listener := rc{addr: "aws_lb_listener.http", actions: "update",
		before: `{"default_action":[{"type":"forward","target_group_arn":"arn:aws:elasticloadbalancing:us-east-1:111122223333:targetgroup/web/abc"}]}`,
		after:  `{"default_action":[{"type":"forward"}]}`, unknown: `{"default_action":[{"target_group_arn":true}]}`}
	f = only(t, run(t, nil, tg, listener), "replace-in-use")
	if !strings.Contains(f.Message, "AWS refuses to delete a target group in use (ResourceInUse)") {
		t.Errorf("target group: %s", f.Message)
	}
}

// Deploys: new images, charts and code, in the words a reviewer uses.
func TestDeploys(t *testing.T) {
	defs := func(api, worker string) string {
		return strconv.Quote(`[{"name":"api","image":"123456789012.dkr.ecr.us-east-1.amazonaws.com/api:` + api + `"},{"name":"worker","image":"ghcr.io/acme/worker:` + worker + `"}]`)
	}
	k8s := func(img string) string {
		return `{"spec":[{"template":[{"spec":[{"container":[{"name":"web","image":"` + img + `"}]}]}]}]}`
	}
	r := run(t, nil,
		rc{addr: "aws_ecs_task_definition.api", actions: "delete,create", before: `{"container_definitions":` + defs("1.3.2", "2.0") + `}`, after: `{"container_definitions":` + defs("1.4.0", "2.0") + `}`},
		rc{addr: "kubernetes_deployment.web", actions: "update", before: k8s("nginx:1.25"), after: k8s("nginx@sha256:0123456789abcdef0123456789")},
		rc{addr: "helm_release.ingress", actions: "update", before: `{"chart":"ingress-nginx","version":"4.10.0"}`, after: `{"chart":"ingress-nginx","version":"4.11.2"}`},
		rc{addr: "aws_lambda_function.fn", actions: "update", before: `{"source_code_hash":"abc"}`, after: `{"source_code_hash":"def"}`},
		rc{addr: "aws_lambda_function.same", actions: "update", before: `{"source_code_hash":"abc","timeout":3}`, after: `{"source_code_hash":"abc","timeout":5}`},
	)
	got := map[string]string{}
	for _, ch := range r.Changes {
		got[ch.Address] = ch.Summary
	}
	for addr, want := range map[string]string{
		"aws_ecs_task_definition.api": "deploys api 1.3.2 → 1.4.0",
		"kubernetes_deployment.web":   "deploys nginx 1.25 → sha256:0123456789ab",
		"helm_release.ingress":        "deploys chart ingress-nginx 4.10.0 → 4.11.2",
		"aws_lambda_function.fn":      "deploys new code",
	} {
		if got[addr] != want {
			t.Errorf("%s: %q, want %q", addr, got[addr], want)
		}
	}
	if strings.HasPrefix(got["aws_lambda_function.same"], "deploys") {
		t.Errorf("a timeout change is not a deploy: %q", got["aws_lambda_function.same"])
	}
	var said string
	for _, s := range r.Narrative {
		if strings.HasPrefix(s.Text, "Deploys ") {
			said = s.Text
		}
	}
	if want := "Deploys `api` `1.3.2` → `1.4.0` in `aws_ecs_task_definition.api`"; !strings.HasPrefix(said, want) {
		t.Errorf("narrative: %q, want it to start %q", said, want)
	}
}

// The fix for a replaced database names what forces the replacement, and
// still reads as a sentence when the plan does not say.
func TestStatefulReplaceRemediation(t *testing.T) {
	db := func(reason, paths string) string {
		return `{"address":"aws_db_instance.db","mode":"managed","type":"aws_db_instance","name":"db",` + reason +
			`"provider_name":"registry.terraform.io/hashicorp/aws","change":{"actions":["delete","create"],` +
			`"before":{"storage_encrypted":false},"after":{"storage_encrypted":true},"after_unknown":{}` + paths + `}}`
	}
	for _, c := range []struct{ change, want string }{
		{db(`"action_reason":"replace_because_cannot_update",`, `,"replace_paths":[["storage_encrypted"]]`),
			"If you did not mean to recreate it, revert the change to `storage_encrypted`. If you did,"},
		{db("", ""), "If you did not mean to recreate it, revert the change that forces it. If you did,"},
	} {
		r := Analyze(planJSON(t, "["+c.change+"]", ""), Options{})
		if f := only(t, r, "stateful-replace"); !strings.HasPrefix(f.Remediation, c.want) {
			t.Errorf("remediation = %q, want it to start %q", f.Remediation, c.want)
		}
	}
}
