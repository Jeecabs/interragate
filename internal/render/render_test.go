package render_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/analysis"
	"github.com/Jeecabs/interragate/internal/gitdiff"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
	"github.com/Jeecabs/interragate/internal/render/annotations"
	"github.com/Jeecabs/interragate/internal/render/html"
	"github.com/Jeecabs/interragate/internal/render/markdown"
	"github.com/Jeecabs/interragate/internal/render/mermaid"
	"github.com/Jeecabs/interragate/internal/render/sarif"
	"github.com/Jeecabs/interragate/internal/render/text"
)

func report(t *testing.T) *model.Report {
	t.Helper()
	p, err := plan.ReadFile("../../testdata/plans/aws-webapp.terraform.json")
	if err != nil {
		t.Fatal(err)
	}
	return analysis.Analyze(p, analysis.Options{SourceDir: "../../testdata/scenarios/aws-webapp/v2", Generator: "interragate test"})
}

func TestText(t *testing.T) {
	var b bytes.Buffer
	if err := text.Render(&b, report(t), text.Options{Width: 100}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"Critical risk. Destroys an S3 bucket", "What matters", "module.network", "Findings (", "moved {", "13 cosmetic updates hidden",
		"When you apply\n  ● The apply should fail at aws_db_instance.orders, where deletion protection is on",
		"· Once that is fixed, the apply should take about 16 minutes, most of it replacing\n    aws_db_instance.orders.\n",
		"aws_db_instance.orders will not exist for about 15 minutes while"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output lacks %q", want)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("colour codes without Color")
	}
	for _, line := range strings.Split(out, "\n") {
		if n := len([]rune(line)); n > 100 {
			t.Errorf("line wider than 100 columns (%d): %q", n, line)
		}
	}
}

func TestTextShowsEdits(t *testing.T) {
	r := report(t)
	r.Git = &model.GitInfo{Base: "origin/main", Commit: "abc1234", Files: []string{"main.tf"}}
	byAddr := map[string]*model.Change{}
	for _, ch := range r.Changes {
		byAddr[ch.Address] = ch
	}
	db := byAddr["aws_db_instance.orders"]
	db.Edit = &model.Edit{File: "main.tf", Kind: "edited", Omitted: 3, Lines: []model.EditLine{
		{Op: "-", Line: 40, Text: "  storage_encrypted = false"},
		{Op: "+", Line: 40, Text: "  storage_encrypted = true \x1b[2J"},
	}}
	subnet := byAddr["module.network.aws_subnet.private[1]"]
	subnet.Edit = &model.Edit{File: "main.tf", Kind: "module", Module: "module.network", Lines: []model.EditLine{
		{Op: "-", Line: 7, Text: `  azs = ["a", "b", "c"]`},
		{Op: "+", Line: 7, Text: `  azs = ["a", "c"]`},
	}}

	var b bytes.Buffer
	if err := text.RenderChange(&b, r, db, text.Options{Width: 100}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"\nYour edit\n  Changed in main.tf since origin/main.\n  -   storage_encrypted = false\n", "# (3 more changed lines)"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q:\n%s", want, out)
		}
	}
	// A line from the configuration cannot drive the terminal.
	if strings.Contains(out, "\x1b") {
		t.Errorf("escape sequence printed:\n%q", out)
	}

	b.Reset()
	if err := text.RenderChange(&b, r, subnet, text.Options{Width: 100}); err != nil {
		t.Fatal(err)
	}
	if want := "The call to module.network changed in main.tf since origin/main; this block did not."; !strings.Contains(b.String(), want) {
		t.Errorf("explain output lacks %q:\n%s", want, b.String())
	}

	// A change that follows from the edit says where it starts.
	b.Reset()
	if err := text.RenderChange(&b, r, byAddr["aws_ssm_parameter.db_endpoint"], text.Options{Width: 100}); err != nil {
		t.Fatal(err)
	}
	if want := "  It starts with your edit to aws_db_instance.orders, in main.tf.\n"; !strings.Contains(b.String(), want) {
		t.Errorf("explain lacks %q:\n%s", want, b.String())
	}

	b.Reset()
	if err := text.Render(&b, r, text.Options{Width: 100}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"edited · kms_key_id", "module call edited · "} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("changes list lacks %q", want)
		}
	}
}

// explain says what a firewall change lets in, in words.
func TestTextShowsAccess(t *testing.T) {
	r := report(t)
	for _, ch := range r.Changes {
		if ch.Address != "aws_security_group.app" {
			continue
		}
		var b bytes.Buffer
		if err := text.RenderChange(&b, r, ch, text.Options{Width: 100}); err != nil {
			t.Fatal(err)
		}
		if want := "\nNetwork access\n  + Inbound TCP 22 (SSH) from 0.0.0.0/0 (anywhere)\n"; !strings.Contains(b.String(), want) {
			t.Errorf("explain lacks %q:\n%s", want, b.String())
		}
		return
	}
	t.Fatal("aws_security_group.app not in the plan")
}

// The diagram draws what changes, as Terraform marks it, grouped by
// module, leaving out updates that only touch tags.
func TestMermaid(t *testing.T) {
	var b bytes.Buffer
	if err := mermaid.Render(&b, report(t), mermaid.Options{}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"flowchart LR\n", "-/+\u00a0aws_db_instance.orders\"]:::replace", "subgraph m0[\"module.network\"]",
		"aws_subnet.private · -\u00a01 · -/+\u00a01\"]:::delete", "aws_security_group.db\"]:::keep"} {
		if !strings.Contains(out, want) {
			t.Errorf("diagram lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "aws_kms_key.data") {
		t.Error("a tag-only update is drawn")
	}
	var md bytes.Buffer
	if err := markdown.Render(&md, report(t), markdown.Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md.String(), "<details><summary><b>Map</b></summary>\n\n```mermaid\nflowchart LR\n") {
		t.Error("the pull request comment has no map")
	}
	if err := mermaid.Render(&b, report(t), mermaid.Options{Max: 5}); err == nil {
		t.Error("a diagram larger than Max")
	}
}

func TestMarkdown(t *testing.T) {
	var b bytes.Buffer
	if err := markdown.Render(&b, report(t), markdown.Options{ReportURL: "https://ci.example/report.html"}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"### 🔴", "**Destroys an S3 bucket", "<details><summary><b>Findings (", "```hcl\nmoved {", "[interactive report](https://ci.example/report.html)",
		"**When you apply**\n\n- 🟠 The apply should fail at `aws_db_instance.orders`, where deletion protection is on",
		"- Once that is fixed, the apply should take about 16 minutes, most of it replacing `aws_db_instance.orders`.\n- 🟡 `aws_db_instance.orders` will not exist",
		"<details open><summary><b>Access (3 changes)</b></summary>\n\n`aws_iam_role_policy.app`\n- **+** **Allows `s3:*` on every resource**\n\n`aws_security_group.app`\n- **+** **Inbound TCP 22 (SSH) from `0.0.0.0/0` (anywhere)**\n\n`aws_security_group.alb`\n- **−** Inbound TCP 8443"} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
	if len(out) > markdown.MaxLen+5000 {
		t.Errorf("comment is %d bytes", len(out))
	}
}

func TestHTMLEmbedsSafely(t *testing.T) {
	r := report(t)
	// A hostile value must not be able to close the script element.
	r.Changes[0].Why = `</script><script>alert(1)</script>`
	var b bytes.Buffer
	if err := html.Render(&b, r); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	// The theme script, the data block, the pull request comment and the
	// app: a hostile value would add a fifth.
	if n := strings.Count(out, "</script>"); n != 4 {
		t.Fatalf("expected 4 closing script tags, found %d", n)
	}
	md := regexp.MustCompile(`(?s)<script id="interragate-markdown" type="application/json">(.*?)</script>`).FindStringSubmatch(out)
	var comment string
	if md == nil || json.Unmarshal([]byte(md[1]), &comment) != nil || !strings.Contains(comment, "### ") {
		t.Fatal("the pull request comment is missing or does not decode")
	}
	m := regexp.MustCompile(`(?s)<script id="interragate-data" type="application/json">(.*?)</script>`).FindStringSubmatch(out)
	if m == nil {
		t.Fatal("data block missing")
	}
	var back model.Report
	if err := json.Unmarshal([]byte(m[1]), &back); err != nil {
		t.Fatalf("embedded data does not parse: %v", err)
	}
	if back.Changes[0].Why != r.Changes[0].Why {
		t.Error("data did not round-trip")
	}
	if strings.Contains(out, "fixture-password-123") {
		t.Error("report leaks a sensitive value")
	}
	// Self-contained: nothing is loaded from the network.
	if m := regexp.MustCompile(`(?i)(src|href)\s*=\s*["']?https?:|url\(\s*["']?https?:|@import|fetch\(|XMLHttpRequest`).FindString(out); m != "" {
		t.Errorf("report loads remote content: %q", m)
	}
}

func TestMarkdownSplitsChangesByOrigin(t *testing.T) {
	r := report(t)
	r.Git = &model.GitInfo{Base: "origin/main", Commit: "abc1234", Files: []string{"main.tf"}}
	mine := 0
	for _, ch := range r.Changes {
		if ch.Action != model.NoOp && mine < 3 {
			ch.Origin = "edit"
			mine++
		}
	}
	var b bytes.Buffer
	if err := markdown.Render(&b, r, markdown.Options{}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	nobody, yours := strings.Index(out, "<b>Not tied to an edit ("), strings.Index(out, "<b>From your edits (3)</b>")
	if nobody < 0 || yours < 0 || nobody > yours {
		t.Errorf("want the changes nobody edited first, then yours:\n%s", out)
	}
}

// No output shows a secret from the lines of an edit: a rotated password
// the plan never shows, or one in JSON, given to jsonencode or written in
// a string.
func TestOutputsKeepEditedSecretsHidden(t *testing.T) {
	files := map[string]string{
		"main.tf": `variable "db_pw" {
  type      = string
  sensitive = true
}

resource "terraform_data" "pw" {
  input = sha256(var.db_pw)
}

resource "aws_secretsmanager_secret_version" "json" {
  secret_id     = "db"
  secret_string = jsonencode({ username = "admin", password = "Sup3rS3cretJson" })
}

resource "aws_secretsmanager_secret_version" "escaped" {
  secret_id     = "db"
  secret_string = "{\"username\":\"admin\",\"password\":\"Sup3rS3cretNew\"}"
}
`,
		"terraform.tfvars": "db_pw = \"NewSecretPw2\"\n",
	}
	diff := `diff --git a/main.tf b/main.tf
--- a/main.tf
+++ b/main.tf
@@ -12 +12 @@ resource "aws_secretsmanager_secret_version" "json" {
-  secret_string = jsonencode({ username = "admin", password = "OldJsonPass1" })
+  secret_string = jsonencode({ username = "admin", password = "Sup3rS3cretJson" })
@@ -17 +17 @@ resource "aws_secretsmanager_secret_version" "escaped" {
-  secret_string = "{\"username\":\"admin\",\"password\":\"OldEscPass1\"}"
+  secret_string = "{\"username\":\"admin\",\"password\":\"Sup3rS3cretNew\"}"
diff --git a/terraform.tfvars b/terraform.tfvars
--- a/terraform.tfvars
+++ b/terraform.tfvars
@@ -1 +1 @@
-db_pw = "OldSecretPw1"
+db_pw = "NewSecretPw2"
`
	secrets := []string{"OldSecretPw1", "NewSecretPw2", "OldJsonPass1", "Sup3rS3cretJson", "OldEscPass1", "Sup3rS3cretNew"}
	dir := t.TempDir()
	d := &gitdiff.Diff{Base: "origin/main", Commit: "abc1234", Branch: "feature"}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range gitdiff.Parse([]byte(diff)) {
		if !f.Rebuild([]byte(files[f.Path])) {
			t.Fatalf("the diff does not fit %s", f.Path)
		}
		d.Files = append(d.Files, f)
	}
	secret := func(addr, name, old, new string) string {
		return `{"address":"` + addr + `","mode":"managed","type":"aws_secretsmanager_secret_version","name":"` + name + `",` +
			`"provider_name":"registry.terraform.io/hashicorp/aws","change":{"actions":["update"],` +
			`"before":{"secret_id":"db","secret_string":` + old + `},"after":{"secret_id":"db","secret_string":` + new + `},"after_unknown":{},` +
			`"before_sensitive":{"secret_string":true},"after_sensitive":{"secret_string":true}}}`
	}
	p, err := plan.Parse([]byte(`{"format_version":"1.2","terraform_version":"1.16.0","planned_values":{},
	  "variables":{"db_pw":{"value":"NewSecretPw2"}},
	  "resource_changes":[
	    {"address":"terraform_data.pw","mode":"managed","type":"terraform_data","name":"pw","provider_name":"terraform.io/builtin/terraform",
	     "change":{"actions":["update"],"before":{"input":"h1"},"after":{"input":"h2"},"after_unknown":{}}},` +
		secret("aws_secretsmanager_secret_version.json", "json",
			`"{\"password\":\"OldJsonPass1\",\"username\":\"admin\"}"`, `"{\"password\":\"Sup3rS3cretJson\",\"username\":\"admin\"}"`) + `,` +
		secret("aws_secretsmanager_secret_version.escaped", "escaped",
			`"{\"username\":\"admin\",\"password\":\"OldEscPass1\"}"`, `"{\"username\":\"admin\",\"password\":\"Sup3rS3cretNew\"}"`) + `],
	  "configuration":{"root_module":{"variables":{"db_pw":{"sensitive":true}},
	    "resources":[{"address":"terraform_data.pw","mode":"managed","type":"terraform_data","name":"pw","expressions":{"input":{"references":["var.db_pw"]}}}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := analysis.Analyze(p, analysis.Options{SourceDir: dir, Edits: d, Generator: "interragate test"})
	for _, ch := range r.Changes {
		if ch.Edit == nil {
			t.Fatalf("%s has no edit, so its lines go untested", ch.Address)
		}
	}

	outs := map[string]*bytes.Buffer{}
	out := func(name string) *bytes.Buffer {
		if outs[name] == nil {
			outs[name] = &bytes.Buffer{}
		}
		return outs[name]
	}
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(text.Render(out("text"), r, text.Options{Width: 100, All: true, Diffs: true}))
	for _, ch := range r.Changes {
		check(text.RenderChange(out("explain"), r, ch, text.Options{Width: 100}))
	}
	check(markdown.Render(out("markdown"), r, markdown.Options{}))
	check(html.Render(out("html"), r))
	check(sarif.Render(out("sarif"), r, sarif.Options{}))
	check(annotations.Render(out("github"), r, ""))
	check(mermaid.Render(out("mermaid"), r, mermaid.Options{}))
	check(json.NewEncoder(out("json")).Encode(r))
	for name, b := range outs {
		for _, s := range secrets {
			if strings.Contains(b.String(), s) {
				t.Errorf("%s output shows %q", name, s)
			}
		}
	}
	// The edits are shown, with what holds a secret masked.
	for _, want := range []string{"db_pw = (sensitive value)", "secret_string = (sensitive value)"} {
		if !strings.Contains(outs["explain"].String(), want) || !strings.Contains(outs["html"].String(), want) {
			t.Errorf("explain or the page lacks %q", want)
		}
	}
}
