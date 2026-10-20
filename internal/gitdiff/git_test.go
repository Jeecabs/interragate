package gitdiff

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/plan"
)

// repo makes a git repository in a temporary directory and commits files
// to main in it. It skips the test when git is not installed.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// The user's settings, such as a diff algorithm or signed commits,
	// must not change what the test sees.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, v := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(v, "interragate")
	}
	for _, v := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(v, "interragate@example.com")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	write(t, dir, files)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir string, files map[string]string) {
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

func paths(d *Diff) []string {
	var out []string
	for _, f := range d.Files {
		out = append(out, f.Path)
	}
	return out
}

func file(d *Diff, path string) *File {
	for _, f := range d.Files {
		if f.Path == path {
			return f
		}
	}
	return nil
}

func TestLoad(t *testing.T) {
	dir := repo(t, map[string]string{
		"infra/main.tf":              "resource \"random_pet\" \"a\" {\n  length = 2\n}\n",
		"infra/modules/x/main.tf":    "resource \"random_id\" \"x\" {\n  byte_length = 2\n}\n",
		"modules/shared/main.tf":     "resource \"random_id\" \"s\" {\n  byte_length = 2\n}\n",
		"modules/unused/main.tf":     "resource \"random_id\" \"u\" {\n  byte_length = 2\n}\n",
		"README.md":                  "# infra\n",
		"infra/[weird] name/main.tf": "resource \"random_id\" \"w\" {}\n",
	})
	infra := filepath.Join(dir, "infra")
	// The configuration calls one module from outside its directory.
	cfg := &plan.Configuration{RootModule: &plan.ConfigModule{ModuleCalls: map[string]*plan.ModuleCall{
		"shared": {Source: "../modules/shared"},
		"x":      {Source: "./modules/x"},
	}}}

	if d := Load(infra, "", cfg); d == nil || d.Base != "HEAD" || d.Branch != "main" || d.Dirty || len(d.Files) != 0 {
		t.Fatalf("nothing edited: %+v", d)
	}

	write(t, dir, map[string]string{
		"infra/main.tf":                      "resource \"random_pet\" \"a\" {\n  length = 3\n}\n",
		"infra/extra.tf":                     "resource \"random_uuid\" \"e\" {}\n",
		"infra/.terraform/modules/m/main.tf": "resource \"random_id\" \"cached\" {}\n",
		"infra/modules/x/main.tf":            "resource \"random_id\" \"x\" {\n  byte_length = 4\n}\n",
		"modules/shared/main.tf":             "resource \"random_id\" \"s\" {\n  byte_length = 4\n}\n",
		"modules/unused/main.tf":             "resource \"random_id\" \"u\" {\n  byte_length = 4\n}\n",
		"README.md":                          "# infrastructure\n",
		"infra/[weird] name/main.tf":         "resource \"random_id\" \"w\" {\n}\n",
		"infra/notes.txt":                    "not configuration\n",
	})
	d := Load(infra, "", cfg)
	if d == nil {
		t.Fatal("no diff")
	}
	// Paths are relative to the Terraform directory; caches, other files and
	// modules nothing calls are left out.
	want := []string{"../modules/shared/main.tf", "[weird] name/main.tf", "extra.tf", "main.tf", "modules/x/main.tf"}
	if d.Base != "HEAD" || !d.Dirty || !slices.Equal(paths(d), want) {
		t.Fatalf("uncommitted edits: base %q, dirty %v, files %q, want %q", d.Base, d.Dirty, paths(d), want)
	}
	if f := file(d, "main.tf"); len(f.Hunks) != 1 || !slices.Equal(f.Old, []string{`resource "random_pet" "a" {`, "  length = 2", "}"}) || f.New[1] != "  length = 3" {
		t.Errorf("main.tf = %+v", f)
	}
	if f := file(d, "extra.tf"); !f.Added || len(f.Hunks) != 1 || len(f.Old) != 0 {
		t.Errorf("an untracked file is wholly added: %+v", f)
	}

	// On a branch, the base is where it left main, so its commits count too.
	main := git(t, dir, "rev-parse", "--short", "HEAD")
	git(t, dir, "checkout", "-q", "-b", "feature")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "edit")
	d = Load(infra, "", cfg)
	if d == nil || d.Base != "main" || d.Commit != main || d.Branch != "feature" || d.Dirty || !slices.Contains(paths(d), "main.tf") || !slices.Contains(paths(d), "extra.tf") {
		t.Fatalf("committed on a branch: %+v (files %q), want base main at %s", d, paths(d), main)
	}
	write(t, dir, map[string]string{"infra/main.tf": "resource \"random_pet\" \"a\" {\n  length = 4\n}\n"})
	if d := Load(infra, "", cfg); d == nil || !d.Dirty {
		t.Errorf("an uncommitted edit on top of the branch: %+v", d)
	}

	// --base names the comparison; none turns it off.
	if d := Load(infra, "HEAD", cfg); d == nil || d.Base != "HEAD" || !slices.Equal(paths(d), []string{"main.tf"}) {
		t.Errorf("--base HEAD: %+v (files %q)", d, paths(d))
	}
	// Work that lands on main after the branch left it is not this
	// branch's edit.
	git(t, dir, "checkout", "-q", "--", "infra/main.tf")
	git(t, dir, "checkout", "-q", "main")
	write(t, dir, map[string]string{"infra/other.tf": "resource \"random_pet\" \"o\" {}\n"})
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "elsewhere")
	git(t, dir, "checkout", "-q", "feature")
	if d := Load(infra, "main", cfg); d == nil || d.Commit != main || slices.Contains(paths(d), "other.tf") {
		t.Errorf("--base main after main moved on: %+v (files %q), want the commit the branch left at %s", d, paths(d), main)
	}
	for _, base := range []string{"none", "no-such-branch", "--output=x"} {
		if d := Load(infra, base, cfg); d != nil {
			t.Errorf("--base %s: %+v, want nothing", base, d)
		}
	}
	if d := Load(t.TempDir(), "", nil); d != nil {
		t.Errorf("outside a repository: %+v", d)
	}
}

// Without a default branch to start from there is nothing to compare with.
func TestLoadWithoutDefaultBranch(t *testing.T) {
	dir := repo(t, map[string]string{"main.tf": "resource \"random_pet\" \"a\" {}\n"})
	git(t, dir, "branch", "-q", "-m", "main", "trunk")
	write(t, dir, map[string]string{"main.tf": "resource \"random_pet\" \"b\" {}\n"})
	if d := Load(dir, "", nil); d != nil {
		t.Errorf("no main or master: %+v", d)
	}
	if d := Load(dir, "trunk", nil); d == nil || d.Base != "trunk" || len(d.Files) != 1 {
		t.Errorf("--base trunk: %+v", d)
	}
}

func TestLoadProviders(t *testing.T) {
	lock := func(aws, random string) string {
		s := "provider \"registry.terraform.io/hashicorp/aws\" {\n  version = \"" + aws + "\"\n}\n"
		if random != "" {
			s += "provider \"registry.terraform.io/hashicorp/random\" {\n  version = \"" + random + "\"\n}\n"
		}
		return s
	}
	dir := repo(t, map[string]string{"main.tf": "resource \"terraform_data\" \"x\" {}\n", ".terraform.lock.hcl": lock("5.62.0", "3.6.0")})
	write(t, dir, map[string]string{".terraform.lock.hcl": lock("6.0.0", "")})
	d := Load(dir, "", nil)
	if d == nil {
		t.Fatal("no diff")
	}
	want := []Provider{{Source: "registry.terraform.io/hashicorp/aws", From: "5.62.0", To: "6.0.0"}, {Source: "registry.terraform.io/hashicorp/random", From: "3.6.0"}}
	if !slices.Equal(d.Providers, want) {
		t.Errorf("providers = %+v", d.Providers)
	}

	// A lock file the base never committed compares with nothing.
	dir = repo(t, map[string]string{"main.tf": "resource \"terraform_data\" \"x\" {}\n"})
	write(t, dir, map[string]string{".terraform.lock.hcl": lock("6.0.0", "")})
	if d := Load(dir, "", nil); d == nil || len(d.Providers) != 0 {
		t.Errorf("uncommitted lock file: %+v", d)
	}
}
