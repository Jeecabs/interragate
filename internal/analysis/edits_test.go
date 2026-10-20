package analysis

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/gitdiff"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
	"github.com/Jeecabs/interragate/internal/scan"
)

// pc is one planned change for an edit test.
type pc struct {
	addr, actions, reason, prev string
}

// editPlan builds a plan with the given changes, whose configuration calls
// local modules from ./modules/NAME.
func editPlan(t *testing.T, modules []string, changes ...pc) *plan.Plan {
	t.Helper()
	var rcs []map[string]any
	for _, c := range changes {
		addr, err := plan.ParseAddress(c.addr)
		if err != nil {
			t.Fatal(err)
		}
		before, after := any(map[string]any{"v": 1}), any(map[string]any{"v": 2})
		switch c.actions {
		case "create":
			before = nil
		case "delete":
			after = nil
		case "no-op":
			after = before
		}
		entry := map[string]any{
			"address": c.addr, "mode": addr.Mode, "type": addr.Type, "name": addr.Name,
			"provider_name": "registry.terraform.io/hashicorp/random",
			"change":        map[string]any{"actions": strings.Split(c.actions, ","), "before": before, "after": after, "after_unknown": map[string]any{}},
		}
		if c.reason != "" {
			entry["action_reason"] = c.reason
		}
		if c.prev != "" {
			entry["previous_address"] = c.prev
		}
		rcs = append(rcs, entry)
	}
	calls := map[string]any{}
	for _, m := range modules {
		calls[m] = map[string]any{"source": "./modules/" + m, "module": map[string]any{}}
	}
	doc, _ := json.Marshal(map[string]any{
		"format_version": "1.2", "terraform_version": "1.16.0", "planned_values": map[string]any{},
		"resource_changes": rcs, "configuration": map[string]any{"root_module": map[string]any{"module_calls": calls}},
	})
	p, err := plan.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// cannedDiff reads what git printed for files that are now as given. With
// content, the files' lines are known too, as they are when interragate runs
// git itself; without, only the hunks are.
func cannedDiff(t *testing.T, files map[string]string, out string, content bool) *gitdiff.Diff {
	t.Helper()
	d := &gitdiff.Diff{Base: "origin/main", Commit: "abc1234", Branch: "feature"}
	for _, f := range gitdiff.Parse([]byte(out)) {
		if content && !f.Rebuild([]byte(files[f.Path])) {
			t.Fatalf("the canned diff does not fit %s", f.Path)
		}
		d.Files = append(d.Files, f)
	}
	return d
}

// describe renders an edit as "kind file: -3 old | +3 new".
func describe(e *model.Edit) string {
	if e == nil {
		return ""
	}
	var parts []string
	for _, l := range e.Lines {
		parts = append(parts, fmt.Sprintf("%s%d %s", l.Op, l.Line, l.Text))
	}
	s := e.Kind + " " + e.File
	if e.Module != "" {
		s += " (" + e.Module + ")"
	}
	return s + ": " + strings.Join(parts, " | ")
}

// The diffs below are what git diff --unified=0 printed for each edit.
func TestEditsFromCannedDiffs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string // the configuration now
		diff    string
		changes []pc
		want    map[string]string // by address; "" for no edit
		// hunksOnly holds what differs when only the hunks are known.
		hunksOnly map[string]string
	}{{
		name: "modified block",
		files: map[string]string{"main.tf": `resource "terraform_data" "deploy" {
  input = {
    version = "1.5.0"
  }
}

resource "terraform_data" "other" {
  input = "same"
}
`},
		diff: `diff --git a/main.tf b/main.tf
index ca07ed2..3619de9 100644
--- a/main.tf
+++ b/main.tf
@@ -3 +3 @@ resource "terraform_data" "deploy" {
-    version = "1.4.0"
+    version = "1.5.0"
`,
		changes: []pc{{addr: "terraform_data.deploy", actions: "update"}, {addr: "terraform_data.other", actions: "update"}},
		want: map[string]string{
			"terraform_data.deploy": `edited main.tf: -3     version = "1.4.0" | +3     version = "1.5.0"`,
			"terraform_data.other":  "",
		},
	}, {
		name: "added block",
		files: map[string]string{"main.tf": `resource "random_string" "a" {
  length = 16
}

import {
  to = random_string.imported
  id = "abc"
}

resource "random_string" "imported" {
  length = 15
}
`},
		diff: `diff --git a/main.tf b/main.tf
index 2f65405..266edfb 100644
--- a/main.tf
+++ b/main.tf
@@ -3,0 +4,9 @@ resource "random_string" "a" {
+
+import {
+  to = random_string.imported
+  id = "abc"
+}
+
+resource "random_string" "imported" {
+  length = 15
+}
`,
		changes: []pc{{addr: "random_string.a", actions: "no-op"}, {addr: "random_string.imported", actions: "create"}},
		want: map[string]string{
			// The import block above it comes with it.
			"random_string.imported": `added main.tf: +5 import { | +6   to = random_string.imported | +7   id = "abc" | +8 } | +9  | +10 resource "random_string" "imported" { | +11   length = 15 | +12 }`,
			"random_string.a":        "",
		},
	}, {
		name: "removed block",
		files: map[string]string{"main.tf": `resource "random_string" "a" {
  length = 16
}

resource "random_string" "c" {
  length = 8
}
`},
		diff: `diff --git a/main.tf b/main.tf
index 869efe2..7f0f6fe 100644
--- a/main.tf
+++ b/main.tf
@@ -5,4 +4,0 @@ resource "random_string" "a" {
-resource "random_string" "b" {
-  length = 32
-}
-
`,
		changes: []pc{
			{addr: "random_string.a", actions: "no-op"},
			{addr: "random_string.b", actions: "delete", reason: "delete_because_no_resource_config"},
			{addr: "random_string.c", actions: "no-op"},
		},
		want: map[string]string{
			"random_string.b": `removed main.tf: -5 resource "random_string" "b" { | -6   length = 32 | -7 }`,
			"random_string.a": "",
			"random_string.c": "",
		},
	}, {
		name: "multiple hunks in one file",
		files: map[string]string{"main.tf": `resource "null_resource" "queue" {
  for_each = toset(["a", "c"])
  triggers = {
    name = each.key
    size = 2
  }
}

resource "null_resource" "other" {
  triggers = { x = 1 }
}
`},
		diff: `diff --git a/main.tf b/main.tf
index a5059d0..5a89fb6 100644
--- a/main.tf
+++ b/main.tf
@@ -2 +2 @@ resource "null_resource" "queue" {
-  for_each = toset(["a", "b"])
+  for_each = toset(["a", "c"])
@@ -5 +5 @@ resource "null_resource" "queue" {
-    size = 1
+    size = 2
@@ -10 +10 @@ resource "null_resource" "other" {
-  triggers = {}
+  triggers = { x = 1 }
`,
		changes: []pc{
			{addr: `null_resource.queue["a"]`, actions: "update"},
			{addr: `null_resource.queue["b"]`, actions: "delete", reason: "delete_because_each_key"},
			{addr: `null_resource.queue["c"]`, actions: "create"},
			{addr: "null_resource.other", actions: "update"},
		},
		want: map[string]string{
			// Every instance shares the block, and so the edit.
			`null_resource.queue["a"]`: `edited main.tf: -2   for_each = toset(["a", "b"]) | +2   for_each = toset(["a", "c"]) | -5     size = 1 | +5     size = 2`,
			`null_resource.queue["b"]`: `edited main.tf: -2   for_each = toset(["a", "b"]) | +2   for_each = toset(["a", "c"]) | -5     size = 1 | +5     size = 2`,
			`null_resource.queue["c"]`: `edited main.tf: -2   for_each = toset(["a", "b"]) | +2   for_each = toset(["a", "c"]) | -5     size = 1 | +5     size = 2`,
			"null_resource.other":      `edited main.tf: -10   triggers = {} | +10   triggers = { x = 1 }`,
		},
	}, {
		name:  "renamed file",
		files: map[string]string{"vpc.tf": "resource \"aws_vpc\" \"main\" {\n  cidr_block = \"10.1.0.0/16\"\n}\n"},
		diff: `diff --git a/network.tf b/vpc.tf
similarity index 67%
rename from network.tf
rename to vpc.tf
index 1111111..2222222 100644
--- a/network.tf
+++ b/vpc.tf
@@ -2 +2 @@ resource "aws_vpc" "main" {
-  cidr_block = "10.0.0.0/16"
+  cidr_block = "10.1.0.0/16"
`,
		changes: []pc{{addr: "aws_vpc.main", actions: "delete,create"}},
		want:    map[string]string{"aws_vpc.main": `edited vpc.tf: -2   cidr_block = "10.0.0.0/16" | +2   cidr_block = "10.1.0.0/16"`},
	}, {
		// The edit to a's last line runs into the new block, so git pairs
		// a's old closing brace with the new block's: one hunk, two blocks.
		name: "a hunk that touches two blocks",
		files: map[string]string{"main.tf": `resource "random_string" "a" {
  length  = 16
  special = true
}

resource "random_pet" "new" {
  length = 2
}

resource "random_string" "b" {
}
`},
		diff: `diff --git a/main.tf b/main.tf
index 9ff3c5c..738b3b0 100644
--- a/main.tf
+++ b/main.tf
@@ -3 +3,5 @@ resource "random_string" "a" {
-  special = false
+  special = true
+}
+
+resource "random_pet" "new" {
+  length = 2
@@ -7 +10,0 @@ resource "random_string" "b" {
-  length  = 32
`,
		changes: []pc{
			{addr: "random_string.a", actions: "delete,create"},
			{addr: "random_pet.new", actions: "create"},
			{addr: "random_string.b", actions: "update"},
		},
		want: map[string]string{
			"random_string.a": `edited main.tf: -3   special = false | +3   special = true`,
			"random_pet.new":  `added main.tf: +6 resource "random_pet" "new" { | +7   length = 2 | +8 }`,
			"random_string.b": `edited main.tf: -7   length  = 32`,
		},
		// From the hunk alone the new block's closing brace is not known
		// to be new.
		hunksOnly: map[string]string{
			"random_pet.new": `added main.tf: +6 resource "random_pet" "new" { | +7   length = 2`,
		},
	}, {
		name: "renamed blocks",
		files: map[string]string{"main.tf": `# Renamed from random_string.api_key without a moved block.
resource "random_string" "api_token" {
  length  = 32
  special = false
}

moved {
  from = random_string.session_secret
  to   = random_string.session_key
}

resource "random_string" "session_key" {
  length = 16
}
`},
		diff: `diff --git a/main.tf b/main.tf
index 66f7e46..57f409f 100644
--- a/main.tf
+++ b/main.tf
@@ -1 +1,2 @@
-resource "random_string" "api_key" {
+# Renamed from random_string.api_key without a moved block.
+resource "random_string" "api_token" {
@@ -6 +7,6 @@ resource "random_string" "api_key" {
-resource "random_string" "session_secret" {
+moved {
+  from = random_string.session_secret
+  to   = random_string.session_key
+}
+
+resource "random_string" "session_key" {
`,
		changes: []pc{
			{addr: "random_string.api_key", actions: "delete", reason: "delete_because_no_resource_config"},
			{addr: "random_string.api_token", actions: "create"},
			{addr: "random_string.session_key", actions: "no-op", prev: "random_string.session_secret"},
		},
		want: map[string]string{
			"random_string.api_token": `edited main.tf: +1 # Renamed from random_string.api_key without a moved block. | -1 resource "random_string" "api_key" { | +2 resource "random_string" "api_token" {`,
			// The destroy of the old name shows the rename too.
			"random_string.api_key":     `removed main.tf: +1 # Renamed from random_string.api_key without a moved block. | -1 resource "random_string" "api_key" { | +2 resource "random_string" "api_token" {`,
			"random_string.session_key": `edited main.tf: +7 moved { | +8   from = random_string.session_secret | +9   to   = random_string.session_key | +10 } | +11  | -6 resource "random_string" "session_secret" { | +12 resource "random_string" "session_key" {`,
		},
		hunksOnly: map[string]string{
			"random_string.api_token":   `edited main.tf: -1 resource "random_string" "api_key" { | +1 # Renamed from random_string.api_key without a moved block. | +2 resource "random_string" "api_token" {`,
			"random_string.api_key":     `removed main.tf: -1 resource "random_string" "api_key" {`,
			"random_string.session_key": `edited main.tf: -6 resource "random_string" "session_secret" { | +7 moved { | +8   from = random_string.session_secret | +9   to   = random_string.session_key | +10 } | +11  | +12 resource "random_string" "session_key" {`,
		},
	}} {
		for _, content := range []bool{true, false} {
			mode := "with content"
			if !content {
				mode = "hunks only"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				writeFiles(t, dir, tc.files)
				r := Analyze(editPlan(t, nil, tc.changes...), Options{SourceDir: dir, Edits: cannedDiff(t, tc.files, tc.diff, content)})
				for addr, want := range tc.want {
					if alt, ok := tc.hunksOnly[addr]; ok && !content {
						want = alt
					}
					if got := describe(change(t, r, addr).Edit); got != want {
						t.Errorf("%s:\n got %s\nwant %s", addr, got, want)
					}
				}
			})
		}
	}
}

// A change inside a module whose own block was not edited follows from the
// module's inputs, so it shows the edit to the call.
func TestEditsInModules(t *testing.T) {
	files := map[string]string{
		"main.tf": `module "cache" {
  source = "./modules/cache"
  size   = 3
}
`,
		"modules/cache/main.tf": `resource "random_id" "node" {
  byte_length = 2
}

resource "null_resource" "warm" {
  triggers = { at = "dawn" }
}

resource "random_id" "idle" {
  byte_length = 2
}
`,
	}
	diff := `diff --git a/main.tf b/main.tf
index 1111111..2222222 100644
--- a/main.tf
+++ b/main.tf
@@ -3 +3 @@ module "cache" {
-  size   = 2
+  size   = 3
@@ -5,4 +4,0 @@ module "cache" {
-
-module "legacy" {
-  source = "./modules/legacy"
-}
diff --git a/modules/cache/main.tf b/modules/cache/main.tf
index 3333333..4444444 100644
--- a/modules/cache/main.tf
+++ b/modules/cache/main.tf
@@ -6 +6 @@ resource "null_resource" "warm" {
-  triggers = { at = "dusk" }
+  triggers = { at = "dawn" }
`
	for _, content := range []bool{true, false} {
		dir := t.TempDir()
		writeFiles(t, dir, files)
		p := editPlan(t, []string{"cache"},
			pc{addr: "module.cache.random_id.node", actions: "delete,create"},
			pc{addr: "module.cache.null_resource.warm", actions: "update"},
			pc{addr: "module.cache.random_id.idle", actions: "no-op"},
			pc{addr: "module.legacy.random_string.token", actions: "delete", reason: "delete_because_no_resource_config"},
		)
		r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, content)})
		for addr, want := range map[string]string{
			"module.cache.random_id.node":       `module main.tf (module.cache): -3   size   = 2 | +3   size   = 3`,
			"module.cache.null_resource.warm":   `edited modules/cache/main.tf: -6   triggers = { at = "dusk" } | +6   triggers = { at = "dawn" }`,
			"module.cache.random_id.idle":       "", // nothing changes, so nothing follows
			"module.legacy.random_string.token": `module main.tf (module.legacy): -6 module "legacy" { | -7   source = "./modules/legacy" | -8 }`,
		} {
			if got := describe(change(t, r, addr).Edit); got != want {
				t.Errorf("content %v, %s:\n got %s\nwant %s", content, addr, got, want)
			}
		}
	}
}

// An insertion that only gives a block a closing brace, because git slid a
// new block against it, is not an edit to it; nor is a block that only
// moved to another file.
func TestEditsIgnoreAlignment(t *testing.T) {
	files := map[string]string{
		"main.tf": "resource \"random_pet\" \"a\" {\n  length = 2\n}\nresource \"random_pet\" \"b\" {\n  length = 2\n}\n",
		"pets.tf": "resource \"random_pet\" \"c\" {\n  length = 1\n}\n",
	}
	diff := `diff --git a/main.tf b/main.tf
index 1111111..2222222 100644
--- a/main.tf
+++ b/main.tf
@@ -2,0 +3,3 @@ resource "random_pet" "a" {
+}
+resource "random_pet" "b" {
+  length = 2
@@ -4,3 +6,0 @@ resource "random_pet" "a" {
-resource "random_pet" "c" {
-  length = 1
-}
diff --git a/pets.tf b/pets.tf
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/pets.tf
@@ -0,0 +1,3 @@
+resource "random_pet" "c" {
+  length = 1
+}
`
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p := editPlan(t, nil, pc{addr: "random_pet.a", actions: "update"}, pc{addr: "random_pet.b", actions: "create"}, pc{addr: "random_pet.c", actions: "update"})
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	for addr, want := range map[string]string{
		"random_pet.a": "",
		"random_pet.b": `added main.tf: +4 resource "random_pet" "b" { | +5   length = 2 | +6 }`,
		"random_pet.c": "",
	} {
		if got := describe(change(t, r, addr).Edit); got != want {
			t.Errorf("%s:\n got %s\nwant %s", addr, got, want)
		}
	}
	if got := strings.Join(r.Git.Files, ","); got != "main.tf,pets.tf" {
		t.Errorf("files = %s", got)
	}
}

// The report is shared, so a sensitive value in an edited line is masked
// like everywhere else in it.
func TestEditRedactsSecrets(t *testing.T) {
	files := map[string]string{"main.tf": "resource \"aws_db_instance\" \"main\" {\n  password = \"hunter2-SECRET-new\"\n}\n"}
	diff := `diff --git a/main.tf b/main.tf
index 1111111..2222222 100644
--- a/main.tf
+++ b/main.tf
@@ -2 +2 @@ resource "aws_db_instance" "main" {
-  password = "hunter2-SECRET-old"
+  password = "hunter2-SECRET-new"
`
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p := planJSON(t, `[{"address":"aws_db_instance.main","mode":"managed","type":"aws_db_instance","name":"main",
	  "provider_name":"registry.terraform.io/hashicorp/aws",
	  "change":{"actions":["update"],"before":{"password":"hunter2-SECRET-old"},"after":{"password":"hunter2-SECRET-new"},
	    "after_unknown":{},"before_sensitive":{"password":true},"after_sensitive":{"password":true}}}]`, "")
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	want := `edited main.tf: -2   password = (sensitive value) | +2   password = (sensitive value)`
	if got := describe(change(t, r, "aws_db_instance.main").Edit); got != want {
		t.Errorf("edit:\n got %s\nwant %s", got, want)
	}
	assertNoSecrets(t, r, "hunter2-SECRET-old", "hunter2-SECRET-new")
}

func TestEditLinesAreCapped(t *testing.T) {
	var body strings.Builder
	body.WriteString("resource \"local_file\" \"big\" {\n")
	var hunk strings.Builder
	for i := 2; i <= 61; i++ {
		fmt.Fprintf(&body, "  # line %d\n", i)
		fmt.Fprintf(&hunk, "+  # line %d\n", i)
	}
	body.WriteString("}\n")
	files := map[string]string{"main.tf": body.String()}
	diff := "diff --git a/main.tf b/main.tf\nnew file mode 100644\n--- /dev/null\n+++ b/main.tf\n@@ -0,0 +1,62 @@\n" +
		"+resource \"local_file\" \"big\" {\n" + hunk.String() + "+}\n"
	dir := t.TempDir()
	writeFiles(t, dir, files)
	r := Analyze(editPlan(t, nil, pc{addr: "local_file.big", actions: "create"}), Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	e := change(t, r, "local_file.big").Edit
	if e == nil || e.Kind != "added" || len(e.Lines) != maxEditLines || e.Omitted != 62-maxEditLines {
		t.Fatalf("edit = %+v", e)
	}
}

func TestEditSentence(t *testing.T) {
	edit := &model.Edit{File: "main.tf", Kind: "edited", Lines: []model.EditLine{{Op: "+", Line: 1, Text: "x"}}}
	r := &model.Report{
		Git: &model.GitInfo{Base: "origin/main"},
		Changes: []*model.Change{
			{Address: "a.one", Action: model.Replace, Edit: edit},
			{Address: "a.two", Action: model.Update, Edit: edit},
			{Address: "a.same", Action: model.NoOp, Edit: edit}, // edited, but nothing changes
			{Address: "b.one", Action: model.Update, Causes: []model.Cause{{Address: "a.one"}}},
			{Address: "b.two", Action: model.Update, Causes: []model.Cause{{Address: "b.one"}}}, // through b.one
			{Address: "c.one", Action: model.Update, Causes: []model.Cause{{Address: "c.two"}}},
			{Address: "c.two", Action: model.Update, Causes: []model.Cause{{Address: "c.one"}}}, // a cycle, no edit
			{Address: "d.one", Action: model.Delete},
		},
	}
	sentence := func() string {
		for _, ch := range r.Changes {
			ch.Origin = ""
		}
		origins(r, nil)
		return editSentence(r)
	}
	if got, want := sentence(), "Your edits since `origin/main` touch 2 resources; 2 more change because of them and 3 other changes are not tied to an edit."; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	r.Git.Base = "HEAD"
	r.Changes = r.Changes[:4]
	if got, want := sentence(), "Your uncommitted edits touch 2 resources; 1 more changes because of them."; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	r.Changes = r.Changes[1:3]
	if got, want := sentence(), "Your uncommitted edits touch 1 resource."; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	r.Changes = nil
	if got := sentence(); got != "" {
		t.Errorf("no edits: %q", got)
	}
}

// End to end: a real repository, a committed configuration and an edit to
// it, read by git and matched with a plan.
func TestEditsFromGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "interragate")
	t.Setenv("GIT_AUTHOR_EMAIL", "interragate@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "interragate")
	t.Setenv("GIT_COMMITTER_EMAIL", "interragate@example.com")
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q")
	git("symbolic-ref", "HEAD", "refs/heads/main")
	writeFiles(t, dir, map[string]string{
		"main.tf": `resource "random_pet" "name" {
  length = 2
}

resource "null_resource" "worker" {
  count = 2
  triggers = {
    name = random_pet.name.id
  }
}

resource "random_string" "gone" {
  length = 4
}

module "cache" {
  source = "./modules/cache"
  size   = 1
}
`,
		"modules/cache/main.tf": "variable \"size\" {}\n\nresource \"random_id\" \"node\" {\n  byte_length = var.size\n}\n",
	})
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	writeFiles(t, dir, map[string]string{
		"main.tf": `resource "random_pet" "name" {
  length = 3
}

resource "null_resource" "worker" {
  count = 2
  triggers = {
    name = random_pet.name.id
  }
}

module "cache" {
  source = "./modules/cache"
  size   = 2
}
`,
		"extra.tf": "resource \"random_uuid\" \"extra\" {}\n",
	})

	p := editPlan(t, []string{"cache"},
		pc{addr: "random_pet.name", actions: "delete,create"},
		pc{addr: "null_resource.worker[0]", actions: "delete,create"},
		pc{addr: "null_resource.worker[1]", actions: "delete,create"},
		pc{addr: "random_string.gone", actions: "delete", reason: "delete_because_no_resource_config"},
		pc{addr: "module.cache.random_id.node", actions: "delete,create"},
		pc{addr: "random_uuid.extra", actions: "create"},
	)
	d := gitdiff.Load(dir, "", p.Configuration)
	if d == nil {
		t.Fatal("no diff from git")
	}
	r := Analyze(p, Options{SourceDir: dir, Edits: d})
	if r.Git == nil || r.Git.Base != "HEAD" || r.Git.Branch != "main" || !r.Git.Dirty || strings.Join(r.Git.Files, ",") != "extra.tf,main.tf" {
		t.Fatalf("git = %+v", r.Git)
	}
	for addr, want := range map[string]string{
		"random_pet.name":             "edited main.tf: -2   length = 2 | +2   length = 3",
		"null_resource.worker[0]":     "",
		"null_resource.worker[1]":     "",
		"random_string.gone":          `removed main.tf: -12 resource "random_string" "gone" { | -13   length = 4 | -14 }`,
		"module.cache.random_id.node": "module main.tf (module.cache): -18   size   = 1 | +14   size   = 2",
		"random_uuid.extra":           `added extra.tf: +1 resource "random_uuid" "extra" {}`,
	} {
		if got := describe(change(t, r, addr).Edit); got != want {
			t.Errorf("%s:\n got %s\nwant %s", addr, got, want)
		}
	}
	// The JSON says the same, for tools and the HTML report.
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"git":{"base":"HEAD"`, `"edit":{"file":"main.tf","kind":"edited","lines":[{"op":"-","line":2,"text":"  length = 2"}`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON lacks %s", want)
		}
	}
}

func TestMaskSecretLiteral(t *testing.T) {
	for in, want := range map[string]string{
		`  password = "hunter2"`:                 `  password = "(sensitive value)"`,
		`  master_password = "s3cr3t-value"`:     `  master_password = "(sensitive value)"`,
		`    "api_key": "abc123",`:               `    "api_key": "(sensitive value)",`,
		`  password = var.db_password`:           `  password = var.db_password`,
		`  token    = "${random_id.t.hex}"`:      `  token    = "${random_id.t.hex}"`,
		`  name     = "orders"`:                  `  name     = "orders"`,
		`  secret_string = jsonencode(local.x)`:  `  secret_string = jsonencode(local.x)`,
		`  AWS_SECRET_ACCESS_KEY = "wJalrXUtnF"`: `  AWS_SECRET_ACCESS_KEY = "(sensitive value)"`,
		// Anywhere on the line, in what a function is given too.
		`  secret_string = jsonencode({ username = "admin", password = "Sup3rS3cretJson" })`: `  secret_string = jsonencode({ username = "admin", password = "(sensitive value)" })`,
		`  env = { DB_PASSWORD = "hunter2", LOG_LEVEL = "debug" }`:                           `  env = { DB_PASSWORD = "(sensitive value)", LOG_LEVEL = "debug" }`,
		`  name = "orders" # password: hunter2`:                                              `  name = "orders" # password: (sensitive value)`,
		// JSON written inside a string, under a secret name or as one.
		`  value = "{\"username\":\"admin\",\"password\":\"Sup3rS3cretNew\"}"`:         `  value = "{\"username\":\"admin\",\"password\":\"(sensitive value)\"}"`,
		`  secret_string = "{\"username\":\"admin\",\"password\":\"Sup3rS3cretNew\"}"`: `  secret_string = "(sensitive value)"`,
		`  password = "a\"b" , other = "visible"`:                                      `  password = "(sensitive value)" , other = "visible"`,
		// Quoted any way, or not at all, as in YAML or a header.
		`  password = 'single-quoted'`:       `  password = '(sensitive value)'`,
		`    token: eyJhbGciOi.eyJzdWIi.Sfl`: `    token: (sensitive value)`,
		`Authorization: Bearer abc123`:       `Authorization: (sensitive value)`,
		`  api_key = "unterminated`:          `  api_key = "(sensitive value)`,
		// Text around an interpolation is written out; a lone one is not.
		`  password = "${var.prefix}hunter2"`: `  password = "(sensitive value)"`,
		// Nothing written out: nothing to hide.
		`  password = null`:                      `  password = null`,
		`  password = ""`:                        `  password = ""`,
		`  password = <<EOT`:                     `  password = <<EOT`,
		`  password = random_password.db.result`: `  password = random_password.db.result`,
	} {
		if got := maskSecretLiteral(in); got != want {
			t.Errorf("maskSecretLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProviderSentence(t *testing.T) {
	r := &model.Report{
		Git: &model.GitInfo{Base: "origin/main", Providers: []model.ProviderChange{{Source: "registry.terraform.io/hashicorp/aws", From: "5.62.0", To: "6.0.0"}}},
		Changes: []*model.Change{
			{Address: "aws_s3_bucket.a", Action: model.Update, Provider: "aws"},
			{Address: "aws_s3_bucket.b", Action: model.Update, Provider: "aws"},
			{Address: "aws_s3_bucket.c", Action: model.Update, Provider: "aws", Origin: "edit"},
			{Address: "random_id.x", Action: model.Update, Provider: "random"},
		},
	}
	s := providerSentence(r)
	want := "The `aws` provider moves from 5.62.0 to 6.0.0 since `origin/main`, which may explain the 2 changes to its resources that are not tied to an edit. That is a new major version, so its upgrade guide is worth a read."
	if s == nil || s.Text != want || s.Severity != model.Low {
		t.Fatalf("got %+v\nwant %q", s, want)
	}
	r.Git.Providers[0].To = "5.70.0"
	r.Git.Base = "HEAD"
	if s := providerSentence(r); s == nil || s.Text != "The `aws` provider moves from 5.62.0 to 5.70.0 in your uncommitted changes, which may explain the 2 changes to its resources that are not tied to an edit." {
		t.Errorf("minor: %+v", s)
	}
}

func TestUntiedDestroyAndOriginPolicies(t *testing.T) {
	files := map[string]string{"main.tf": "resource \"random_pet\" \"edited\" {\n  length = 3\n}\n\nresource \"random_pet\" \"other\" {\n  length = 2\n}\n"}
	diff := "diff --git a/main.tf b/main.tf\n--- a/main.tf\n+++ b/main.tf\n@@ -2 +2 @@ resource \"random_pet\" \"edited\" {\n-  length = 2\n+  length = 3\n"
	dir := t.TempDir()
	writeFiles(t, dir, files)
	cfg := policyConfig(t, "policies:\n  - id: untied-replace\n    title: Replacements nobody asked for\n    severity: high\n    match:\n      action: replace\n      origin: untied\n")
	r := Analyze(editPlan(t, nil,
		pc{addr: "random_pet.edited", actions: "delete,create"},
		pc{addr: "random_pet.other", actions: "delete,create"},
	), Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true), Config: cfg})
	f := only(t, r, "untied-destroy")
	if f.Addresses[0] != "random_pet.other" || !strings.Contains(f.Message, "none of your edits since `origin/main` touch it") {
		t.Errorf("untied-destroy: %s %s", f.Addresses[0], f.Message)
	}
	if p := only(t, r, "untied-replace"); p.Addresses[0] != "random_pet.other" {
		t.Errorf("origin policy flagged %s", p.Addresses[0])
	}

	// Without edits of its own, a plan is not checked.
	r = Analyze(editPlan(t, nil, pc{addr: "random_pet.other", actions: "delete,create"}), Options{SourceDir: dir, Edits: cannedDiff(t, files, "", true), Config: cfg})
	none(t, r, "untied-destroy")
}

func TestEditsReachThroughVariablesAndLocals(t *testing.T) {
	files := map[string]string{
		"main.tf": "variable \"size\" {\n  default = 3\n}\n\nlocals {\n  count = var.size\n  name  = \"x\"\n}\n\nresource \"random_pet\" \"many\" {\n  count  = local.count\n  length = 2\n}\n\nresource \"random_pet\" \"named\" {\n  prefix = local.name\n}\n",
	}
	diff := "diff --git a/main.tf b/main.tf\n--- a/main.tf\n+++ b/main.tf\n@@ -2 +2 @@ variable \"size\" {\n-  default = 2\n+  default = 3\n"
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p := editPlan(t, nil, pc{addr: "random_pet.many[2]", actions: "create"}, pc{addr: "random_pet.named", actions: "update"})
	// The plan's configuration says what each resource reads.
	p.Configuration.RootModule.Resources = []*plan.ConfigResource{
		{Address: "random_pet.many", Mode: "managed", Type: "random_pet", Name: "many", CountExpression: &plan.Expression{References: []string{"local.count"}}},
		{Address: "random_pet.named", Mode: "managed", Type: "random_pet", Name: "named", Expressions: map[string]any{"prefix": map[string]any{"references": []any{"local.name"}}}},
	}
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	e := change(t, r, "random_pet.many[2]").Edit
	if e == nil || e.Kind != "input" || e.Input != "var.size" || describe(e) != "input main.tf: -2   default = 2 | +2   default = 3" {
		t.Fatalf("many: %+v", e)
	}
	if e := change(t, r, "random_pet.named").Edit; e != nil {
		t.Errorf("named reads an unedited local, but got %+v", e)
	}
}

func TestEditsReachThroughProviderTags(t *testing.T) {
	files := map[string]string{
		"main.tf": "provider \"random\" {\n  default_tags {\n    tags = { Team = \"b\" }\n  }\n}\n\nresource \"random_pet\" \"tagged\" {\n  length = 2\n}\n",
	}
	diff := "diff --git a/main.tf b/main.tf\n--- a/main.tf\n+++ b/main.tf\n@@ -3 +3 @@ provider \"random\" {\n-    tags = { Team = \"a\" }\n+    tags = { Team = \"b\" }\n"
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p := synth(t, rc{addr: "random_pet.tagged", actions: "update",
		before: `{"length":2,"tags":{"Team":"a"},"tags_all":{"Team":"a"}}`, after: `{"length":2,"tags":{},"tags_all":{"Team":"b"}}`})
	p.Configuration = &plan.Configuration{RootModule: &plan.ConfigModule{}}
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	e := change(t, r, "random_pet.tagged").Edit
	if e == nil || e.Kind != "provider" || e.Input != "provider.random" {
		t.Fatalf("edit = %+v", e)
	}
}

func TestScannerIssuesOnTheChanges(t *testing.T) {
	files := map[string]string{
		"main.tf": "resource \"random_pet\" \"new\" {\n  length = 2\n}\n\nresource \"random_pet\" \"edited\" {\n  length = 3\n  prefix = \"a\"\n}\n\nresource \"random_pet\" \"old\" {\n  length = 2\n}\n",
	}
	diff := "diff --git a/main.tf b/main.tf\n--- a/main.tf\n+++ b/main.tf\n@@ -6 +6 @@ resource \"random_pet\" \"edited\" {\n-  length = 2\n+  length = 3\n"
	dir := t.TempDir()
	writeFiles(t, dir, files)
	issues := []scan.Issue{
		{Tool: "trivy", ID: "T-1", Severity: "high", Title: "New thing", Message: "Is risky.", File: "main.tf", Start: 1, End: 3, Resource: "random_pet.new"},
		{Tool: "trivy", ID: "T-2", Severity: "medium", Title: "Edited line", Message: "Length is short.", File: "main.tf", Start: 6, End: 6},
		{Tool: "trivy", ID: "T-3", Severity: "medium", Title: "Untouched line", Message: "Prefix is odd.", File: "main.tf", Start: 7, End: 7},
		{Tool: "trivy", ID: "T-4", Severity: "low", Title: "Old resource", Message: "Was always like this.", File: "main.tf", Start: 10, End: 12},
	}
	r := Analyze(editPlan(t, nil,
		pc{addr: "random_pet.new", actions: "create"},
		pc{addr: "random_pet.edited", actions: "update"},
		pc{addr: "random_pet.old", actions: "update"},
	), Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true), Scans: []ScanResult{{Tool: "trivy", Issues: issues}}})
	got := findingsFor(r, "trivy")
	if len(got) != 2 {
		t.Fatalf("trivy findings = %d: %v", len(got), ruleList(r))
	}
	if got[0].Message != "`random_pet.new` is new, and trivy flags it: is risky (T-1)." || got[0].Severity != model.High {
		t.Errorf("new: %q %s", got[0].Message, got[0].Severity)
	}
	if got[1].Message != "trivy flags the lines you edited in `random_pet.edited`: length is short (T-2)." {
		t.Errorf("edited: %q", got[1].Message)
	}
	if len(r.Scans) != 1 || r.Scans[0].Issues != 4 || r.Scans[0].Shown != 2 {
		t.Errorf("scans = %+v", r.Scans)
	}
}

func TestEditsReachThroughVariableFiles(t *testing.T) {
	files := map[string]string{
		"main.tf":          "variable \"size\" {\n  default = 1\n}\n\nresource \"random_pet\" \"many\" {\n  count  = var.size\n  length = 2\n}\n",
		"prod.tfvars":      "size = 3\n",
		"staging.tfvars":   "size = 2\n",
		"unrelated.tfvars": "other = 1\n",
	}
	diff := "diff --git a/prod.tfvars b/prod.tfvars\n--- a/prod.tfvars\n+++ b/prod.tfvars\n@@ -1 +1 @@\n-size = 2\n+size = 3\n" +
		"diff --git a/staging.tfvars b/staging.tfvars\n--- a/staging.tfvars\n+++ b/staging.tfvars\n@@ -1 +1 @@\n-size = 1\n+size = 2\n"
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p := editPlan(t, nil, pc{addr: "random_pet.many[2]", actions: "create"})
	p.Configuration.RootModule.Resources = []*plan.ConfigResource{
		{Address: "random_pet.many", Mode: "managed", Type: "random_pet", Name: "many", CountExpression: &plan.Expression{References: []string{"var.size"}}},
	}
	// The plan was made with prod.tfvars: size is 3.
	p.Variables = map[string]plan.Variable{"size": {Value: json.Number("3")}}
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	e := change(t, r, "random_pet.many[2]").Edit
	if e == nil || e.Kind != "input" || e.Input != "var.size" || e.File != "prod.tfvars" {
		t.Fatalf("edit = %+v", e)
	}
}

// A module switched off by a variable is destroyed because of that
// variable: what the module call's count reads explains every change in it.
func TestEditsReachThroughModuleRepetition(t *testing.T) {
	files := map[string]string{
		"main.tf":               "variable \"enable_cache\" {\n  type = bool\n}\n\nmodule \"cache\" {\n  source = \"./modules/cache\"\n  count  = var.enable_cache ? 1 : 0\n}\n",
		"modules/cache/main.tf": "resource \"random_id\" \"node\" {\n  byte_length = 4\n}\n",
		"terraform.tfvars":      "enable_cache = false\n",
	}
	diff := "diff --git a/terraform.tfvars b/terraform.tfvars\n--- a/terraform.tfvars\n+++ b/terraform.tfvars\n@@ -1 +1 @@\n-enable_cache = true\n+enable_cache = false\n"
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p := editPlan(t, []string{"cache"}, pc{addr: "module.cache[0].random_id.node", actions: "delete", reason: "delete_because_no_module"})
	p.Configuration.RootModule.ModuleCalls["cache"].CountExpression = &plan.Expression{References: []string{"var.enable_cache"}}
	p.Variables = map[string]plan.Variable{"enable_cache": {Value: false}}
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	e := change(t, r, "module.cache[0].random_id.node").Edit
	if e == nil || e.Kind != "input" || e.Input != "var.enable_cache" || describe(e) != "input terraform.tfvars: -1 enable_cache = true | +1 enable_cache = false" {
		t.Fatalf("edit = %s", describe(e))
	}
	none(t, r, "untied-destroy")
}

// A variable file can live in a directory of its own (-var-file), and a
// heredoc can end it, its closing marker on the last line.
func TestEditsReachThroughVariableFilesAnywhere(t *testing.T) {
	files := map[string]string{
		"main.tf":          "variable \"size\" {}\n\nvariable \"note\" {}\n\nresource \"random_pet\" \"sized\" {\n  length = var.size\n}\n\nresource \"random_pet\" \"noted\" {\n  prefix = var.note\n}\n",
		"envs/prod.tfvars": "size = 2\nnote = <<EOT\nworld\nEOT\n",
	}
	diff := "diff --git a/envs/prod.tfvars b/envs/prod.tfvars\n--- a/envs/prod.tfvars\n+++ b/envs/prod.tfvars\n" +
		"@@ -1 +1 @@\n-size = 1\n+size = 2\n@@ -3 +3 @@ note = <<EOT\n-hello\n+world\n"
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p := editPlan(t, nil, pc{addr: "random_pet.sized", actions: "delete,create"}, pc{addr: "random_pet.noted", actions: "delete,create"})
	p.Configuration.RootModule.Resources = []*plan.ConfigResource{
		{Address: "random_pet.sized", Mode: "managed", Type: "random_pet", Name: "sized", Expressions: map[string]any{"length": map[string]any{"references": []any{"var.size"}}}},
		{Address: "random_pet.noted", Mode: "managed", Type: "random_pet", Name: "noted", Expressions: map[string]any{"prefix": map[string]any{"references": []any{"var.note"}}}},
	}
	p.Variables = map[string]plan.Variable{"size": {Value: json.Number("2")}, "note": {Value: "world\n"}}
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	for addr, want := range map[string]string{
		"random_pet.sized": "input envs/prod.tfvars: -1 size = 1 | +1 size = 2",
		"random_pet.noted": "input envs/prod.tfvars: -3 hello | +3 world",
	} {
		if got := describe(change(t, r, addr).Edit); got != want {
			t.Errorf("%s:\n got %s\nwant %s", addr, got, want)
		}
	}
	none(t, r, "untied-destroy")
}

// A variable's default only explains a change when the plan used it: a
// variable file that did not change, or -var, may set the value instead.
func TestEditsCountDefaultsOnlyWhenUsed(t *testing.T) {
	files := map[string]string{
		"main.tf": "variable \"size\" {\n  default = 2\n}\n\nvariable \"name\" {\n  default = \"b\"\n}\n\n" +
			"resource \"random_pet\" \"sized\" {\n  length = var.size\n}\n\nresource \"random_pet\" \"named\" {\n  prefix = var.name\n}\n",
	}
	diff := "diff --git a/main.tf b/main.tf\n--- a/main.tf\n+++ b/main.tf\n" +
		"@@ -2 +2 @@ variable \"size\" {\n-  default = 1\n+  default = 2\n@@ -6 +6 @@ variable \"name\" {\n-  default = \"a\"\n+  default = \"b\"\n"
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p := editPlan(t, nil, pc{addr: "random_pet.sized", actions: "delete,create"}, pc{addr: "random_pet.named", actions: "delete,create"})
	p.Configuration.RootModule.Resources = []*plan.ConfigResource{
		{Address: "random_pet.sized", Mode: "managed", Type: "random_pet", Name: "sized", Expressions: map[string]any{"length": map[string]any{"references": []any{"var.size"}}}},
		{Address: "random_pet.named", Mode: "managed", Type: "random_pet", Name: "named", Expressions: map[string]any{"prefix": map[string]any{"references": []any{"var.name"}}}},
	}
	p.Configuration.RootModule.Variables = map[string]*plan.ConfigVariable{"size": {Default: json.Number("2")}, "name": {Default: "b"}}
	// The plan took size from elsewhere, and name from its default.
	p.Variables = map[string]plan.Variable{"size": {Value: json.Number("3")}, "name": {Value: "b"}}
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	if e := change(t, r, "random_pet.sized").Edit; e != nil {
		t.Errorf("sized is credited to a default the plan did not use: %s", describe(e))
	}
	if got, want := describe(change(t, r, "random_pet.named").Edit), `input main.tf: -6   default = "a" | +6   default = "b"`; got != want {
		t.Errorf("named:\n got %s\nwant %s", got, want)
	}
	if f := only(t, r, "untied-destroy"); f.Addresses[0] != "random_pet.sized" {
		t.Errorf("untied-destroy flags %s", f.Addresses[0])
	}
}

// A change to tags_all alone comes from the provider's default_tags, even
// in a resource that also reads an edited variable: each instance that
// only changes tags credits the same edit.
func TestTagOnlyChangesComeFromTheProvider(t *testing.T) {
	files := map[string]string{
		"main.tf": "variable \"azs\" {\n  default = [\"a\", \"b\"]\n}\n\nprovider \"random\" {\n  default_tags {\n    tags = { Team = \"b\" }\n  }\n}\n\n" +
			"resource \"random_pet\" \"tagged\" {\n  count  = length(var.azs)\n  length = 2\n}\n",
	}
	diff := "diff --git a/main.tf b/main.tf\n--- a/main.tf\n+++ b/main.tf\n" +
		"@@ -2 +2 @@ variable \"azs\" {\n-  default = [\"a\"]\n+  default = [\"a\", \"b\"]\n@@ -7 +7 @@ provider \"random\" {\n-    tags = { Team = \"a\" }\n+    tags = { Team = \"b\" }\n"
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p := synth(t,
		rc{addr: "random_pet.tagged[0]", actions: "update", before: `{"length":2,"tags":{},"tags_all":{"Team":"a"}}`, after: `{"length":2,"tags":{},"tags_all":{"Team":"b"}}`},
		rc{addr: "random_pet.tagged[1]", actions: "create", after: `{"length":2,"tags":{},"tags_all":{"Team":"b"}}`})
	p.Configuration = &plan.Configuration{RootModule: &plan.ConfigModule{Resources: []*plan.ConfigResource{
		{Address: "random_pet.tagged", Mode: "managed", Type: "random_pet", Name: "tagged", CountExpression: &plan.Expression{References: []string{"var.azs"}}},
	}}}
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	if e := change(t, r, "random_pet.tagged[0]").Edit; e == nil || e.Kind != "provider" || e.Input != "provider.random" {
		t.Errorf("tags only: %s", describe(e))
	}
	if e := change(t, r, "random_pet.tagged[1]").Edit; e == nil || e.Kind != "input" || e.Input != "var.azs" {
		t.Errorf("new instance: %s", describe(e))
	}
}

// A data source read during the apply because something it depends on
// changes follows from that change.
func TestDataReadsFollowWhatTheyWaitFor(t *testing.T) {
	files := map[string]string{
		"main.tf": "resource \"random_pet\" \"app\" {\n  length = 3\n}\n\ndata \"local_file\" \"p\" {\n  depends_on = [random_pet.app]\n  filename   = \"x\"\n}\n",
	}
	diff := "diff --git a/main.tf b/main.tf\n--- a/main.tf\n+++ b/main.tf\n@@ -2 +2 @@ resource \"random_pet\" \"app\" {\n-  length = 2\n+  length = 3\n"
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p := synthWith(t, `{"root_module":{"resources":[
		{"address":"random_pet.app","mode":"managed","type":"random_pet","name":"app","expressions":{"length":{"constant_value":3}}},
		{"address":"data.local_file.p","mode":"data","type":"local_file","name":"p","expressions":{"filename":{"constant_value":"x"}},"depends_on":["random_pet.app"]}]}}`,
		rc{addr: "random_pet.app", actions: "update", before: `{"length":2}`, after: `{"length":3}`},
		rc{addr: "data.local_file.p", actions: "read", reason: "read_because_dependency_pending", after: `{"filename":"x"}`})
	r := Analyze(p, Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	if o := change(t, r, "data.local_file.p").Origin; o != "follows" {
		t.Errorf("read origin = %q", o)
	}
	if got, want := editSentence(r), "Your edits since `origin/main` touch 1 resource; 1 more changes because of them."; got != want {
		t.Errorf("sentence:\n got %s\nwant %s", got, want)
	}
}

// Blocks in .tf.json have an end, so the edits inside them are theirs.
func TestEditsInJSONConfiguration(t *testing.T) {
	files := map[string]string{
		"main.tf.json": "{\n  \"resource\": {\n    \"random_pet\": {\n      \"x\": {\n        \"length\": 3\n      },\n      \"y\": {\n        \"length\": 2\n      }\n    }\n  }\n}\n",
	}
	diff := "diff --git a/main.tf.json b/main.tf.json\n--- a/main.tf.json\n+++ b/main.tf.json\n@@ -5 +5 @@\n-        \"length\": 2\n+        \"length\": 3\n"
	dir := t.TempDir()
	writeFiles(t, dir, files)
	r := Analyze(editPlan(t, nil, pc{addr: "random_pet.x", actions: "delete,create"}, pc{addr: "random_pet.y", actions: "no-op"}),
		Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	if got, want := describe(change(t, r, "random_pet.x").Edit), `edited main.tf.json: -5         "length": 2 | +5         "length": 3`; got != want {
		t.Errorf("x:\n got %s\nwant %s", got, want)
	}
	if e := change(t, r, "random_pet.y").Edit; e != nil {
		t.Errorf("y was not edited: %s", describe(e))
	}
	none(t, r, "untied-destroy")
}

// untied-destroy and origin policies go by the same test: configuration
// files changed and interragate read them, in a plan that is not a destroy
// plan.
func TestUntiedChecksAgree(t *testing.T) {
	cfg := policyConfig(t, "policies:\n  - id: untied-changes\n    title: Changes nobody asked for\n    severity: high\n    match:\n      action: [replace, delete]\n      origin: untied\n")
	files := map[string]string{"main.tf": "resource \"random_pet\" \"other\" {\n  length = 2\n}\n", "staging.tfvars": "size = 2\n"}
	dir := t.TempDir()
	writeFiles(t, dir, files)
	replace := editPlan(t, nil, pc{addr: "random_pet.other", actions: "delete,create"})

	// A branch whose edits explain nothing in this plan is checked.
	staging := "diff --git a/staging.tfvars b/staging.tfvars\n--- a/staging.tfvars\n+++ b/staging.tfvars\n@@ -1 +1 @@\n-size = 1\n+size = 2\n"
	r := Analyze(replace, Options{SourceDir: dir, Edits: cannedDiff(t, files, staging, true), Config: cfg})
	only(t, r, "untied-destroy")
	only(t, r, "untied-changes")

	// Nothing edited, as on main, says nothing about where changes come from.
	r = Analyze(replace, Options{SourceDir: dir, Edits: cannedDiff(t, files, "", true), Config: cfg})
	none(t, r, "untied-destroy")
	none(t, r, "untied-changes")

	// Nor does a branch whose files interragate did not read.
	r = Analyze(replace, Options{Edits: cannedDiff(t, files, staging, true), Config: cfg})
	none(t, r, "untied-destroy")
	none(t, r, "untied-changes")

	// In a destroy plan, -destroy explains every change.
	edit := "diff --git a/main.tf b/main.tf\n--- a/main.tf\n+++ b/main.tf\n@@ -2 +2 @@ resource \"random_pet\" \"other\" {\n-  length = 1\n+  length = 2\n"
	r = Analyze(editPlan(t, nil, pc{addr: "random_pet.other", actions: "delete"}, pc{addr: "random_pet.more", actions: "delete"}),
		Options{SourceDir: dir, Edits: cannedDiff(t, files, edit, true), Config: cfg})
	if !r.Plan.Destroy {
		t.Fatal("not read as a destroy plan")
	}
	none(t, r, "untied-destroy")
	none(t, r, "untied-changes")
	if s := editSentence(r); s != "" {
		t.Errorf("edit sentence in a destroy plan: %s", s)
	}
}

// A plan can destroy thousands of blocks removed from one file; each is
// found once, by its own first line.
func TestManyRemovedBlocks(t *testing.T) {
	const n = 2000
	var old, removed strings.Builder
	old.WriteString("resource \"random_pet\" \"keep\" {\n  length = 1\n}\n")
	changes := []pc{{addr: "random_pet.keep", actions: "no-op"}}
	for i := range n {
		block := fmt.Sprintf("\nresource \"random_pet\" \"r%d\" {\n  length = 2\n}\n", i)
		old.WriteString(block)
		for _, l := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
			removed.WriteString("-" + l + "\n")
		}
		changes = append(changes, pc{addr: fmt.Sprintf("random_pet.r%d", i), actions: "delete", reason: "delete_because_no_resource_config"})
	}
	files := map[string]string{"main.tf": "resource \"random_pet\" \"keep\" {\n  length = 1\n}\n"}
	diff := fmt.Sprintf("diff --git a/main.tf b/main.tf\n--- a/main.tf\n+++ b/main.tf\n@@ -4,%d +3,0 @@ resource \"random_pet\" \"keep\" {\n", 4*n) + removed.String()
	dir := t.TempDir()
	writeFiles(t, dir, files)
	r := Analyze(editPlan(t, nil, changes...), Options{SourceDir: dir, Edits: cannedDiff(t, files, diff, true)})
	for _, i := range []int{0, 1, n / 2, n - 1} {
		first := 5 + 4*i
		want := fmt.Sprintf(`removed main.tf: -%d resource "random_pet" "r%d" { | -%d   length = 2 | -%d }`, first, i, first+1, first+2)
		if got := describe(change(t, r, fmt.Sprintf("random_pet.r%d", i)).Edit); got != want {
			t.Errorf("r%d:\n got %s\nwant %s", i, got, want)
		}
	}
}
