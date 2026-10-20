package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets this test binary stand in for interragate when a shell
// running a completion script calls it.
func TestMain(m *testing.M) {
	if os.Getenv("INTERRAGATE_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// completionDir makes a directory with plans to complete from, and a
// interragate on PATH that is this test binary.
func completionDir(t *testing.T) (dir string, env []string) {
	t.Helper()
	dir = t.TempDir()
	plan, err := os.ReadFile(mechanicsPlan)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"mech.json":      plan,
		"my plan.json":   plan,
		"nochange.json":  []byte(`{"format_version":"1.2","terraform_version":"1.16.6","resource_changes":[]}`),
		"other/.keep":    nil,
		"my dir/sub/.ok": nil,
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "interragate")); err != nil {
		t.Fatal(err)
	}
	env = append(os.Environ(), "INTERRAGATE_TEST_MAIN=1", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+dir)
	return dir, env
}

func writeScript(t *testing.T, shell string) string {
	t.Helper()
	out, err := runCLI(t, "completion", shell)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "interragate."+shell)
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The bash script, run by bash with the words bash would split a command
// line into. Outside an interactive completion, compopt is unavailable, so
// the addresses come back quoted for a bare word, as bash 3 has them.
func TestBashCompletion(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	dir, env := completionDir(t)
	script := writeScript(t, "bash")
	driver := filepath.Join(t.TempDir(), "driver.bash")
	if err := os.WriteFile(driver, []byte(`source "$1"
shift
COMP_WORDS=("$@")
COMP_CWORD=$((${#COMP_WORDS[@]} - 1))
COMP_LINE="${COMP_WORDS[*]}"
COMP_POINT=${#COMP_LINE}
_interragate interragate "${COMP_WORDS[COMP_CWORD]}" "${COMP_WORDS[COMP_CWORD-1]}"
printf '%s\n' "${COMPREPLY[@]}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	workers := `null_resource.worker null_resource.worker\[0\] null_resource.worker\[1\] null_resource.worker\[2\]`
	cases := []struct {
		words []string
		want  string
	}{
		{[]string{"interragate", "--format", "="}, "text markdown json html github sarif mermaid"},
		{[]string{"interragate", "--format", "=", "j"}, "json"},
		{[]string{"interragate", "explain", "mech.json", `null_resource.queue\[\"o`}, `null_resource.queue\[\"orders\"\]`},
		{[]string{"interragate", "explain", "mech.json", `'null_resource.queue["o`}, `null_resource.queue\[\"orders\"\]`},
		{[]string{"interragate", "--format", "=", "json", "explain", "mech.json", "null_resource.w"}, workers},
		{[]string{"interragate", "explain", "--format", "=", "json", "mech.json", "null_resource.w"}, workers},
		{[]string{"interragate", "explain", "-C", "other", "mech.json", "null_resource.w"}, workers},
		{[]string{"interragate", "explain", `my\ plan.json`, "null_resource.w"}, workers},
		{[]string{"interragate", "explain", `"my plan.json"`, "null_resource.w"}, workers},
		{[]string{"interragate", "explain", "~/mech.json", "null_resource.w"}, workers},
		{[]string{"interragate", "explain", "nochange.json", ""}, ""},
		{[]string{"interragate", "explain", "mech.json", "zzz"}, ""},
		// Directories are left to bash, which keeps "my dir" whole.
		{[]string{"interragate", "-C", ""}, ""},
	}
	for _, c := range cases {
		cmd := exec.Command(bash, append([]string{driver, script}, c.words...)...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%q: %v\n%s", c.words, err, out)
			continue
		}
		if got := strings.Join(strings.Fields(string(out)), " "); got != c.want {
			t.Errorf("%q: completions %q, want %q", c.words, got, c.want)
		}
	}
}

func TestZshCompletion(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	script := writeScript(t, "zsh")
	if out, err := exec.Command(zsh, "-n", script).CombinedOutput(); err != nil {
		t.Errorf("zsh -n: %v\n%s", err, out)
	}
	load := exec.Command(zsh, "-f", "-c", `autoload -Uz compinit && compinit -u -d "$1/zcompdump" && source "$2"`, "zsh", t.TempDir(), script)
	if out, err := load.CombinedOutput(); err != nil || len(out) > 0 {
		t.Errorf("loading the script: %v\n%s", err, out)
	}
}

// fish completes a command line on request (complete -C), so the script
// can be checked as fish would use it.
func TestFishCompletion(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is not installed")
	}
	dir, env := completionDir(t)
	script := writeScript(t, "fish")
	if out, err := exec.Command(fish, "--no-config", "-n", script).CombinedOutput(); err != nil {
		t.Errorf("fish -n: %v\n%s", err, out)
	}
	cases := map[string]string{
		"interragate explain -C other mech.json null_resource.w":      "null_resource.worker",
		"interragate explain --format=json mech.json null_resource.w": "null_resource.worker",
		"interragate -C other explain mech.json null_resource.q":      `null_resource.queue["orders"]`,
		"interragate explain 'my plan.json' null_resource.w":          "null_resource.worker",
		"interragate explain nochange.json ":                          "",
	}
	for line, want := range cases {
		cmd := exec.Command(fish, "--no-config", "-c", `source $argv[1]; complete -C $argv[2]`, script, line)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%q: %v\n%s", line, err, out)
			continue
		}
		if want == "" && strings.TrimSpace(string(out)) != "" || !strings.Contains(string(out), want) {
			t.Errorf("%q: completions\n%s\nwant %q among them", line, out, want)
		}
	}
}
