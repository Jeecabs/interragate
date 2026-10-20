package analysis

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
	"github.com/Jeecabs/interragate/internal/rules"
)

// policyConfig loads a .interragate.yaml written for the test, so policies
// go through the same validation as a real file.
func policyConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".interragate.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func withPolicies(t *testing.T, fixture, yaml string) *model.Report {
	t.Helper()
	p, err := plan.ReadFile("../../testdata/plans/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	return Analyze(p, Options{Config: policyConfig(t, yaml), Generator: "interragate test"})
}

// flagged lists the addresses a rule's findings name, in order.
func flagged(r *model.Report, rule string) []string {
	var out []string
	for _, f := range findingsFor(r, rule) {
		out = append(out, f.Addresses...)
	}
	return out
}

const awsPolicies = `
policies:
  - id: prod-db-kept
    title: Production databases are never replaced or destroyed
    severity: critical
    message: Talk to the platform team before replacing a production database.
    remediation: Restore from the nightly snapshot instead, or add lifecycle { prevent_destroy = true }.
    match:
      type: [aws_db_instance, aws_rds_cluster]
      action: [delete, replace]
  - id: no-temporary-rules
    title: Temporary firewall rules are not merged
    severity: high
    match:
      category: firewall
    when:
      - attribute: ingress.*.description
        matches: (?i)^temp
      - attribute: ingress
        changes: true
  - id: subnets-kept
    title: Subnets are not destroyed
    severity: medium
    match:
      module: module.network
      type: aws_subnet
      action: destroy
  - id: owner-tag
    title: New resources have an owner
    severity: low
    match:
      action: create
    when:
      - attribute: tags_all.Owner
        present: false
`

func TestPoliciesOnAWSWebapp(t *testing.T) {
	r := withPolicies(t, "aws-webapp.terraform.json", awsPolicies)

	db := expectFinding(t, r, "prod-db-kept", "aws_db_instance.orders", model.Critical)
	if db != nil {
		if db.ID != "prod-db-kept:aws_db_instance.orders" || db.Category != "policy" || db.Title != "Production databases are never replaced or destroyed" {
			t.Errorf("finding = %+v", db)
		}
		want := "`aws_db_instance.orders` is replaced, which the policy \"Production databases are never replaced or destroyed\" does not allow. " +
			"Talk to the platform team before replacing a production database."
		if db.Message != want {
			t.Errorf("message:\n  got  %s\n  want %s", db.Message, want)
		}
		if db.Remediation != "Restore from the nightly snapshot instead, or add lifecycle { prevent_destroy = true }." {
			t.Errorf("remediation = %q", db.Remediation)
		}
		if !slices.Contains(change(t, r, "aws_db_instance.orders").Findings, db.ID) {
			t.Error("the finding should be linked to its change")
		}
	}

	// Every condition must hold: the load balancer's group changes its
	// ingress too, but none of its rules is described as temporary.
	if got := flagged(r, "no-temporary-rules"); !slices.Equal(got, []string{"aws_security_group.app"}) {
		t.Fatalf("no-temporary-rules flagged %v", got)
	}
	temp := findingsFor(r, "no-temporary-rules")[0]
	want := "`aws_security_group.app` is updated, which the policy \"Temporary firewall rules are not merged\" does not allow: " +
		"`ingress[0].description` matches `(?i)^temp` and `ingress` changes."
	if temp.Message != want || temp.Severity != model.High {
		t.Errorf("no-temporary-rules: %s\n  got  %s\n  want %s", temp.Severity, temp.Message, want)
	}
	if !slices.Equal(temp.Paths, []string{"ingress[0].description", "ingress"}) {
		t.Errorf("paths = %v", temp.Paths)
	}

	// One finding per instance, as for interragate's own per-resource rules.
	if n := len(findingsFor(r, "subnets-kept")); n != 2 {
		t.Errorf("subnets-kept findings = %d", n)
	}
	if got := flagged(r, "subnets-kept"); !slices.Equal(got, []string{"module.network.aws_subnet.private[2]", "module.network.aws_subnet.public[2]"}) {
		t.Errorf("subnets-kept flagged %v", got)
	}
	if got := flagged(r, "owner-tag"); !slices.Equal(got, []string{"aws_cloudwatch_metric_alarm.db_cpu", "aws_instance.app", "aws_sns_topic.alerts"}) {
		t.Errorf("owner-tag flagged %v", got)
	}

	// Policies add to interragate's own findings rather than replacing them.
	expectFinding(t, r, "stateful-replace", "aws_db_instance.orders", model.Critical)
	expectFinding(t, r, "public-ingress", "aws_security_group.app", model.Critical)
}

func TestPolicyOverridesAndIgnores(t *testing.T) {
	r := withPolicies(t, "aws-webapp.terraform.json", awsPolicies+`
severity:
  prod-db-kept: medium
disable: [owner-tag]
ignore:
  - rule: no-temporary-rules
    address: aws_security_group.app
    reason: the debug rule is removed in the next change
  - rule: subnets-kept
    address: module.network.aws_subnet.private*
    reason: private subnets are being consolidated
`)
	expectFinding(t, r, "prod-db-kept", "aws_db_instance.orders", model.Medium)
	none(t, r, "owner-tag")
	none(t, r, "no-temporary-rules")
	if got := flagged(r, "subnets-kept"); !slices.Equal(got, []string{"module.network.aws_subnet.public[2]"}) {
		t.Errorf("subnets-kept flagged %v", got)
	}
	reasons := map[string]string{}
	for _, f := range r.Suppressed {
		reasons[f.ID] = f.Reason
	}
	if reasons["no-temporary-rules:aws_security_group.app"] != "the debug rule is removed in the next change" ||
		reasons["subnets-kept:module.network.aws_subnet.private[2]"] != "private subnets are being consolidated" {
		t.Errorf("suppressed = %v", reasons)
	}
	if c := change(t, r, "aws_security_group.app"); slices.Contains(c.Findings, "no-temporary-rules:aws_security_group.app") {
		t.Error("an ignored finding should not mark its change")
	}
}

// Replacing the database makes port and multi_az known only after apply.
func TestPolicyUnknownValuesDoNotMatch(t *testing.T) {
	r := withPolicies(t, "aws-webapp.terraform.json", `
policies:
  - {id: port-is, title: Port is 5432, severity: low, match: {type: aws_db_instance}, when: [{attribute: port, equals: 5432}]}
  - {id: port-is-not, title: Port is not 5432, severity: low, match: {type: aws_db_instance}, when: [{attribute: port, not_equals: 5432}]}
  - {id: port-in, title: Port is a database port, severity: low, match: {type: aws_db_instance}, when: [{attribute: port, in: [5432, 3306]}]}
  - {id: port-matches, title: Port starts with 5, severity: low, match: {type: aws_db_instance}, when: [{attribute: port, matches: "^5"}]}
  - {id: single-az, title: Databases run in one zone, severity: low, match: {type: aws_db_instance}, when: [{attribute: multi_az, equals: false}]}
  - {id: port-set, title: Port is set, severity: low, match: {type: aws_db_instance}, when: [{attribute: port, present: true}]}
  - {id: port-unset, title: Port is not set, severity: low, match: {type: aws_db_instance}, when: [{attribute: port, present: false}]}
  - {id: port-changes, title: Port changes, severity: low, match: {type: aws_db_instance}, when: [{attribute: port, changes: true}]}
`)
	for _, id := range []string{"port-is", "port-is-not", "port-in", "port-matches", "single-az", "port-unset"} {
		none(t, r, id)
	}
	if f := only(t, r, "port-set"); !strings.HasSuffix(f.Message, ": `port` is set (known after apply).") {
		t.Errorf("message = %s", f.Message)
	}
	if f := only(t, r, "port-changes"); !strings.HasSuffix(f.Message, ": `port` changes (known after apply).") {
		t.Errorf("message = %s", f.Message)
	}
}

func TestPolicySensitiveValuesAreNotCompared(t *testing.T) {
	r := withPolicies(t, "aws-webapp.terraform.json", `
policies:
  - {id: known-password, title: T, severity: high, match: {type: aws_db_instance}, when: [{attribute: password, equals: fixture-password-123}]}
  - {id: other-password, title: T, severity: high, match: {type: aws_db_instance}, when: [{attribute: password, not_equals: hunter2}]}
  - {id: password-word, title: T, severity: high, match: {type: aws_db_instance}, when: [{attribute: password, contains: fixture}]}
  - {id: password-set, title: T, severity: high, match: {type: aws_db_instance}, when: [{attribute: password, present: true}]}
  - {id: password-changes, title: T, severity: high, match: {type: aws_db_instance}, when: [{attribute: password, changes: true}]}
`)
	for _, id := range []string{"known-password", "other-password", "password-word", "password-changes"} {
		none(t, r, id)
	}
	only(t, r, "password-set")
	assertNoSecrets(t, r, "fixture-password-123")
}

// Inside a sensitive value, present still looks at the attribute named,
// but messages keep back the keys and positions that lead to it.
func TestPolicySensitiveContainers(t *testing.T) {
	p := planJSON(t, `[{"address":"aws_ssm_parameter.config","mode":"managed","type":"aws_ssm_parameter","name":"config",
	  "provider_name":"registry.terraform.io/hashicorp/aws",
	  "change":{"actions":["create"],"before":null,
	    "after":{"tags":{"db-password-rotation":"weekly"},"value":"{\"Statement\":[{\"Sid\":\"InternalOnly\"}]}"},
	    "after_unknown":{},"after_sensitive":{"tags":true,"value":true}}}]`, "")
	r := Analyze(p, Options{Config: policyConfig(t, `
policies:
  - {id: any-tag, title: T, severity: low, match: {type: aws_ssm_parameter}, when: [{attribute: tags.*, present: true}]}
  - {id: tag-inside-tag, title: T, severity: low, match: {type: aws_ssm_parameter}, when: [{attribute: tags.*.nothing, present: true}]}
  - {id: statement-sid, title: T, severity: low, match: {type: aws_ssm_parameter}, when: [{attribute: value.Statement.Sid, present: true}]}
  - {id: weekly, title: T, severity: low, match: {type: aws_ssm_parameter}, when: [{attribute: tags.*, equals: weekly}]}
`)})
	if f := only(t, r, "any-tag"); !strings.HasSuffix(f.Message, ": `tags.*` is set.") || !slices.Equal(f.Paths, []string{"tags.*"}) {
		t.Errorf("any-tag: %s %v", f.Message, f.Paths)
	}
	none(t, r, "tag-inside-tag")
	if f := only(t, r, "statement-sid"); !strings.HasSuffix(f.Message, ": `value.Statement.Sid` is set.") {
		t.Errorf("statement-sid: %s", f.Message)
	}
	none(t, r, "weekly")
	assertNoSecrets(t, r, "db-password-rotation", "InternalOnly")
}

func TestPolicyDestroyUsesTheCurrentObject(t *testing.T) {
	r := withPolicies(t, "aws-webapp.terraform.json", `
policies:
  - id: full-buckets-kept
    title: Buckets are not destroyed with everything in them
    severity: high
    match: {type: aws_s3_bucket, action: destroy}
    when:
      - attribute: force_destroy
        equals: true
`)
	f := only(t, r, "full-buckets-kept")
	want := "`aws_s3_bucket.logs` is destroyed, which the policy \"Buckets are not destroyed with everything in them\" does not allow: `force_destroy` is `true`."
	if f.Message != want {
		t.Errorf("message:\n  got  %s\n  want %s", f.Message, want)
	}
}

func TestPolicyChanges(t *testing.T) {
	r := withPolicies(t, "aws-webapp.terraform.json", `
policies:
  - {id: ttl-changes, title: T, severity: low, match: {type: aws_route53_record}, when: [{attribute: ttl, changes: true}]}
  - {id: engine-changes, title: T, severity: low, match: {type: aws_db_instance}, when: [{attribute: engine_version, changes: true}]}
  - {id: engine-stays, title: T, severity: low, match: {type: aws_db_instance}, when: [{attribute: engine_version, changes: false}]}
  - {id: encryption-changes, title: T, severity: low, match: {type: aws_db_instance}, when: [{attribute: storage_encrypted, changes: true}]}
  - {id: cidrs-change, title: T, severity: low, match: {type: aws_security_group}, when: [{attribute: ingress.*.cidr_blocks, changes: true}]}
  - {id: cidrs-stay, title: T, severity: low, match: {type: aws_security_group}, when: [{attribute: ingress.*.cidr_blocks, changes: false}]}
`)
	if f := only(t, r, "ttl-changes"); f.Addresses[0] != "aws_route53_record.www" || !strings.HasSuffix(f.Message, ": `ttl` changes.") {
		t.Errorf("ttl-changes = %v %s", f.Addresses, f.Message)
	}
	none(t, r, "engine-changes")
	if f := only(t, r, "engine-stays"); !strings.HasSuffix(f.Message, ": `engine_version` does not change.") {
		t.Errorf("message = %s", f.Message)
	}
	only(t, r, "encryption-changes")
	// With *, changes: true holds when any of the values changes, and
	// changes: false only when none of them does.
	if got := flagged(r, "cidrs-change"); !slices.Equal(got, []string{"aws_security_group.alb", "aws_security_group.app"}) {
		t.Errorf("cidrs-change flagged %v", got)
	}
	if got := flagged(r, "cidrs-stay"); !slices.Equal(got, []string{"aws_security_group.db"}) {
		t.Errorf("cidrs-stay flagged %v", got)
	}
}

// null becoming empty and reformatted JSON are noise, as in interragate's
// diffs.
func TestPolicyChangesIgnoreNoise(t *testing.T) {
	cfg := policyConfig(t, `
policies:
  - {id: tags-change, title: T, severity: low, match: {type: aws_s3_bucket}, when: [{attribute: tags, changes: true}]}
  - {id: policy-changes, title: T, severity: low, match: {type: aws_s3_bucket_policy}, when: [{attribute: policy, changes: true}]}
`)
	r := run(t, cfg,
		rc{addr: "aws_s3_bucket.logs", actions: "update", before: `{"tags":null,"acl":"private"}`, after: `{"tags":{},"acl":"log-delivery-write"}`},
		rc{addr: "aws_s3_bucket_policy.logs", actions: "update", before: `{"policy":"{\"Version\":\"2012-10-17\"}"}`, after: `{"policy":"{\n  \"Version\": \"2012-10-17\"\n}"}`},
	)
	none(t, r, "tags-change")
	none(t, r, "policy-changes")
}

// JSON held in a string, such as an IAM policy, can be followed into.
func TestPolicyLooksInsideJSONStrings(t *testing.T) {
	r := withPolicies(t, "aws-webapp.terraform.json", `
policies:
  - id: iam-names-resources
    title: IAM statements name their resources
    severity: high
    match: {type: "aws_iam_*"}
    when:
      - attribute: policy.Statement.*.Resource.*
        equals: "*"
`)
	f := only(t, r, "iam-names-resources")
	if f.Addresses[0] != "aws_iam_role_policy.app" || !strings.HasSuffix(f.Message, ": `policy.Statement[2].Resource[0]` is `\"*\"`.") {
		t.Errorf("finding = %v %s", f.Addresses, f.Message)
	}
}

// What a plan does to each resource reads naturally, whatever the action.
func TestPolicyMessagesPerAction(t *testing.T) {
	r := withPolicies(t, "mechanics.terraform.json", `
policies:
  - id: watched
    title: Watched resources
    severity: low
    match:
      action: [create, update, replace, delete, read, forget, no-op]
      address:
        - data.local_file.policy_read
        - random_uuid.legacy_id
        - random_string.session_key
        - random_string.imported
        - 'null_resource.queue["*"]'
        - terraform_data.deploy
`)
	got := map[string]string{}
	for _, f := range findingsFor(r, "watched") {
		got[f.Addresses[0]] = strings.TrimSuffix(f.Message, `, which the policy "Watched resources" does not allow.`)
	}
	want := map[string]string{
		"data.local_file.policy_read":     "`data.local_file.policy_read` is read",
		"random_uuid.legacy_id":           "`random_uuid.legacy_id` is removed from the state without being destroyed",
		"random_string.session_key":       "`random_string.session_key` is moved from `random_string.session_secret`",
		"random_string.imported":          "`random_string.imported` is imported and replaced",
		`null_resource.queue["emails"]`:   "`null_resource.queue[\"emails\"]` does not change in this plan, but the policy \"Watched resources\" does not allow it.",
		`null_resource.queue["invoices"]`: "`null_resource.queue[\"invoices\"]` is created",
		`null_resource.queue["orders"]`:   "`null_resource.queue[\"orders\"]` is replaced",
		`null_resource.queue["reports"]`:  "`null_resource.queue[\"reports\"]` is destroyed",
		"terraform_data.deploy":           "`terraform_data.deploy` is updated",
	}
	for addr, msg := range want {
		if got[addr] != msg {
			t.Errorf("%s:\n  got  %s\n  want %s", addr, got[addr], msg)
		}
	}
	if len(got) != len(want) {
		t.Errorf("flagged %v", got)
	}
}

// Without action, a policy covers the objects a plan leaves in place: what
// it creates, updates, replaces or imports.
func TestPolicyDefaultActions(t *testing.T) {
	r := withPolicies(t, "mechanics.terraform.json", `
policies:
  - {id: everything, title: Everything is reviewed, severity: low, match: {type: "*"}}
`)
	got := flagged(r, "everything")
	for _, addr := range []string{`null_resource.queue["invoices"]`, `null_resource.queue["orders"]`, "terraform_data.deploy", "random_string.imported"} {
		if !slices.Contains(got, addr) {
			t.Errorf("%s should be covered", addr)
		}
	}
	for _, addr := range []string{`null_resource.queue["reports"]`, `null_resource.queue["emails"]`, "data.local_file.policy_read",
		"random_uuid.legacy_id", "random_string.session_key", "module.legacy.null_resource.beacon"} {
		if slices.Contains(got, addr) {
			t.Errorf("%s should need action to be covered", addr)
		}
	}
}

// Each condition picks its own element, as the README says, so these two
// are met by different ingress rules.
func TestPolicyConditionsAreCheckedOnTheirOwn(t *testing.T) {
	cfg := policyConfig(t, `
policies:
  - id: ssh-from-anywhere
    title: SSH is never open to the internet
    severity: high
    match: {type: aws_security_group}
    when:
      - attribute: ingress.*.from_port
        equals: 22
      - attribute: ingress.*.cidr_blocks
        contains: 0.0.0.0/0
`)
	r := run(t, cfg, rc{addr: "aws_security_group.web", actions: "create",
		after: `{"ingress":[{"from_port":22,"cidr_blocks":["10.0.0.0/8"]},{"from_port":443,"cidr_blocks":["0.0.0.0/0"]}]}`})
	f := only(t, r, "ssh-from-anywhere")
	if !strings.HasSuffix(f.Message, ": `ingress[0].from_port` is `22` and `ingress[1].cidr_blocks` contains `\"0.0.0.0/0\"`.") {
		t.Errorf("message = %s", f.Message)
	}
}

func TestPolicyConditions(t *testing.T) {
	cfg := policyConfig(t, `
policies:
  - {id: unencrypted-root, title: T, severity: high, match: {type: aws_instance}, when: [{attribute: root_block_device.encrypted, equals: false}]}
  - {id: elb-subnets, title: T, severity: low, match: {type: aws_subnet}, when: [{attribute: 'tags["kubernetes.io/role/elb"]', present: false}]}
  - {id: ssh-port-text, title: T, severity: low, match: {type: aws_security_group}, when: [{attribute: "ingress[0].from_port", equals: "22"}]}
  - {id: small-instances, title: T, severity: low, match: {type: aws_instance}, when: [{attribute: instance_type, in: [t3.micro, t3.small]}]}
  - {id: not-micro, title: T, severity: low, match: {type: aws_instance}, when: [{attribute: instance_type, not_equals: t3.micro}]}
  - {id: temp-names, title: T, severity: low, match: {type: aws_instance}, when: [{attribute: tags.Name, matches: "^tmp-"}]}
  - {id: debug-description, title: T, severity: low, match: {type: aws_security_group}, when: [{attribute: description, contains: debug}]}
  - {id: no-egress-description, title: T, severity: low, match: {type: aws_security_group}, when: [{attribute: egress.*.description, present: false}]}
  - {id: no-key-pair, title: T, severity: low, match: {type: aws_instance}, when: [{attribute: key_name, present: false}]}
  - {id: key-pair-not-ops, title: T, severity: low, match: {type: aws_instance}, when: [{attribute: key_name, not_equals: ops}]}
  - {id: port-is-a-word, title: T, severity: low, match: {type: aws_security_group}, when: [{attribute: "ingress[0].from_port", matches: "2"}]}
  - {id: groups, title: T, severity: low, match: {type: aws_instance}, when: [{attribute: vpc_security_group_ids, contains: sg-web}]}
  - {id: unknown-group, title: T, severity: low, match: {type: aws_instance}, when: [{attribute: vpc_security_group_ids, contains: sg-new}]}
`)
	r := run(t, cfg,
		rc{addr: "aws_instance.app", actions: "create",
			after:   `{"instance_type":"t3.small","tags":{"Name":"tmp-load-test"},"key_name":null,"root_block_device":[{"encrypted":false,"volume_size":20}],"vpc_security_group_ids":[null,"sg-web"]}`,
			unknown: `{"vpc_security_group_ids":[true,false]}`},
		rc{addr: "aws_subnet.public", actions: "create", after: `{"tags":{"kubernetes.io/role/internal-elb":"1"}}`},
		rc{addr: "aws_security_group.debug", actions: "create", after: `{"description":"temporary debug access","egress":[],"ingress":[{"from_port":22,"to_port":22,"cidr_blocks":["10.0.0.0/8"]}]}`},
	)
	for _, id := range []string{"unencrypted-root", "elb-subnets", "ssh-port-text", "small-instances", "not-micro", "temp-names", "debug-description", "no-key-pair", "key-pair-not-ops", "groups"} {
		only(t, r, id)
	}
	// * over an empty list selects nothing, a number is not a string, and
	// an element known only after apply contains nothing yet.
	none(t, r, "no-egress-description")
	none(t, r, "port-is-a-word")
	none(t, r, "unknown-group")
	if f := only(t, r, "unencrypted-root"); !slices.Equal(f.Paths, []string{"root_block_device[0].encrypted"}) {
		t.Errorf("nested blocks need no index: paths = %v", f.Paths)
	}
}

// Unquoted numbers keep the text they were written with, so they match
// the strings providers use for versions and file modes.
func TestPolicyNumbersKeepTheirText(t *testing.T) {
	cfg := policyConfig(t, `
policies:
  - {id: mysql-8, title: T, severity: low, match: {type: aws_db_instance}, when: [{attribute: engine_version, equals: 8.0}]}
  - {id: eks-current, title: T, severity: low, match: {type: aws_eks_cluster}, when: [{attribute: version, in: [1.29, 1.30]}]}
  - {id: open-scripts, title: T, severity: low, match: {type: local_file}, when: [{attribute: file_permission, equals: 0755}]}
  - {id: mysql-port, title: T, severity: low, match: {type: aws_db_instance}, when: [{attribute: port, equals: "3306"}]}
  - {id: mysql-port-number, title: T, severity: low, match: {type: aws_db_instance}, when: [{attribute: port, equals: 3306.0}]}
`)
	r := run(t, cfg,
		rc{addr: "aws_db_instance.mysql", actions: "create", after: `{"engine_version":"8.0","port":3306}`},
		rc{addr: "aws_eks_cluster.main", actions: "create", after: `{"version":"1.30"}`},
		rc{addr: "local_file.deploy", actions: "create", after: `{"file_permission":"0755"}`},
	)
	for _, id := range []string{"mysql-8", "eks-current", "open-scripts", "mysql-port", "mysql-port-number"} {
		only(t, r, id)
	}
	if f := only(t, r, "eks-current"); !strings.HasSuffix(f.Message, ": `version` is `1.29` or `1.30`.") {
		t.Errorf("message = %s", f.Message)
	}
}

func TestPolicyPathsAndModules(t *testing.T) {
	cfg := policyConfig(t, `
policies:
  - {id: negative-step, title: T, severity: low, match: {type: aws_security_group}, when: [{attribute: ingress.-1.from_port, present: true}]}
  - {id: no-access-logs, title: T, severity: low, match: {type: aws_lb}, when: [{attribute: access_logs.enabled, not_equals: true}]}
  - {id: wildcard-resource, title: T, severity: high, match: {type: aws_iam_policy}, when: [{attribute: policy.Statement.*.Resource.*, equals: "*"}]}
  - {id: in-app, title: T, severity: low, match: {module: module.app}}
  - {id: in-a-module, title: T, severity: low, match: {module: "*"}}
  - {id: aws, title: T, severity: low, match: {provider: AWS, type: aws_lb}}
`)
	r := run(t, cfg,
		rc{addr: "aws_security_group.web", actions: "create", after: `{"ingress":[{"from_port":22}]}`},
		rc{addr: "aws_lb.web", actions: "create", after: `{"access_logs":[]}`},
		rc{addr: "aws_iam_policy.one", actions: "create", after: `{"policy":"{\"Statement\":[{\"Resource\":\"*\"}]}"}`},
		rc{addr: "aws_iam_policy.many", actions: "create", after: `{"policy":"{\"Statement\":[{\"Resource\":[\"arn:aws:s3:::logs\",\"*\"]}]}"}`},
		rc{addr: "aws_iam_policy.scoped", actions: "create", after: `{"policy":"{\"Statement\":[{\"Resource\":\"arn:aws:s3:::logs/*\"}]}"}`},
		rc{addr: `module.app["blue"].module.db.aws_db_instance.main`, actions: "create", after: `{}`},
	)
	// -1 is a key here, not a position, so it finds nothing.
	none(t, r, "negative-step")
	// An empty list is how a plan writes a block that is not there.
	only(t, r, "no-access-logs")
	// * over a single value selects it, so both of IAM's forms are covered.
	if got := flagged(r, "wildcard-resource"); !slices.Equal(got, []string{"aws_iam_policy.one", "aws_iam_policy.many"}) {
		t.Errorf("wildcard-resource flagged %v", got)
	}
	// A module covers the modules it calls, and the root module is none.
	for _, id := range []string{"in-app", "in-a-module"} {
		if got := flagged(r, id); !slices.Equal(got, []string{`module.app["blue"].module.db.aws_db_instance.main`}) {
			t.Errorf("%s flagged %v", id, got)
		}
	}
	only(t, r, "aws")
}

func TestPolicyIDsCannotTakeBuiltInRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".interragate.yaml")
	if err := os.WriteFile(path, []byte("policies: [{id: public-ingress, title: T, severity: high, match: {type: x}}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path, config.ReserveRuleIDs(rules.IDs()...)); err == nil || !strings.Contains(err.Error(), "id is taken by a built-in rule") {
		t.Fatalf("err = %v", err)
	}
}
