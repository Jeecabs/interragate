package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Jeecabs/interragate/internal/gitdiff"
	"github.com/Jeecabs/interragate/internal/owners"
	"github.com/Jeecabs/interragate/internal/scan"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/source"
	"github.com/Jeecabs/interragate/internal/tfexec"
)

// check is one line of interragate doctor's report.
type check struct {
	level int // ok, note or problem
	text  string
	fix   string // what to do about it, when there is something to do
}

const (
	ok = iota
	note
	problem
)

// doctorCmd checks, without changing anything, that interragate can plan in
// a directory: a working terraform or tofu, configuration, init, the lock
// file, settings and a browser. Each line that is not fine says how to fix
// it. It exits 1 when something would stop interragate from planning.
func doctorCmd(ctx context.Context, o *options, stdout io.Writer) error {
	if len(o.args) > 0 {
		return fmt.Errorf("interragate doctor takes no arguments; to check another directory use interragate doctor -C %s", o.args[0])
	}
	dir := o.dir
	if dir == "" {
		dir = "."
	}
	color := !o.noColor && os.Getenv("NO_COLOR") == "" && isTerminal(stdout)
	var checks []check
	add := func(level int, fix, format string, args ...any) {
		checks = append(checks, check{level, fmt.Sprintf(format, args...), fix})
	}

	// The binary: the one interragate would plan with, chosen as the planner
	// chooses it. Other binaries that do not run are worth knowing about,
	// but only stop interragate when it needs them.
	ch, err := tfexec.Choose(ctx, dir, o.bin)
	if err != nil {
		return err
	}
	if ch.From != "" {
		if r := ch.Runner; r != nil {
			add(ok, "", "%s chooses %s %s (%s).", ch.From, r.Name(), r.Version, home(r.Bin))
		}
	} else {
		cands, err := tfexec.Candidates(ctx, dir, ch.Probed)
		if err != nil {
			return err
		}
		working := 0
		for _, c := range cands {
			switch {
			case c.Path == "":
			case c.Problem == "":
				working++
				add(ok, "", "%s %s runs (%s).", c.Name, c.Version, home(c.Path))
			case ch.Runner == nil && len(ch.Skipped) > 0 && ch.Skipped[0].Path == c.Path:
				add(problem, ch.Fix, "%s", sentence(ch.Problem))
			default:
				add(note, c.Fix(), "%s is on PATH but does not run: %s", c.Name, c.Problem)
			}
		}
		if r := ch.Runner; r != nil {
			switch {
			case ch.Why != "":
				add(ok, "", "interragate will use %s here, because %s.", r.Name(), ch.Why)
			case len(ch.Skipped) > 0:
				add(ok, "", "interragate will use %s here, because %s does not run.", r.Name(), ch.Skipped[0].Name)
			case working > 1:
				add(ok, "", "interragate will use %s here; nothing in the directory asks for OpenTofu.", r.Name())
			}
		}
	}
	switch {
	case ch.Runner == nil && (ch.From != "" || len(ch.Skipped) == 0):
		// A problem with no binary on PATH to put it next to.
		add(problem, ch.Fix, "%s", sentence(ch.Problem))
	case ch.Note != "":
		add(note, "", "%s", sentence(ch.Note))
	}

	// The directory.
	abs, _ := filepath.Abs(dir)
	files := configFiles(dir)
	if len(files) == 0 {
		fix := "Run interragate in your Terraform directory, or pass -C DIR. interragate demo shows a sample plan."
		if found := configDirs(dir, 3, 3); len(found) > 0 {
			fix = "These directories have some: " + strings.Join(found, ", ") + ". Run interragate -C " + found[0] + "."
		}
		add(problem, fix, "%s has no Terraform configuration.", home(abs))
	} else {
		add(ok, "", "%s has Terraform configuration (%s).", home(abs), plural(len(files), "file"))
		if !tfexec.Initialised(dir) {
			add(note, "interragate runs init when a plan needs it; pass --no-init to run it yourself instead.", "It has not been initialised yet.")
		} else {
			if state := backend(dir); state != "" {
				add(ok, "", "%s", state)
			}
		}
		if n := lockedProviders(dir); n > 0 {
			add(ok, "", "The lock file pins %s.", plural(n, "provider"))
		} else if tfexec.Initialised(dir) {
			add(note, "Commit .terraform.lock.hcl so everyone plans with the same provider versions.", "There is no lock file.")
		}
	}

	// Settings.
	path := o.config
	if path == "" {
		path, _ = config.Find(dir)
	}
	if path == "" {
		add(ok, "", "No .interragate.yaml, so every rule runs with its default settings.")
	} else if _, err := readConfig(path); err != nil {
		add(problem, "Fix the file, or pass --config with another one.", "%s cannot be used: %v", home(path), err)
	} else {
		add(ok, "", "Settings come from %s.", home(path))
	}

	// The browser, for --open.
	if opener := browserOpener(); opener == "" || runtime.GOOS == "windows" {
		if runtime.GOOS != "windows" {
			add(note, "Open the HTML file interragate writes yourself, or install xdg-utils.", "Nothing here opens a browser, so --open can only write the report.")
		}
	} else if _, err := exec.LookPath(opener); err != nil {
		add(note, "Open the HTML file interragate writes yourself, or install xdg-utils.", "%s is not installed, so --open can only write the report.", opener)
	} else {
		add(ok, "", "Reports open in your browser with %s.", opener)
	}

	// Git, for tying changes to your edits, and who owns what.
	if d := gitdiff.Load(dir, o.base, nil); d != nil {
		edits := "your edits since " + d.Base
		if d.Base == "HEAD" {
			edits = "your uncommitted edits"
		}
		add(ok, "", "In a git repository: interragate ties each change to %s.", edits)
		if f := owners.Find(d.Top); f != nil {
			add(ok, "", "%s names who owns what, so interragate says whose code a plan reaches.", f.Path)
		}
	} else if o.base != "none" {
		add(note, "", "Not in a git repository (or no commit to compare with), so changes are not tied to edits.")
	}

	// Scanners, for --scan: only worth a line, as none is needed.
	var have, missing []string
	for _, tool := range scan.Tools {
		if _, err := exec.LookPath(tool); err == nil {
			have = append(have, tool)
		} else {
			missing = append(missing, tool)
		}
	}
	switch {
	case len(have) == 0:
		add(note, "", "No scanner for --scan on your PATH (%s); interragate's own rules run anyway.", strings.Join(scan.Tools, ", "))
	case len(missing) == 0:
		add(ok, "", "--scan can run %s.", strings.Join(have, ", "))
	default:
		add(ok, "", "--scan can run %s (not %s).", strings.Join(have, ", "), strings.Join(missing, ", "))
	}

	paint := func(s, code string) string {
		if !color {
			return s
		}
		return code + s + "\x1b[0m"
	}
	fmt.Fprintf(stdout, "interragate %s (%s/%s)\n\n", buildVersion(), runtime.GOOS, runtime.GOARCH)
	problems := 0
	for _, c := range checks {
		mark := paint("✓", "\x1b[32m")
		switch c.level {
		case note:
			mark = paint("!", "\x1b[33m")
		case problem:
			mark = paint("✗", "\x1b[31m")
			problems++
		}
		fmt.Fprintf(stdout, "  %s %s\n", mark, c.text)
		if c.fix != "" {
			fmt.Fprintf(stdout, "    %s\n", paint(c.fix, "\x1b[2m"))
		}
	}
	fmt.Fprintln(stdout)
	if problems > 0 {
		fmt.Fprintln(stdout, "Fix the problems above, then run interragate doctor again.")
		return exitError(1)
	}
	if len(files) > 0 {
		fmt.Fprintln(stdout, "Ready: run interragate here to plan and explain.")
	}
	return nil
}

// sentence makes one of tfexec's messages a line of the report: a capital
// first letter, unless it starts with a command or a flag, and a full stop
// unless it ends with something a binary printed.
func sentence(s string) string {
	first, _, _ := strings.Cut(s, " ")
	switch {
	case s == "", first == "terraform", first == "tofu", strings.HasPrefix(first, "-"), first == "INTERRAGATE_TF_BIN":
	default:
		s = strings.ToUpper(s[:1]) + s[1:]
	}
	if !strings.Contains(s, ": ") && !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

func configFiles(dir string) []string {
	var out []string
	for _, pattern := range []string{"*.tf", "*.tofu", "*.tf.json"} {
		m, _ := filepath.Glob(filepath.Join(dir, pattern))
		out = append(out, m...)
	}
	return out
}

// backend describes where the directory keeps its state, from what init
// recorded, and the selected workspace.
func backend(dir string) string {
	data, err := os.ReadFile(filepath.Join(tfexec.DataDir(dir), "terraform.tfstate"))
	kind := "local"
	if err == nil {
		var s struct {
			Backend struct {
				Type string `json:"type"`
			} `json:"backend"`
		}
		if json.Unmarshal(data, &s) == nil && s.Backend.Type != "" {
			kind = s.Backend.Type
		}
	}
	workspace := tfexec.Workspace(dir)
	if kind == "local" {
		return fmt.Sprintf("State is kept locally, in the %s workspace.", workspace)
	}
	return fmt.Sprintf("State is kept in the %s backend, in the %s workspace.", kind, workspace)
}

func lockedProviders(dir string) int {
	return len(source.LockedVersions(filepath.Join(dir, ".terraform.lock.hcl")))
}

func browserOpener() string {
	switch runtime.GOOS {
	case "darwin":
		return "open"
	case "windows":
		return "rundll32"
	}
	return "xdg-open"
}

// home shortens paths under the home directory to ~.
func home(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		if p == h {
			return "~"
		}
		if strings.HasPrefix(p, h+string(filepath.Separator)) {
			return "~" + p[len(h):]
		}
	}
	return p
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
