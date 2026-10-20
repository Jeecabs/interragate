package tfexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Candidate is one binary interragate considered, for explaining its choice.
type Candidate struct {
	Name    string // terraform or tofu, or the name --bin gave
	Path    string // where it was found; empty when it was not
	Version string // set when it runs
	Problem string // why it cannot be used, when it cannot
	Advice  string // a command its version manager suggests, when it printed one
}

// Fix says what to do about a binary that does not run.
func (c Candidate) Fix() string { return c.fix("binary") }

// fix is Fix, naming what --bin must point at: "terraform" when only that
// will do.
func (c Candidate) fix(what string) string {
	if c.Advice != "" {
		return "Its version manager suggests: " + c.Advice + ". Or pass --bin with the path to a working " + what + "."
	}
	return "If a version manager (mise, asdf, tfenv) provides it, choose a version for this directory, or pass --bin with the path to a working " + what + "."
}

// Choice is the binary interragate uses in a directory, and what decided it.
type Choice struct {
	// Runner is the binary to use, or nil when there is none interragate can
	// use; then Problem says why and Fix what to do about it.
	Runner       *Runner
	Problem, Fix string
	// From is "--bin" or "INTERRAGATE_TF_BIN" when one of them named the
	// binary.
	From string
	// Why says what ties the directory to one tool, when something does:
	// "it has .tofu files, which terraform ignores".
	Why string
	// Skipped are binaries on PATH that were passed over because they do
	// not run.
	Skipped []Candidate
	// Note is something to tell the user about the binary chosen: that
	// terraform, named explicitly, ignores the directory's .tofu files.
	Note string
	// Probed is every binary Choose ran to check, for Candidates to reuse:
	// a shim that hangs takes seconds to give up on.
	Probed []Candidate
}

// Err is Problem and Fix as an error, or nil when there is a Runner.
func (c *Choice) Err() error {
	if c.Runner != nil {
		return nil
	}
	return errors.New(c.Problem + "\n" + c.Fix)
}

// Find picks the binary for dir as Choose does, and returns an error that
// says what to do when there is none interragate can use.
func Find(ctx context.Context, dir, bin string) (*Runner, error) {
	ch, err := Choose(ctx, dir, bin)
	if err != nil {
		return nil, err
	}
	if ch.Runner == nil {
		return nil, ch.Err()
	}
	return ch.Runner, nil
}

// Choose picks the binary for dir. bin (a name or a path, from --bin) wins,
// then the INTERRAGATE_TF_BIN environment variable. Otherwise what is in the
// directory decides: .tofu files, which only OpenTofu reads; then the
// registry the lock file's providers come from; then the one init installed
// them from. A directory tied to one tool is only planned with that tool,
// because the other would ignore its .tofu files, or rewrite its lock file
// for its own registry when it ran init. When that tool does not run,
// interragate stops and says how to fix it rather than switch. A directory
// tied to neither gets whichever of terraform and tofu runs, terraform
// first.
//
// The error is only for ctx ending; anything else that stops interragate
// from planning is in the Choice.
func Choose(ctx context.Context, dir, bin string) (*Choice, error) {
	ch := &Choice{}
	t := tiesOf(dir)
	from := "--bin"
	if bin == "" {
		bin, from = os.Getenv("INTERRAGATE_TF_BIN"), "INTERRAGATE_TF_BIN"
	}
	if bin != "" {
		ch.From = from
		return ch, ch.explicit(ctx, dir, bin, t)
	}

	if t.files == opentofu && t.lock == terraform {
		ch.Problem = "this directory has .tofu files, which only tofu reads, but its lock file names providers from the Terraform registry"
		ch.Fix = "To plan it with terraform, which ignores the .tofu files, pass --bin terraform. To move it to OpenTofu, run tofu init, which rewrites the lock file for the OpenTofu registry."
		return ch, nil
	}
	owner, why := t.owner()
	ch.Why = why
	names := []string{"terraform", "tofu"}
	if owner != "" {
		names = []string{command(owner)}
	}
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		c := probe(ctx, dir, name, path)
		if ctx.Err() != nil {
			return ch, context.Cause(ctx)
		}
		ch.Probed = append(ch.Probed, c)
		if c.Problem != "" {
			ch.Skipped = append(ch.Skipped, c)
			continue
		}
		ch.Runner = &Runner{Bin: path, Tool: toolFor(path), Dir: dir, Version: c.Version}
		return ch, nil
	}

	switch {
	case owner != "" && len(ch.Skipped) > 0:
		// What the shim printed is usually a version manager explaining
		// what it needs.
		c := ch.Skipped[0]
		ch.Problem = fmt.Sprintf("%s is on PATH but does not run: %s", c.Name, c.Problem)
		ch.Fix = fmt.Sprintf("This directory needs %s, because %s. %s", c.Name, why, c.fix(c.Name))
	case owner != "":
		name := command(owner)
		ch.Problem = fmt.Sprintf("this directory needs %s, because %s, but %s is not on PATH", name, why, name)
		ch.Fix = fmt.Sprintf("Install %s (%s), or pass --bin with the path to %s.", product(owner), installURL(owner), name)
	case len(ch.Skipped) > 0:
		c := ch.Skipped[0]
		ch.Problem = fmt.Sprintf("%s is on PATH but does not run: %s", c.Name, c.Problem)
		ch.Fix = c.Fix()
	default:
		ch.Problem = "neither terraform nor tofu is on PATH"
		ch.Fix = fmt.Sprintf("Install Terraform (%s) or OpenTofu (%s), or pass --bin with the path to one. interragate demo works without either.", installURL(terraform), installURL(opentofu))
	}
	return ch, nil
}

// explicit checks the binary --bin or INTERRAGATE_TF_BIN names. Like one
// found on PATH, it must run. It must also be the tool whose registry the
// lock file names, if the lock file names one: the other's init would
// rewrite the lock file.
func (ch *Choice) explicit(ctx context.Context, dir, bin string, t ties) error {
	path, err := exec.LookPath(bin)
	if err != nil {
		ch.Problem = fmt.Sprintf("%s names %s, which cannot be found", ch.From, bin)
		ch.Fix = fmt.Sprintf("Point %s at a terraform or tofu binary, or unset it to let interragate choose.", ch.From)
		return nil
	}
	name, tool := filepath.Base(path), toolFor(path)
	c := probe(ctx, dir, name, path)
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if c.Problem != "" {
		ch.Problem = fmt.Sprintf("%s names %s, which does not run: %s", ch.From, path, c.Problem)
		ch.Fix = fmt.Sprintf("Point %s at a working binary, or unset it to let interragate choose.", ch.From)
		if c.Advice != "" {
			ch.Fix = "Its version manager suggests: " + c.Advice + ". " + ch.Fix
		}
		return nil
	}
	if t.lock != "" && t.lock != tool {
		ch.Problem = fmt.Sprintf("%s names %s, but this directory's lock file names providers from the %s registry, and %s init would rewrite it for the %s registry", ch.From, name, product(t.lock), name, product(tool))
		ch.Fix = fmt.Sprintf("Use %s here, or run %s init yourself to move the directory to %s, then run interragate again.", command(t.lock), name, product(tool))
		return nil
	}
	ch.Runner = &Runner{Bin: path, Tool: tool, Dir: dir, Version: c.Version}
	if t.files == opentofu && tool == terraform {
		ch.Note = fmt.Sprintf("%s reads only the .tf files here, not the .tofu ones.", name)
	}
	return nil
}

// Candidates looks for terraform and tofu on PATH and checks that each
// runs. Version managers put shims on PATH that fail until a version is
// chosen, so finding a binary is not enough. Binaries in known, such as a
// Choice's Probed, are not checked again. The error is only for ctx ending.
func Candidates(ctx context.Context, dir string, known []Candidate) ([]Candidate, error) {
	var out []Candidate
	for _, name := range []string{"terraform", "tofu"} {
		c := Candidate{Name: name}
		if path, err := exec.LookPath(name); err == nil {
			c = Candidate{}
			for _, k := range known {
				if k.Path == path {
					c = k
				}
			}
			if c.Path == "" {
				c = probe(ctx, dir, name, path)
				if ctx.Err() != nil {
					return out, context.Cause(ctx)
				}
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// versionAdvice finds the command a version manager suggests in its error.
var versionAdvice = regexp.MustCompile(`(?m)^\s*((?:mise use|asdf set|asdf local|tfenv use|tfenv install|tenv tf use|tenv tf install)\b[^\n]*?)\s*$`)

// probeTimeout bounds `version -json`, which can hang in a broken shim.
var probeTimeout = 10 * time.Second

// probe runs `version -json` to learn the version, or what went wrong.
// When ctx ends first, the Candidate is incomplete and the caller should
// stop.
func probe(ctx context.Context, dir, name, path string) Candidate {
	c := Candidate{Name: name, Path: path}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(pctx, path, "version", "-json")
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Skip the update check, which would otherwise call home.
	cmd.Env = append(os.Environ(), "CHECKPOINT_DISABLE=1")
	// A shim is often a script that runs the binary as a child, which would
	// outlive killing the script and hold its output open. Stop all of it,
	// and give up on the output soon after.
	ownGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return c
		}
		if pctx.Err() != nil {
			c.Problem = fmt.Sprintf("%s version did not finish within %s", name, seconds(probeTimeout))
			return c
		}
		out := stderr.String() + "\n" + stdout.String()
		c.Problem = firstLine(out)
		if c.Problem == "" {
			c.Problem = err.Error()
		}
		if m := versionAdvice.FindStringSubmatch(out); m != nil {
			c.Advice = m[1]
		}
		return c
	}
	var v struct {
		Version string `json:"terraform_version"`
	}
	if json.Unmarshal(stdout.Bytes(), &v) != nil || v.Version == "" {
		c.Problem = "it did not report a version"
		return c
	}
	c.Version = v.Version
	return c
}

func seconds(d time.Duration) string {
	if d < 2*time.Second {
		return d.String()
	}
	return fmt.Sprintf("%d seconds", int(d.Seconds()))
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

const (
	terraform = "terraform"
	opentofu  = "opentofu"
)

func toolFor(path string) string {
	if strings.Contains(filepath.Base(path), "tofu") {
		return opentofu
	}
	return terraform
}

// command is what users type for a tool.
func command(tool string) string {
	if tool == opentofu {
		return "tofu"
	}
	return "terraform"
}

func product(tool string) string {
	if tool == opentofu {
		return "OpenTofu"
	}
	return "Terraform"
}

func installURL(tool string) string {
	if tool == opentofu {
		return "https://opentofu.org/docs/intro/install/"
	}
	return "https://developer.hashicorp.com/terraform/install"
}

// ties is what in a directory ties it to one tool. Each field is
// "terraform", "opentofu" or "" when that part says nothing.
type ties struct {
	files string // opentofu when the directory has .tofu files
	lock  string // the tool whose registry the lock file's providers come from
	data  string // the tool whose registry init installed providers from
}

func tiesOf(dir string) ties {
	var t ties
	for _, pattern := range []string{"*.tofu", "*.tofu.json"} {
		if m, _ := filepath.Glob(filepath.Join(dir, pattern)); len(m) > 0 {
			t.files = opentofu
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".terraform.lock.hcl")); err == nil {
		var hosts []string
		for _, m := range lockedProvider.FindAllSubmatch(b, -1) {
			hosts = append(hosts, string(m[1]))
		}
		t.lock = registryTool(hosts)
	}
	if entries, err := os.ReadDir(filepath.Join(DataDir(dir), "providers")); err == nil {
		var hosts []string
		for _, e := range entries {
			hosts = append(hosts, e.Name())
		}
		t.data = registryTool(hosts)
	}
	return t
}

// owner is the tool the directory belongs to, and why, strongest tie first.
// The lock file outranks the providers init installed, which can include
// both registries' after a move from one tool to the other.
func (t ties) owner() (tool, why string) {
	switch {
	case t.files != "":
		return t.files, "it has .tofu files, which terraform ignores"
	case t.lock != "":
		return t.lock, fmt.Sprintf("its lock file names providers from the %s registry, and %s init would rewrite it", product(t.lock), command(other(t.lock)))
	case t.data != "":
		return t.data, fmt.Sprintf("init installed its providers from the %s registry", product(t.data))
	}
	return "", ""
}

func other(tool string) string {
	if tool == opentofu {
		return terraform
	}
	return opentofu
}

var lockedProvider = regexp.MustCompile(`(?m)^\s*provider\s+"([^"/]+)/`)

// registryTool names the tool whose registry hosts come from, when they
// come from exactly one of the two. Providers from other registries work
// with either tool, so they say nothing.
func registryTool(hosts []string) string {
	var tf, tofu bool
	for _, h := range hosts {
		switch strings.ToLower(h) {
		case "registry.terraform.io":
			tf = true
		case "registry.opentofu.org":
			tofu = true
		}
	}
	switch {
	case tf && !tofu:
		return terraform
	case tofu && !tf:
		return opentofu
	}
	return ""
}
