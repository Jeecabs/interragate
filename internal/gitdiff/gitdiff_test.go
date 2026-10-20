package gitdiff

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// lines renders a hunk's lines as "-12 text" and "+12 text".
func lines(h *Hunk) []string {
	var out []string
	for _, l := range h.Lines {
		out = append(out, fmt.Sprintf("%s%d %s", l.Op, l.Num, l.Text))
	}
	return out
}

func only(t *testing.T, files []*File) *File {
	t.Helper()
	if len(files) != 1 {
		t.Fatalf("parsed %d files, want 1: %+v", len(files), files)
	}
	return files[0]
}

func TestParseModifiedBlock(t *testing.T) {
	f := only(t, Parse([]byte(`diff --git a/main.tf b/main.tf
index 9110cc7..4b4cc9c 100644
--- a/main.tf
+++ b/main.tf
@@ -129 +149 @@ resource "terraform_data" "deploy" {
-    version = "1.4.0"
+    version = "1.5.0"
`)))
	if f.Path != "main.tf" || f.OldPath != "" || f.Added || f.Deleted || len(f.Hunks) != 1 {
		t.Fatalf("file = %+v", f)
	}
	h := f.Hunks[0]
	if h.OldStart != 129 || h.OldLines != 1 || h.NewStart != 149 || h.NewLines != 1 {
		t.Errorf("hunk = %+v", h)
	}
	want := []string{`-129     version = "1.4.0"`, `+149     version = "1.5.0"`}
	if got := lines(h); !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

func TestParseAddedBlock(t *testing.T) {
	f := only(t, Parse([]byte(`diff --git a/main.tf b/main.tf
index 1111111..2222222 100644
--- a/main.tf
+++ b/main.tf
@@ -5,0 +6,5 @@ resource "random_string" "a" {
+resource "random_string" "new" {
+  length  = 12
+  special = false
+}
+
`)))
	h := f.Hunks[0]
	if h.OldStart != 5 || h.OldLines != 0 || h.NewStart != 6 || h.NewLines != 5 {
		t.Errorf("hunk = %+v", h)
	}
	got := lines(h)
	if len(got) != 5 || got[0] != `+6 resource "random_string" "new" {` || got[4] != "+10 " {
		t.Errorf("lines = %q", got)
	}
}

func TestParseRemovedBlock(t *testing.T) {
	// The block was the end of a file with no newline at its end.
	f := only(t, Parse([]byte(`diff --git a/main.tf b/main.tf
index 1111111..2222222 100644
--- a/main.tf
+++ b/main.tf
@@ -10,5 +9,0 @@ resource "random_string" "b" {
-
-resource "random_string" "c" {
-  length  = 8
-  special = false
-}
\ No newline at end of file
`)))
	h := f.Hunks[0]
	if h.OldStart != 10 || h.OldLines != 5 || h.NewStart != 9 || h.NewLines != 0 {
		t.Errorf("hunk = %+v", h)
	}
	want := []string{"-10 ", `-11 resource "random_string" "c" {`, "-12   length  = 8", "-13   special = false", "-14 }"}
	if got := lines(h); !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

// The counts in each header decide which lines belong to the hunk, so
// changed lines that look like headers are read as lines.
func TestParseMultipleHunksInOneFile(t *testing.T) {
	f := only(t, Parse([]byte("diff --git a/main.tf b/main.tf\n"+
		"index 1111111..2222222 100644\n"+
		"--- a/main.tf\n"+
		"+++ b/main.tf\n"+
		"@@ -3 +3 @@ resource \"local_file\" \"notes\" {\n"+
		"--- old heading\n"+
		"+++ new heading\n"+
		"@@ -9,2 +9 @@ resource \"local_file\" \"notes\" {\n"+
		"-  file_permission = \"0600\"\r\n"+
		"-  directory_permission = \"0700\"\r\n"+
		"+  file_permission = \"0644\"\r\n"+
		"@@ -20,0 +20,2 @@ resource \"null_resource\" \"x\" {\n"+
		"+  # why\n"+
		"+  triggers = {}\n")))
	if len(f.Hunks) != 3 {
		t.Fatalf("hunks = %d, want 3", len(f.Hunks))
	}
	if got := lines(f.Hunks[0]); !slices.Equal(got, []string{"-3 -- old heading", "+3 ++ new heading"}) {
		t.Errorf("first hunk = %q", got)
	}
	// Carriage returns from a file with Windows line endings are dropped.
	if got := lines(f.Hunks[1]); !slices.Equal(got, []string{`-9   file_permission = "0600"`, `-10   directory_permission = "0700"`, `+9   file_permission = "0644"`}) {
		t.Errorf("second hunk = %q", got)
	}
	if h := f.Hunks[2]; h.NewStart != 20 || h.NewLines != 2 || len(h.Lines) != 2 {
		t.Errorf("third hunk = %+v", h)
	}
}

func TestParseRenamedFile(t *testing.T) {
	// Git ends a name that has a space with a tab on the --- and +++ lines.
	spaced := "diff --git a/my dir/main.tf b/my dir/main.tf\n" +
		"index 5555555..6666666 100644\n" +
		"--- a/my dir/main.tf\t\n" +
		"+++ b/my dir/main.tf\t\n" +
		"@@ -1 +1 @@\n" +
		"-# old\n" +
		"+# new\n"
	files := Parse([]byte(`diff --git a/network.tf b/vpc.tf
similarity index 91%
rename from network.tf
rename to vpc.tf
index 1111111..2222222 100644
--- a/network.tf
+++ b/vpc.tf
@@ -2 +2 @@ resource "aws_vpc" "main" {
-  cidr_block = "10.0.0.0/16"
+  cidr_block = "10.1.0.0/16"
diff --git a/old name.tf b/new name.tf
similarity index 100%
rename from old name.tf
rename to new name.tf
diff --git "a/caf\303\251.tf" "b/caf\303\251.tf"
index 3333333..4444444 100644
--- "a/caf\303\251.tf"
+++ "b/caf\303\251.tf"
@@ -1 +1 @@
-# old
+# new
` + spaced))
	if len(files) != 4 {
		t.Fatalf("parsed %d files, want 4", len(files))
	}
	if f := files[0]; f.Path != "vpc.tf" || f.OldPath != "network.tf" || len(f.Hunks) != 1 || f.Hunks[0].Lines[1].Text != `  cidr_block = "10.1.0.0/16"` {
		t.Errorf("renamed with an edit = %+v", f)
	}
	// A rename alone has no lines, but the file still differs.
	if f := files[1]; f.Path != "new name.tf" || f.OldPath != "old name.tf" || len(f.Hunks) != 0 {
		t.Errorf("renamed only = %+v", f)
	}
	if f := files[2]; f.Path != "café.tf" || f.OldPath != "" {
		t.Errorf("quoted name = %+v", f)
	}
	if f := files[3]; f.Path != "my dir/main.tf" || f.OldPath != "" {
		t.Errorf("name with a space = %q (old %q)", f.Path, f.OldPath)
	}
}

func TestParseAddedAndDeletedFiles(t *testing.T) {
	files := Parse([]byte(`diff --git a/new.tf b/new.tf
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/new.tf
@@ -0,0 +1,3 @@
+resource "null_resource" "n" {
+  triggers = {}
+}
diff --git a/gone.tf b/gone.tf
deleted file mode 100644
index 4444444..0000000
--- a/gone.tf
+++ /dev/null
@@ -1,2 +0,0 @@
-resource "null_resource" "g" {
-}
diff --git a/run.sh b/run.sh
old mode 100644
new mode 100755
diff --git a/logo.png b/logo.png
index 5555555..6666666 100644
Binary files a/logo.png and b/logo.png differ
`))
	if len(files) != 2 {
		t.Fatalf("parsed %d files, want 2 (mode changes and binary files have no lines)", len(files))
	}
	if f := files[0]; f.Path != "new.tf" || !f.Added || f.OldPath != "" || f.Hunks[0].NewLines != 3 {
		t.Errorf("added = %+v", f)
	}
	if f := files[1]; f.Path != "gone.tf" || !f.Deleted || f.OldPath != "" || f.Hunks[0].OldLines != 2 {
		t.Errorf("deleted = %+v", f)
	}
}

const before = `resource "random_string" "a" {
  length  = 16
  special = false
}

resource "random_string" "b" {
  length  = 32
}
`

const after = `resource "random_string" "a" {
  length  = 16
  special = true
}

resource "random_pet" "new" {
  length = 2
}

resource "random_string" "b" {
}
`

// What git prints for the change from before to after: the edit to a runs
// into the new block, so git pairs a's old closing brace with the new
// block's.
const diffBeforeAfter = `diff --git a/main.tf b/main.tf
index 1111111..2222222 100644
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
`

func TestRebuild(t *testing.T) {
	f := only(t, Parse([]byte(diffBeforeAfter)))
	if !f.Rebuild([]byte(after)) {
		t.Fatal("the hunks should fit the file")
	}
	if got := strings.Join(f.Old, "\n") + "\n"; got != before {
		t.Errorf("old =\n%s\nwant\n%s", got, before)
	}
	if got := strings.Join(f.New, "\n") + "\n"; got != after {
		t.Errorf("new =\n%s", got)
	}
	// Saved again since git diff ran: the added lines no longer match.
	g := only(t, Parse([]byte(diffBeforeAfter)))
	if g.Rebuild([]byte(strings.Replace(after, "special = true", "special = maybe", 1))) || g.Old != nil {
		t.Error("a file that changed since the diff should not be rebuilt")
	}
	// A deleted file is all removed lines.
	d := only(t, Parse([]byte("diff --git a/gone.tf b/gone.tf\ndeleted file mode 100644\n--- a/gone.tf\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-a\n-b\n")))
	if !d.Rebuild(nil) || !slices.Equal(d.Old, []string{"a", "b"}) || len(d.New) != 0 {
		t.Errorf("deleted file: old %q, new %q", d.Old, d.New)
	}
}

func TestOldLine(t *testing.T) {
	f := only(t, Parse([]byte(diffBeforeAfter)))
	for _, c := range []struct {
		now, then int
		ok        bool
	}{
		{1, 1, true}, {2, 2, true}, {3, 0, false}, {7, 0, false},
		{8, 4, true},  // the new block's closing brace was a's
		{10, 6, true}, // b's first line, after four more lines
		{11, 8, true}, // b's last line, after the removed one
		{12, 9, true}, // past the end
		{0, 0, false}, // not a line
	} {
		then, ok := f.OldLine(c.now)
		if ok != c.ok || ok && then != c.then {
			t.Errorf("OldLine(%d) = %d, %v; want %d, %v", c.now, then, ok, c.then, c.ok)
		}
	}
}

func TestUntracked(t *testing.T) {
	f := Untracked("extra.tf", []byte("resource \"random_uuid\" \"x\" {}\r\n\n"))
	if !f.Added || len(f.Hunks) != 1 || f.Hunks[0].NewStart != 1 || f.Hunks[0].NewLines != 2 {
		t.Fatalf("untracked = %+v", f)
	}
	if got := lines(f.Hunks[0]); !slices.Equal(got, []string{`+1 resource "random_uuid" "x" {}`, "+2 "}) {
		t.Errorf("lines = %q", got)
	}
	if !slices.Equal(f.New, []string{`resource "random_uuid" "x" {}`, ""}) || len(f.Old) != 0 {
		t.Errorf("content: old %q, new %q", f.Old, f.New)
	}
}
