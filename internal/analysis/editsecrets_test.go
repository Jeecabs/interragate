package analysis

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/plan"
)

// configuredPlan builds a plan from raw resource_changes, variables and
// configuration.
func configuredPlan(t *testing.T, changes, variables, configuration string) *plan.Plan {
	t.Helper()
	doc := `{"format_version":"1.2","terraform_version":"1.16.0","planned_values":{},` +
		`"variables":` + or(variables, "{}") + `,"resource_changes":` + changes + `,"configuration":` + configuration + `}`
	p, err := plan.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// inputUpdate is a resource change that updates input, for the plans below.
func inputUpdate(addr, before, after string, sensitive bool) string {
	module, name, _ := strings.Cut(addr, ".terraform_data.")
	if name == "" {
		module, name = "", strings.TrimPrefix(addr, "terraform_data.")
	}
	mask := `{}`
	if sensitive {
		mask = `{"input":true}`
	}
	s := `{"address":"` + addr + `","mode":"managed","type":"terraform_data","name":"` + name + `",`
	if module != "" {
		s += `"module_address":"` + module + `",`
	}
	return s + `"provider_name":"terraform.io/builtin/terraform","change":{"actions":["update"],` +
		`"before":{"input":` + before + `},"after":{"input":` + after + `},"after_unknown":{},` +
		`"before_sensitive":` + mask + `,"after_sensitive":` + mask + `}}`
}

// Rotating a variable declared sensitive changes nothing the plan shows
// in the clear, and the old value is in no part of the plan at all, so the
// lines that set it are masked for what the variable is, not for how its
// name reads.
func TestEditHidesSensitiveVariables(t *testing.T) {
	files := map[string]string{
		"main.tf": `variable "db_pw" {
  type      = string
  sensitive = true
}

variable "tls_key" {
  type      = string
  sensitive = true
}

variable "seed" {
  sensitive = true
  default   = "Seed-new-0042"
}

resource "terraform_data" "pw" {
  input = sha256(var.db_pw)
}

resource "terraform_data" "cert" {
  input = sha256(var.tls_key)
}

resource "terraform_data" "seeded" {
  input = sha256(var.seed)
}
`,
		// The heredoc ends the file, as a key pasted in last does.
		"terraform.tfvars": `db_pw = "NewSecretPw2"
tls_key = <<EOT
-----BEGIN PRIVATE KEY-----
MIIEvQIBADANBgkqhkiG9w0BAQEFAASCNEWKEYBODY
-----END PRIVATE KEY-----
EOT
`,
	}
	diff := `diff --git a/main.tf b/main.tf
--- a/main.tf
+++ b/main.tf
@@ -13 +13 @@ variable "seed" {
-  default   = "Seed-old-0041"
+  default   = "Seed-new-0042"
diff --git a/terraform.tfvars b/terraform.tfvars
--- a/terraform.tfvars
+++ b/terraform.tfvars
@@ -1 +1 @@
-db_pw = "OldSecretPw1"
+db_pw = "NewSecretPw2"
@@ -4 +4 @@ tls_key = <<EOT
-MIIEvQIBADANBgkqhkiG9w0BAQEFAASCOLDKEYBODY
+MIIEvQIBADANBgkqhkiG9w0BAQEFAASCNEWKEYBODY
`
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p := configuredPlan(t, `[`+
		inputUpdate("terraform_data.pw", `"h1"`, `"h2"`, false)+`,`+
		inputUpdate("terraform_data.cert", `"h3"`, `"h4"`, false)+`,`+
		inputUpdate("terraform_data.seeded", `"h5"`, `"h6"`, false)+`]`,
		`{"db_pw":{"value":"NewSecretPw2"},"seed":{"value":"Seed-new-0042"},
		  "tls_key":{"value":"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCNEWKEYBODY\n-----END PRIVATE KEY-----\n"}}`,
		`{"root_module":{
		  "variables":{"db_pw":{"sensitive":true},"tls_key":{"sensitive":true},"seed":{"sensitive":true,"default":"Seed-new-0042"}},
		  "resources":[
		    {"address":"terraform_data.pw","mode":"managed","type":"terraform_data","name":"pw","expressions":{"input":{"references":["var.db_pw"]}}},
		    {"address":"terraform_data.cert","mode":"managed","type":"terraform_data","name":"cert","expressions":{"input":{"references":["var.tls_key"]}}},
		    {"address":"terraform_data.seeded","mode":"managed","type":"terraform_data","name":"seeded","expressions":{"input":{"references":["var.seed"]}}}]}}`)
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	for addr, want := range map[string]string{
		"terraform_data.pw":     "input terraform.tfvars: -1 db_pw = (sensitive value) | +1 db_pw = (sensitive value)",
		"terraform_data.cert":   "input terraform.tfvars: -4 (sensitive value) | +4 (sensitive value)",
		"terraform_data.seeded": "input main.tf: -13   default   = (sensitive value) | +13   default   = (sensitive value)",
	} {
		if got := describe(change(t, r, addr).Edit); got != want {
			t.Errorf("%s:\n got %s\nwant %s", addr, got, want)
		}
	}
	assertNoSecrets(t, r, "OldSecretPw1", "NewSecretPw2", "OLDKEYBODY", "NEWKEYBODY", "Seed-old-0041", "Seed-new-0042")
}

// In a block, the lines of an argument the plan marks sensitive are masked
// whatever its name, a heredoc or JSON in a string included, while an
// argument that only refers to the secret says where it comes from.
func TestEditHidesSensitiveArguments(t *testing.T) {
	files := map[string]string{"main.tf": `resource "terraform_data" "json" {
  input = jsonencode({ username = "admin", passcode = "Js0n-new-pass" })
}

resource "terraform_data" "escaped" {
  input = "{\"username\":\"admin\",\"passcode\":\"Esc-new-pass\"}"
}

resource "terraform_data" "pem" {
  input = <<EOT
-----BEGIN PRIVATE KEY-----
MIIEvQNEWKEYLINE
-----END PRIVATE KEY-----
EOT
  triggers_replace = ["v2"]
}

resource "terraform_data" "ref" {
  input = var.db_password
}
`}
	diff := `diff --git a/main.tf b/main.tf
--- a/main.tf
+++ b/main.tf
@@ -2 +2 @@ resource "terraform_data" "json" {
-  input = jsonencode({ username = "admin", passcode = "Js0n-old-pass" })
+  input = jsonencode({ username = "admin", passcode = "Js0n-new-pass" })
@@ -6 +6 @@ resource "terraform_data" "escaped" {
-  input = "{\"username\":\"admin\",\"passcode\":\"Esc-old-pass\"}"
+  input = "{\"username\":\"admin\",\"passcode\":\"Esc-new-pass\"}"
@@ -12 +12 @@ resource "terraform_data" "pem" {
-MIIEvQOLDKEYLINE
+MIIEvQNEWKEYLINE
@@ -15 +15 @@ EOT
-  triggers_replace = ["v1"]
+  triggers_replace = ["v2"]
@@ -19 +19 @@ resource "terraform_data" "ref" {
-  input = var.old_password
+  input = var.db_password
@@ -21,4 +20,0 @@ resource "terraform_data" "ref" {
-
-resource "terraform_data" "gone" {
-  input = "Gone-secret-77"
-}
`
	dir := t.TempDir()
	writeFiles(t, dir, files)
	// The plan's values are not the source's: what is masked here is
	// masked by structure, not found by value.
	changes := `[` +
		inputUpdate("terraform_data.json", `"j1"`, `"j2"`, true) + `,` +
		inputUpdate("terraform_data.escaped", `"e1"`, `"e2"`, true) + `,` +
		inputUpdate("terraform_data.pem", `"p1"`, `"p2"`, true) + `,` +
		inputUpdate("terraform_data.ref", `"r1"`, `"r2"`, true) + `,` +
		`{"address":"terraform_data.gone","mode":"managed","type":"terraform_data","name":"gone","action_reason":"delete_because_no_resource_config",
		  "provider_name":"terraform.io/builtin/terraform","change":{"actions":["delete"],"before":{"input":"g1"},"after":null,
		  "after_unknown":{},"before_sensitive":{"input":true},"after_sensitive":false}}]`
	p := configuredPlan(t, changes, "", `{"root_module":{}}`)
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	for addr, want := range map[string]string{
		"terraform_data.json":    "edited main.tf: -2   input = (sensitive value) | +2   input = (sensitive value)",
		"terraform_data.escaped": "edited main.tf: -6   input = (sensitive value) | +6   input = (sensitive value)",
		"terraform_data.pem":     `edited main.tf: -12 (sensitive value) | +12 (sensitive value) | -15   triggers_replace = ["v1"] | +15   triggers_replace = ["v2"]`,
		"terraform_data.ref":     "edited main.tf: -19   input = var.old_password | +19   input = var.db_password",
		"terraform_data.gone":    `removed main.tf: -22 resource "terraform_data" "gone" { | -23   input = (sensitive value) | -24 }`,
	} {
		if got := describe(change(t, r, addr).Edit); got != want {
			t.Errorf("%s:\n got %s\nwant %s", addr, got, want)
		}
	}
	assertNoSecrets(t, r, "Js0n-old-pass", "Js0n-new-pass", "Esc-old-pass", "Esc-new-pass", "OLDKEYLINE", "NEWKEYLINE", "Gone-secret-77")

	// Without the files' content only the hunks are known, and so not
	// where the old block's arguments were: every line it lost is masked.
	r = Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, false)})
	want := "edited main.tf: -2   (sensitive value) | +2   input = (sensitive value)"
	if got := describe(change(t, r, "terraform_data.json").Edit); got != want {
		t.Errorf("without content:\n got %s\nwant %s", got, want)
	}
	assertNoSecrets(t, r, "Js0n-old-pass", "Js0n-new-pass", "Esc-old-pass", "Esc-new-pass", "OLDKEYLINE", "NEWKEYLINE", "Gone-secret-77")
}

// What a module call passes to a variable the module declares sensitive is
// a secret in the call's edit too.
func TestEditHidesSensitiveModuleArguments(t *testing.T) {
	files := map[string]string{
		"main.tf": "module \"db\" {\n  source   = \"./modules/db\"\n  material = \"Mod-new-002\"\n  name     = \"orders\"\n}\n",
		"modules/db/main.tf": "variable \"material\" {\n  sensitive = true\n}\n\nvariable \"name\" {}\n\n" +
			"resource \"terraform_data\" \"x\" {\n  input = sha256(var.material)\n}\n",
	}
	diff := `diff --git a/main.tf b/main.tf
--- a/main.tf
+++ b/main.tf
@@ -3,2 +3,2 @@ module "db" {
-  material = "Mod-old-001"
-  name     = "users"
+  material = "Mod-new-002"
+  name     = "orders"
`
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p := configuredPlan(t, `[`+inputUpdate("module.db.terraform_data.x", `"h1"`, `"h2"`, false)+`]`, "",
		`{"root_module":{"module_calls":{"db":{"source":"./modules/db","module":{"variables":{"material":{"sensitive":true},"name":{}}}}}}}`)
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	want := `module main.tf (module.db): -3   material = (sensitive value) | -4   name     = "users" | +3   material = (sensitive value) | +4   name     = "orders"`
	if got := describe(change(t, r, "module.db.terraform_data.x").Edit); got != want {
		t.Errorf("edit:\n got %s\nwant %s", got, want)
	}
	assertNoSecrets(t, r, "Mod-old-001", "Mod-new-002")
}

// redact finds a secret in the forms source writes it in, and only there:
// the rest of a sensitive JSON document and a line common to a sensitive
// file and another are ordinary text.
func TestRedactSourceForms(t *testing.T) {
	pem := "-----BEGIN PRIVATE KEY-----\nMIIEvQLINEOFKEY\napiVersion: v1\n-----END PRIVATE KEY-----\n"
	doc := `{"engine":"postgres","username":"app_admin","password":"Zq9-pw-88","nested":{"api_key":"Ak-0193-x"}}`
	p := configuredPlan(t, `[`+
		`{"address":"aws_secretsmanager_secret_version.db","mode":"managed","type":"aws_secretsmanager_secret_version","name":"db",
		  "provider_name":"registry.terraform.io/hashicorp/aws","change":{"actions":["update"],
		  "before":{"secret_string":"x"},"after":{"secret_string":`+jsonString(t, doc)+`},"after_unknown":{},
		  "before_sensitive":{"secret_string":true},"after_sensitive":{"secret_string":true}}},`+
		`{"address":"tls_private_key.k","mode":"managed","type":"tls_private_key","name":"k",
		  "provider_name":"registry.terraform.io/hashicorp/tls","change":{"actions":["update"],
		  "before":{"private_key_pem":"x"},"after":{"private_key_pem":`+jsonString(t, pem)+`},"after_unknown":{},
		  "before_sensitive":{"private_key_pem":true},"after_sensitive":{"private_key_pem":true}}},`+
		`{"address":"aws_db_instance.db","mode":"managed","type":"aws_db_instance","name":"db",
		  "provider_name":"registry.terraform.io/hashicorp/aws","change":{"actions":["update"],
		  "before":{"engine":"postgres","username":"app_admin"},"after":{"engine":"aurora-postgresql","username":"app_admin"},"after_unknown":{}}}]`,
		"", `{"root_module":{}}`)
	s := secrets(p)
	for _, want := range []string{doc, hclEscape(doc), pem, hclEscape(pem), "Zq9-pw-88", "Ak-0193-x"} {
		if !contains(s.values, want) {
			t.Errorf("secrets lack %q", want)
		}
	}
	for _, not := range []string{"postgres", "app_admin", "apiVersion: v1"} {
		if contains(s.values, not) {
			t.Errorf("secrets take in %q, which the document only shares with ordinary text", not)
		}
	}
	if !s.lines["MIIEvQLINEOFKEY"] || !s.lines["apiVersion: v1"] {
		t.Errorf("lines = %v", s.lines)
	}

	r := Analyze(p, Options{})
	db := change(t, r, "aws_db_instance.db")
	engine := false
	for _, n := range db.Diff.Children {
		engine = engine || n.Key == "engine" && n.Before == "postgres" && n.After == "aurora-postgresql"
	}
	if !engine {
		t.Errorf("the engine's change is masked: %+v", db.Diff.Children)
	}
	// A line of the key on its own is masked; within other text it is not.
	db.Why = "  MIIEvQLINEOFKEY"
	db.Summary = "kind: Service, apiVersion: v1"
	out := redact(r, s)
	if got := change(t, out, "aws_db_instance.db"); got.Why != "  (sensitive value)" || got.Summary != "kind: Service, apiVersion: v1" {
		t.Errorf("why = %q, summary = %q", got.Why, got.Summary)
	}
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
