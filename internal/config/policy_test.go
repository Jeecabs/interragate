package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, yaml string) (*Config, string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".interragate.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	// Package rules would reserve every built-in ID; one stands for them.
	c, err := Load(path, ReserveRuleIDs("stateful-destroy"))
	return c, path, err
}

func TestLoadPolicies(t *testing.T) {
	c, _, err := load(t, `
policies:
  - id: prod-db-kept
    title: Production databases are never replaced or destroyed
    severity: critical
    message: Talk to the platform team first.
    remediation: Restore from the nightly snapshot instead.
    match:
      type: [aws_db_instance, aws_rds_cluster]
      address: "module.prod.*"
      provider: aws
      action: [destroy, replace]
      category: database
      stateful: true
    when:
      - attribute: publicly_accessible
        equals: true
      - attribute: tags.Owner
        present: false
      - attribute: instance_class
        in: [db.t4g.small, db.t4g.medium]
      - attribute: port
        not_equals: 5432
  - id: no-temp-rules
    title: Temporary firewall rules are not merged
    severity: medium
    match: {category: firewall}
    when:
      - attribute: ingress[*].description
        matches: (?i)^temp
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Policies) != 2 {
		t.Fatalf("policies = %+v", c.Policies)
	}
	p := c.Policies[0]
	m := p.Match
	if p.ID != "prod-db-kept" || p.Severity != "critical" || p.Remediation == "" {
		t.Errorf("policy = %+v", p)
	}
	if fmt.Sprint(m.Type, m.Address, m.Provider, m.Action, m.Category) != "[aws_db_instance aws_rds_cluster] [module.prod.*] [aws] [destroy replace] [database]" {
		t.Errorf("one value and lists should both read as lists: %+v", m)
	}
	if m.Stateful == nil || !*m.Stateful {
		t.Errorf("stateful = %v", m.Stateful)
	}
	want := []Condition{
		{"publicly_accessible", "equals", true},
		{"tags.Owner", "present", false},
		{"instance_class", "in", []any{"db.t4g.small", "db.t4g.medium"}},
		{"port", "not_equals", 5432},
	}
	if fmt.Sprint(p.When) != fmt.Sprint(want) {
		t.Errorf("when = %v, want %v", p.When, want)
	}
	if c.Policies[1].When[0].Operator != "matches" {
		t.Errorf("second policy = %+v", c.Policies[1])
	}
}

// Unquoted numbers keep the text they were written with, so 8.0 and 1.30
// still match version strings and 0755 a file mode.
func TestPolicyNumbersKeepTheirText(t *testing.T) {
	c, _, err := load(t, `
policies:
  - id: a
    title: 1.10
    severity: low
    match: {type: x}
    when:
      - {attribute: engine_version, equals: 8.0}
      - {attribute: version, in: [1.29, 1.30]}
      - {attribute: file_permission, equals: 0755}
      - {attribute: port, equals: 3306}
      - {attribute: encrypted, equals: true}
`)
	if err != nil {
		t.Fatal(err)
	}
	p := c.Policies[0]
	got := fmt.Sprintf("%q %#v %#v %#v %#v %#v", p.Title, p.When[0].Value, p.When[1].Value, p.When[2].Value, p.When[3].Value, p.When[4].Value)
	want := `"1.10" "8.0" []interface {}{"1.29", "1.30"} "0755" "3306" true`
	if got != want {
		t.Errorf("values:\n  got  %s\n  want %s", got, want)
	}
	if _, isNumber := p.When[0].Value.(json.Number); !isNumber {
		t.Errorf("8.0 should stay a number: %T", p.When[0].Value)
	}
	if _, isString := p.When[2].Value.(string); !isString {
		t.Errorf("0755 is not a JSON number, so it stays as written: %T", p.When[2].Value)
	}
}

// Anchors and merge keys let policies share a match.
func TestPolicyAnchors(t *testing.T) {
	c, _, err := load(t, `
policies:
  - id: prod-db-kept
    title: Production databases are never replaced or destroyed
    severity: critical
    match: &prod-dbs
      type: [aws_db_instance, aws_rds_cluster]
      action: [delete, replace]
  - id: prod-db-private
    title: Production databases stay private
    severity: high
    match:
      <<: *prod-dbs
      action: [create, update]
    when:
      - attribute: publicly_accessible
        equals: true
`)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Policies[1].Match
	if fmt.Sprint(m.Type, m.Action) != "[aws_db_instance aws_rds_cluster] [create update]" {
		t.Errorf("merged match = %+v", m)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	c, err := Load("../../examples/.interragate.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Policies) != 2 {
		t.Errorf("example policies = %d", len(c.Policies))
	}
}

func TestParsePath(t *testing.T) {
	for path, want := range map[string]string{
		"publicly_accessible":            "[{publicly_accessible false}]",
		"tags.Owner":                     "[{tags false} {Owner false}]",
		"ingress.*.cidr_blocks":          "[{ingress false} { true} {cidr_blocks false}]",
		"ingress[*].cidr_blocks":         "[{ingress false} { true} {cidr_blocks false}]",
		"ingress.0.from_port":            "[{ingress false} {0 false} {from_port false}]",
		"ingress[0].from_port":           "[{ingress false} {0 false} {from_port false}]",
		`tags["kubernetes.io/role/elb"]`: "[{tags false} {kubernetes.io/role/elb false}]",
		`tags["*"]`:                      "[{tags false} {* false}]",
		"matrix[0][1]":                   "[{matrix false} {0 false} {1 false}]",
	} {
		steps, err := ParsePath(path)
		if err != nil {
			t.Errorf("ParsePath(%q): %v", path, err)
			continue
		}
		if got := fmt.Sprint(steps); got != want {
			t.Errorf("ParsePath(%q) = %s, want %s", path, got, want)
		}
	}
}

func TestPolicyErrors(t *testing.T) {
	ok := func(id string) string { return `{id: ` + id + `, title: T, severity: high, match: {type: x}}` }
	cond := func(c string) string {
		return `policies: [{id: a, title: T, severity: high, match: {type: x}, when: [` + c + `]}]`
	}
	for _, tc := range []struct{ name, yaml, want string }{
		{"missing id", `policies: [{title: T, severity: high, match: {type: x}}]`,
			`policies[0]: id is required`},
		{"id not kebab-case", `policies: [` + ok("Prod_DB") + `]`,
			`policies[0] (Prod_DB): id must be kebab-case: lowercase letters and digits joined by hyphens, such as prod-db-kept`},
		{"built-in rule id", `policies: [` + ok("stateful-destroy") + `]`,
			`policies[0] (stateful-destroy): id is taken by a built-in rule; choose another (to change the built-in rule, use severity or disable)`},
		{"duplicate id", `policies: [` + ok("a") + `, ` + ok("b") + `, ` + ok("a") + `]`,
			`policies[2] (a): id is already used by policies[0]`},
		{"missing title", `policies: [{id: a, severity: high, match: {type: x}}]`,
			`policies[0] (a): title is required`},
		{"missing severity", `policies: [{id: a, title: T, match: {type: x}}]`,
			`policies[0] (a): severity is required; use critical, high, medium, low or info`},
		{"bad severity", `policies: [{id: a, title: T, severity: urgent, match: {type: x}}]`,
			`policies[0] (a): unknown severity "urgent"; use critical, high, medium, low or info`},
		{"severity none", `policies: [{id: a, title: T, severity: none, match: {type: x}}]`,
			`policies[0] (a): unknown severity "none"; use critical, high, medium, low or info`},
		{"unknown policy key", `policies: [{id: a, title: T, severty: high, match: {type: x}}]`,
			`policies[0] (a): unknown key "severty"; use id, title, severity, message, remediation, match or when`},
		{"title is a list", `policies: [{id: a, title: [T], severity: high, match: {type: x}}]`,
			`policies[0] (a): title must be text, not a list or mapping`},
		{"unknown match key", `policies: [{id: a, title: T, severity: high, match: {typ: x}}]`,
			`policies[0] (a): unknown match key "typ"; use type, address, module, provider, action, category, stateful or origin`},
		{"empty match", `policies: [{id: a, title: T, severity: high, match: {}}]`,
			`policies[0] (a): match is empty; give at least one of type, address, module, provider, action, category, stateful or origin (type: "*" matches every resource)`},
		{"no match", `policies: [{id: a, title: T, severity: high}]`,
			`policies[0] (a): match is empty; give at least one of type, address, module, provider, action, category, stateful or origin (type: "*" matches every resource)`},
		{"match is a list", `policies: [{id: a, title: T, severity: high, match: [aws_db_instance]}]`,
			`policies[0] (a): match must be a mapping, such as match: {type: aws_db_instance}`},
		{"unknown action", `policies: [` + ok("a") + `, ` + ok("b") + `, {id: prod-db-kept, title: T, severity: critical, match: {type: aws_db_instance, action: [replace, destory]}}]`,
			`policies[2] (prod-db-kept): unknown action "destory"; use create, update, replace, delete, read, forget or no-op`},
		{"unknown category", `policies: [{id: a, title: T, severity: high, match: {category: databse}}]`,
			`policies[0] (a): unknown category "databse"; use network, firewall, identity, secrets, dns, edge, compute, containers, serverless, database, storage, messaging, monitoring, utility or other`},
		{"type is a mapping", `policies: [{id: a, title: T, severity: high, match: {type: {aws: db}}}]`,
			`policies[0] (a): match.type must be a string or a list of strings`},
		{"empty type list", `policies: [{id: a, title: T, severity: high, match: {type: []}}]`,
			`policies[0] (a): match.type is an empty list`},
		{"empty pattern", `policies: [{id: a, title: T, severity: high, match: {address: ""}}]`,
			`policies[0] (a): match.address has an empty value`},
		{"stateful is not a bool", `policies: [{id: a, title: T, severity: high, match: {stateful: maybe}}]`,
			`policies[0] (a): match.stateful must be true or false`},
		{"when is a mapping", `policies: [{id: a, title: T, severity: high, match: {type: x}, when: {attribute: y, equals: 1}}]`,
			`policies[0] (a): when must be a list of conditions, each starting with -`},
		{"condition is not a mapping", cond(`publicly_accessible`),
			`policies[0] (a): when[0]: a condition must be a mapping, such as {attribute: publicly_accessible, equals: true}`},
		{"no operator", cond(`{attribute: publicly_accessible}`),
			`policies[0] (a): when[0]: has no operator; use one of equals, not_equals, in, matches, contains, present or changes`},
		{"two operators", cond(`{attribute: port, equals: 22, in: [22, 2222]}`),
			`policies[0] (a): when[0]: has both equals and in; use one operator per condition`},
		{"three operators", cond(`{attribute: port, equals: 22, in: [22], present: true}`),
			`policies[0] (a): when[0]: has equals, in and present; use one operator per condition`},
		{"unknown operator", cond(`{attribute: port, equal: 22}`),
			`policies[0] (a): when[0]: unknown key "equal"; use attribute and one of equals, not_equals, in, matches, contains, present or changes`},
		{"bad regex", cond(`{attribute: identifier, matches: "(prod"}`),
			`policies[0] (a): when[0]: matches "(prod" is not a valid regular expression: missing closing )`},
		{"matches a number", cond(`{attribute: port, matches: 22}`),
			`policies[0] (a): when[0]: matches takes a regular expression, written as a string`},
		{"in without a list", cond(`{attribute: instance_class, in: db.t4g.small}`),
			`policies[0] (a): when[0]: in takes a list of values, such as in: [db.t4g.small, db.t4g.medium]`},
		{"present is not a bool", cond(`{attribute: tags.Owner, present: yes}`),
			`policies[0] (a): when[0]: present takes true or false`},
		{"equals nothing", cond(`{attribute: tags.Owner, equals: null}`),
			`policies[0] (a): when[0]: equals needs a value; to check whether tags.Owner is set at all, use present`},
		{"second condition", `policies: [{id: a, title: T, severity: high, match: {type: x}, when: [{attribute: x, present: true}, {attribute: y, changes: 1}]}]`,
			`policies[0] (a): when[1]: changes takes true or false`},
		{"no attribute", cond(`{equals: true}`),
			`policies[0] (a): when[0]: attribute is required`},
		{"empty step", cond(`{attribute: tags..Owner, present: true}`),
			`policies[0] (a): when[0]: attribute "tags..Owner" has an empty step between dots`},
		{"leading dot", cond(`{attribute: .tags, present: true}`),
			`policies[0] (a): when[0]: attribute ".tags" starts with a dot`},
		{"trailing dot", cond(`{attribute: tags., present: true}`),
			`policies[0] (a): when[0]: attribute "tags." ends with a dot`},
		{"dot before bracket", cond(`{attribute: "ingress.[0]", present: true}`),
			`policies[0] (a): when[0]: attribute "ingress.[0]" has a dot right before [`},
		{"word in brackets", cond(`{attribute: "ingress[first].from_port", present: true}`),
			`policies[0] (a): when[0]: attribute "ingress[first].from_port" has [first]; brackets hold a number, * or a quoted key such as ["Cost Center"]`},
		{"unclosed bracket", cond(`{attribute: "ingress[0", present: true}`),
			`policies[0] (a): when[0]: attribute "ingress[0" has a [ that is not closed`},
		{"unclosed quote", cond(`{attribute: 'tags["Owner', present: true}`),
			`policies[0] (a): when[0]: attribute "tags[\"Owner" has a quoted key that is not closed`},
		{"text after bracket", cond(`{attribute: "ingress[0]from_port", present: true}`),
			`policies[0] (a): when[0]: attribute "ingress[0]from_port" needs a dot or [ after ]`},
		{"partial wildcard", cond(`{attribute: "tags.Own*", present: true}`),
			`policies[0] (a): when[0]: attribute "tags.Own*" mixes * with other characters; * stands for a whole step`},
		{"space in a name", cond(`{attribute: tags.Cost Center, present: true}`),
			`policies[0] (a): when[0]: attribute "tags.Cost Center" has a space, quote or ] in a name; put keys like that in brackets, as in tags["Cost Center"]`},
		{"null in a list", cond(`{attribute: instance_class, in: [db.t4g.small, null]}`),
			`policies[0] (a): when[0]: in cannot hold null; to check whether instance_class is set at all, use present`},
		{"negative index", cond(`{attribute: "ingress[-1].from_port", present: true}`),
			`policies[0] (a): when[0]: attribute "ingress[-1].from_port" has [-1]; brackets hold a number, * or a quoted key such as ["Cost Center"]`},
		{"duplicate key", "policies:\n  - id: a\n    title: T\n    severity: high\n    id: b\n    match: {type: x}\n",
			`policies[0]: line 5: mapping key "id" already defined at line 2`},
		{"policies is a mapping", `policies: {prod-db-kept: {}}`,
			`policies must be a list, with each policy starting with -`},
		{"policy is a string", `policies: [prod-db-kept]`,
			`policies[0]: a policy must be a mapping with id, title, severity and match`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, path, err := load(t, tc.yaml)
			if err == nil {
				t.Fatal("expected an error")
			}
			if want := path + ": " + tc.want; err.Error() != want {
				t.Errorf("error:\n  got  %s\n  want %s", err, want)
			}
		})
	}
}
