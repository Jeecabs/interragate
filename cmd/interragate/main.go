// Command interragate explains a Terraform or OpenTofu plan: what it does,
// why, what could go wrong, and how to fix it, in the terminal, in a pull
// request or as an interactive map.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/Jeecabs/interragate/internal/analysis"
	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/consumers"
	"github.com/Jeecabs/interragate/internal/gitdiff"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
	"github.com/Jeecabs/interragate/internal/render/annotations"
	"github.com/Jeecabs/interragate/internal/render/html"
	"github.com/Jeecabs/interragate/internal/render/markdown"
	"github.com/Jeecabs/interragate/internal/render/mermaid"
	"github.com/Jeecabs/interragate/internal/render/sarif"
	"github.com/Jeecabs/interragate/internal/render/text"
	"github.com/Jeecabs/interragate/internal/rules"
	"github.com/Jeecabs/interragate/internal/scan"
	"github.com/Jeecabs/interragate/internal/tfexec"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// buildVersion is the version interragate reports: the one set at build
// time, or else the module version go install recorded.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

const usage = `interragate: understand a Terraform or OpenTofu plan before you apply it.

Usage:
  interragate [plan] [flags] [-- plan args]   run plan here and explain it
  interragate show    [flags] PLAN            explain a saved plan (plan file, JSON, or - for stdin)
  interragate check   [flags] PLAN            findings only; exits 2 when any reach --fail-on
  interragate report  [flags] PLAN            write the interactive HTML report
  interragate explain [flags] PLAN ADDRESS    everything about one resource
  interragate watch   [flags] [-- plan args]  plan, open a live report, and plan again on every save
  interragate apply   [flags] [-- plan args]  plan, explain, and apply exactly that plan once you confirm
  interragate review  [flags] [PLAN]          go through the changes that need a look; write the notes as Markdown
  interragate diff    [flags] BEFORE AFTER    what changed between two plans of the same configuration
  interragate all     [flags] [DIR]           plan every Terraform root below DIR and list them, worst first
  interragate open    [DIR]                   open the report of the last plan interragate made here, without planning again
  interragate demo    [aws|mechanics]         explain a sample plan, no Terraform needed
  interragate doctor                          check that interragate can plan here, and say how to fix it
  interragate init                            write a starter .interragate.yaml, with a first policy from this configuration
  interragate rules   [--format markdown]     list the rules
  interragate completion bash|zsh|fish        print a shell completion script
  interragate version

Flags:
  -C, --dir DIR         Terraform working directory (default: the plan file's directory, or .)
      --bin NAME        terraform or tofu binary (default: detected)
  -f, --format FORMAT   text, markdown, json, html, github (Actions annotations), sarif (code scanning)
                        or mermaid (a diagram of what changes) (default text)
  -o, --out FILE        write the output to FILE instead of stdout
      --open            also write the HTML report and open it in a browser
      --all             include cosmetic and unchanged resources
      --diff            show attribute diffs under each change
      --fail-on LEVEL   lowest severity that fails check: critical, high, medium, low, none (default high)
      --markdown FILE   also write the Markdown summary to FILE (likewise --json FILE, --html FILE)
      --config FILE     .interragate.yaml to use (default: nearest one)
      --save-plan FILE  keep the binary plan from interragate plan, to apply exactly what you reviewed
      --base REF        find your edits since this git ref (default: where the branch started; none: off)
      --scan TOOLS      also run trivy, checkov or tflint (comma-separated), keeping what concerns this plan
      --no-source       do not read .tf files for locations, locals and lifecycle settings
      --no-init         never run init, even in a directory that needs it
      --no-open         do not open the report in a browser (demo and watch open it by default)
      --no-notify       watch: no desktop notification when a save takes the plan to high or critical risk
      --auto-approve    interragate apply without asking; still refuses findings at --fail-on
      --no-color        plain text
  -v, --verbose         show terraform's own output while planning

Examples:
  interragate demo                          see what interragate does, on a sample plan
  interragate                               plan the current directory and summarise it
  interragate --open -- -var-file=prod.tfvars
  interragate watch                         keep a live report open while you edit
  terraform plan -out tfplan && interragate show tfplan
  terraform show -json tfplan | interragate check - --fail-on critical
  interragate show tfplan -f markdown > comment.md
`

type options struct {
	dir, bin, format, out, failOn, config, savePlan string
	mdOut, jsonOut, htmlOut                         string
	base, scan                                      string
	open, all, diffs, noColor, noSource, verbose    bool
	noInit, noOpen, autoApprove, noNotify           bool
	args, extra                                     []string
	given                                           []string // the flags given, as typed

	cfg       *config.Config // loaded during analysis
	sourceDir string         // the Terraform directory the report describes
	hint      string         // the terminal summary's closing line, when a command has a better one
	scanning  bool           // the command shows findings, so runs the scanners asked for
	skipApply bool           // the command never shows the apply, so it is not simulated
	stderr    io.Writer      // where progress goes
}

func parseArgs(argv []string) (*options, error) {
	o := &options{format: "text"}
	strs := map[string]*string{
		"-C": &o.dir, "--dir": &o.dir, "--bin": &o.bin, "-f": &o.format, "--format": &o.format,
		"-o": &o.out, "--out": &o.out, "--fail-on": &o.failOn, "--config": &o.config, "--save-plan": &o.savePlan,
		"--markdown": &o.mdOut, "--json": &o.jsonOut, "--html": &o.htmlOut,
		"--base": &o.base, "--scan": &o.scan,
	}
	bools := map[string]*bool{
		"--open": &o.open, "--all": &o.all, "--diff": &o.diffs, "--no-color": &o.noColor,
		"--no-source": &o.noSource, "-v": &o.verbose, "--verbose": &o.verbose,
		"--no-init": &o.noInit, "--no-open": &o.noOpen, "--auto-approve": &o.autoApprove, "--no-notify": &o.noNotify,
	}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			o.extra = append(o.extra, argv[i+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			o.args = append(o.args, a)
			continue
		}
		name, value, hasValue := strings.Cut(a, "=")
		if p, ok := bools[name]; ok {
			if hasValue {
				return nil, fmt.Errorf("%s does not take a value", name)
			}
			*p = true
			o.given = append(o.given, name)
			continue
		}
		if p, ok := strs[name]; ok {
			if !hasValue {
				if i+1 >= len(argv) {
					return nil, fmt.Errorf("%s needs a value", name)
				}
				i++
				value = argv[i]
			}
			*p = value
			o.given = append(o.given, name)
			continue
		}
		if a == "-h" || a == "--help" || a == "-help" {
			return nil, errHelp
		}
		return nil, fmt.Errorf("unknown flag %s (see interragate --help)", a)
	}
	switch o.format {
	case "text", "markdown", "md", "json", "html", "github", "sarif", "mermaid":
	default:
		return nil, fmt.Errorf("unknown format %q: use text, markdown, json, html, github, sarif or mermaid", o.format)
	}
	for _, tool := range scanList(o.scan) {
		if !slices.Contains(scan.Tools, tool) {
			return nil, fmt.Errorf("unknown scanner %q: use %s, or none", tool, strings.Join(scan.Tools, ", "))
		}
	}
	if o.format == "md" {
		o.format = "markdown"
	}
	return o, nil
}

var errHelp = errors.New("help")

// commandFlags lists the flags each command uses, by their long names; "--"
// stands for plan arguments. A command refuses any other flag rather than
// ignore it. Every command accepts --no-color, so an alias can carry it,
// and --format text, the default.
var commandFlags = map[string]string{
	"plan":       "--dir --bin --format --out --open --all --diff --markdown --json --html --config --save-plan --base --scan --no-source --no-init --verbose --",
	"show":       "--dir --bin --format --out --open --all --diff --markdown --json --html --config --base --scan --no-source",
	"check":      "--dir --bin --format --out --open --fail-on --markdown --json --html --config --base --scan --no-source",
	"report":     "--dir --bin --format --out --open --markdown --json --html --config --base --scan --no-source",
	"explain":    "--dir --bin --config --base --no-source",
	"watch":      "--dir --bin --open --no-open --no-notify --all --diff --config --base --scan --no-source --no-init --verbose --",
	"apply":      "--dir --bin --open --all --diff --fail-on --markdown --json --html --config --save-plan --base --scan --no-source --no-init --auto-approve --verbose --",
	"review":     "--dir --bin --out --config --base --scan --no-source --no-init --verbose --",
	"all":        "--dir --bin --open --fail-on --config --base --scan --no-source --no-init --verbose --",
	"open":       "--dir --no-open",
	"diff":       "--dir --bin --format --fail-on --config --base --no-source",
	"demo":       "--format --out --open --no-open --all --diff --markdown --json --html --no-source",
	"doctor":     "--dir --bin --config",
	"init":       "--dir",
	"rules":      "--dir --format --config",
	"completion": "",
	"version":    "",
	"help":       "",
}

// shortFlags are the long names of the flags with short forms.
var shortFlags = map[string]string{"-C": "--dir", "-f": "--format", "-o": "--out", "-v": "--verbose"}

// unusedBecause says why a command has no use for a flag, where it helps.
var unusedBecause = map[string]string{
	"watch --format":    "it keeps its report in the browser",
	"watch --out":       "it keeps its report in the browser",
	"watch --save-plan": "it keeps its report in the browser",
	"apply --format":    "it prints the plan for you to confirm",
	"apply --out":       "it prints the plan for you to confirm",
}

// checkFlags refuses the flags cmd has no use for.
func checkFlags(cmd string, o *options) error {
	uses := strings.Fields(commandFlags[cmd])
	for _, typed := range o.given {
		name := typed
		if long, ok := shortFlags[typed]; ok {
			name = long
		}
		if name == "--no-color" || name == "--format" && o.format == "text" || slices.Contains(uses, name) {
			continue
		}
		if why := unusedBecause[cmd+" "+name]; why != "" {
			return fmt.Errorf("interragate %s does not use %s: %s", cmd, typed, why)
		}
		return fmt.Errorf("interragate %s does not use %s", cmd, typed)
	}
	if len(o.extra) > 0 && !slices.Contains(uses, "--") {
		return fmt.Errorf("interragate %s does not run plan, so it takes no plan arguments after --", cmd)
	}
	return nil
}

// exitError carries an exit status without printing anything more.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func main() {
	// Go exits 2 on a panic, which is also what check uses for "findings";
	// report crashes as an ordinary error instead.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "interragate: internal error: %v\n\n%s\nPlease report this with the plan that caused it.\n", r, debug.Stack())
			os.Exit(1)
		}
	}()
	ctx, stop := notifyStop()
	defer stop()
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	var code exitError
	var stopped *tfexec.Stopped
	switch {
	case err == nil:
	case errors.Is(err, errHelp):
		fmt.Fprint(os.Stdout, usage)
	case errors.As(err, &code):
		os.Exit(int(code))
	case errors.As(context.Cause(ctx), &stopped):
		// Whatever failed, failed because interragate was asked to stop.
		fmt.Fprintln(os.Stderr, "interragate:", stopped)
		os.Exit(stopped.ExitCode())
	default:
		fmt.Fprintln(os.Stderr, "interragate:", err)
		os.Exit(1)
	}
}

// notifyStop returns a context that ends, with a *tfexec.Stopped cause,
// when interragate is interrupted, terminated or hung up on. Running
// Terraform commands then stop as Terraform expects, and interragate cleans
// up and exits. interragate keeps catching those signals afterwards, so that
// a second Ctrl-C, which Terraform sees too, does not kill interragate
// before Terraform has stopped. Catching a signal also leaves it at its
// default for the commands interragate runs, even when interragate started
// with it ignored, as a background job of a script does with Ctrl-C; the
// interrupt interragate passes on then still stops them. A hangup ignored
// when interragate started, as nohup arranges, stays ignored, for Terraform
// too.
func notifyStop() (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	if !signal.Ignored(syscall.SIGHUP) {
		signal.Notify(ch, syscall.SIGHUP)
	}
	go func() {
		select {
		case sig := <-ch:
			cancel(&tfexec.Stopped{Signal: sig})
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(ch)
		cancel(nil)
	}
}

var commands = map[string]bool{
	"plan": true, "show": true, "check": true, "report": true, "explain": true, "rules": true, "version": true, "help": true,
	"demo": true, "doctor": true, "completion": true, "__addresses": true, "watch": true, "diff": true, "apply": true, "review": true, "init": true,
	"all": true, "open": true,
}

func run(ctx context.Context, argv []string, stdout, stderr io.Writer) error {
	o, err := parseArgs(argv)
	if err != nil {
		return err
	}
	// The subcommand is the first positional argument, wherever flags put it.
	cmd := "plan"
	if len(o.args) > 0 && commands[o.args[0]] {
		cmd, o.args = o.args[0], o.args[1:]
	}
	// __addresses answers the shell, which only ever passes a plan.
	if cmd != "__addresses" {
		if err := checkFlags(cmd, o); err != nil {
			return err
		}
	}
	o.stderr = stderr
	o.scanning = scanCommands[cmd]
	// check prints findings, which do not need the apply simulated; on a
	// large plan that saves real time in CI.
	o.skipApply = cmd == "__addresses" || cmd == "check" && (o.format == "text" || o.format == "github" || o.format == "sarif") &&
		o.mdOut == "" && o.jsonOut == "" && o.htmlOut == "" && !o.open
	if _, err := failThreshold(o); err != nil {
		return err
	}
	switch cmd {
	case "help":
		return errHelp
	case "version":
		fmt.Fprintf(stdout, "interragate %s (%s/%s)\n", buildVersion(), runtime.GOOS, runtime.GOARCH)
		return nil
	case "rules":
		return listRules(stdout, o)
	case "demo":
		return demoCmd(o, stdout, stderr)
	case "watch":
		return watchCmd(ctx, o, stdout, stderr)
	case "apply":
		return applyCmd(ctx, o, stdout, stderr)
	case "review":
		return reviewCmd(ctx, o, stdout, stderr)
	case "all":
		return allCmd(ctx, o, stdout, stderr)
	case "open":
		return openCmd(o, stdout, stderr)
	case "diff":
		return diffCmd(ctx, o, stdout)
	case "doctor":
		return doctorCmd(ctx, o, stdout)
	case "init":
		return initCmd(o, stdout)
	case "completion":
		return completionCmd(o, stdout)
	case "__addresses":
		return addressesCmd(ctx, o, stdout)
	case "plan":
		if len(o.args) > 0 {
			if _, err := os.Stat(o.args[0]); err != nil {
				if near := nearestCommand(o.args[0]); near != "" {
					return fmt.Errorf("no command %q; did you mean interragate %s?", o.args[0], near)
				}
			}
			return fmt.Errorf("interragate plan takes no arguments; to read a saved plan use interragate show %s", o.args[0])
		}
		return planCmd(ctx, o, stdout, stderr)
	case "show", "check", "report":
		if len(o.args) != 1 {
			return fmt.Errorf("interragate %s needs one plan: a plan file, a JSON plan, or - for stdin", cmd)
		}
		if cmd == "report" && o.format == "text" {
			o.format = "html"
		}
		r, err := loadReport(ctx, o, o.args[0])
		if err != nil {
			return err
		}
		return emit(cmd, r, o, stdout, stderr)
	case "explain":
		if len(o.args) != 2 {
			return errors.New("interragate explain needs a plan and a resource address")
		}
		r, err := loadReport(ctx, o, o.args[0])
		if err != nil {
			return err
		}
		return explain(r, o.args[1], o, stdout)
	}
	return nil
}

// nearestCommand is the command a mistyped word most likely meant, or ""
// when none is within two edits of it. A typo rarely changes the first
// letter, and a word like tfplan is more likely a missing plan file than
// interragate plan.
func nearestCommand(word string) string {
	word = strings.ToLower(word)
	best, bestDist := "", 3
	for name := range commands {
		if strings.HasPrefix(name, "_") || word == "" || word[0] != name[0] {
			continue
		}
		if d := editDistance(word, name); d < bestDist || d == bestDist && name < best {
			best, bestDist = name, d
		}
	}
	return best
}

// editDistance counts the insertions, deletions, substitutions and swaps
// of neighbours that turn a into b.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}

func planCmd(ctx context.Context, o *options, stdout, stderr io.Writer) error {
	dir := o.dir
	if dir == "" {
		dir = "."
	}
	if !hasConfigFiles(dir) {
		return noConfigError(dir)
	}
	runner, err := newRunner(ctx, o, dir, stderr)
	if err != nil {
		return err
	}
	planFile := o.savePlan
	if planFile == "" {
		f, err := os.CreateTemp("", "interragate-*.tfplan")
		if err != nil {
			return err
		}
		f.Close()
		planFile = f.Name()
		defer os.Remove(planFile)
	}
	if abs, err := filepath.Abs(planFile); err == nil {
		planFile = abs
	}
	pl := &planner{o: o, runner: runner, dir: dir, stderr: stderr, progress: isTerminal(stderr) && !o.verbose}
	start := time.Now()
	r, _, err := pl.plan(ctx, planFile)
	if err != nil {
		return err
	}
	if pl.progress {
		fmt.Fprintf(stderr, "Planned in %s.\n", time.Since(start).Round(100*time.Millisecond))
	}
	keepReport(dir, r)
	if err := emit("plan", r, o, stdout, stderr); err != nil {
		return err
	}
	if o.savePlan != "" && o.format == "text" {
		fmt.Fprintf(stderr, "\nSaved the plan to %s. To apply exactly this plan: %s apply %s\n", o.savePlan, runner.Name(), o.savePlan)
	}
	return nil
}

// newRunner finds terraform or tofu for dir, and says so when it passed
// over a binary that does not run: the first init ties the directory to the
// tool that ran it.
func newRunner(ctx context.Context, o *options, dir string, stderr io.Writer) (*tfexec.Runner, error) {
	ch, err := tfexec.Choose(ctx, dir, o.bin)
	if err != nil {
		return nil, err
	}
	if ch.Runner == nil {
		return nil, ch.Err()
	}
	runner := ch.Runner
	for _, c := range ch.Skipped {
		fmt.Fprintf(stderr, "%s does not run here (%s), so interragate uses %s.\n", c.Name, c.Problem, runner.Name())
	}
	if ch.Note != "" {
		fmt.Fprintln(stderr, ch.Note)
	}
	// Terraform's output shown as it runs (by apply, and with -v) is
	// coloured only on a terminal. What interragate keeps, for an error or
	// the live page, is always plain.
	runner.NoColor = o.noColor || os.Getenv("NO_COLOR") != "" || !isTerminal(stderr)
	return runner, nil
}

// planner runs plan in a directory, running init first when the directory
// needs it, and analyses the result.
type planner struct {
	o        *options
	runner   *tfexec.Runner
	dir      string
	stderr   io.Writer
	progress bool // say what is running on stderr's current line
}

func (pl *planner) status(msg string) {
	if pl.progress {
		fmt.Fprint(pl.stderr, "\r\x1b[K"+msg)
	}
}

// plan runs plan into planFile and returns the analysed report and the
// plan it came from.
func (pl *planner) plan(ctx context.Context, planFile string) (*model.Report, *plan.Plan, error) {
	o, runner, dir := pl.o, pl.runner, pl.dir
	log := io.Discard
	if o.verbose {
		log = pl.stderr
	}
	initialise := func(why string) error {
		pl.status(fmt.Sprintf("Running %s init in %s (%s)…", runner.Name(), displayDir(dir), why))
		err := runner.Init(ctx, log)
		pl.status("")
		if tfexec.NeedsApproval(err) {
			return fmt.Errorf("%s init wants to copy the existing state to the new backend, which needs your approval: run %s init here, then run interragate again", runner.Name(), runner.Name())
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(pl.stderr, "Ran %s init first: %s.\n", runner.Name(), why)
		return nil
	}
	runPlan := func() error {
		lead := fmt.Sprintf("Planning with %s in %s", runner.Name(), displayDir(dir))
		pl.status(lead + "…")
		var err error
		if pl.progress {
			// At a terminal, Terraform's event stream says how far the
			// plan has got; long plans no longer look stuck.
			pp := &planProgress{lead: lead, width: lineWidth(pl.stderr), show: pl.status}
			err = runner.PlanWithProgress(ctx, planFile, o.extra, pp.event)
		} else {
			err = runner.Plan(ctx, planFile, o.extra, log)
		}
		pl.status("")
		return err
	}
	// Run init only when Terraform asks for it, once: in a directory that
	// has never been initialised, or one whose configuration has changed
	// since (a new module or provider, often after a pull). Guessing from
	// the directory is not enough: init in a configuration that uses only
	// built-in providers leaves nothing behind.
	err := runPlan()
	if err != nil && ctx.Err() == nil && !o.noInit && tfexec.NeedsInit(err) {
		if reason := tfexec.BackendChange(err); reason != "" {
			// What happens to the state is the user's call.
			name := runner.Name()
			return nil, nil, fmt.Errorf("the backend has changed since the last init (%s), and init must be told what to do with the state, which interragate leaves to you. Run %s init -migrate-state to copy the state to the new backend, or %s init -reconfigure to use the new backend without it, then run interragate again", reason, name, name)
		}
		why := "the configuration has changed since the last init"
		if !tfexec.Initialised(dir) {
			why = "this directory had not been initialised"
		}
		if err := initialise(why); err != nil {
			return nil, nil, err
		}
		err = runPlan()
	}
	if ctx.Err() != nil {
		return nil, nil, context.Cause(ctx)
	}
	if err != nil {
		return nil, nil, err
	}
	data, err := runner.ShowJSON(ctx, planFile)
	if err != nil {
		return nil, nil, err
	}
	p, err := plan.Parse(data)
	if err != nil {
		return nil, nil, err
	}
	r, err := analyze(ctx, p, o, dir, runner.Tool, "")
	if err != nil {
		return nil, nil, err
	}
	// The plan file does not say which workspace it was made in, but
	// having just run it here, interragate knows.
	r.Plan.Workspace = tfexec.Workspace(dir)
	return r, p, nil
}

func displayDir(dir string) string {
	if dir == "." {
		if wd, err := os.Getwd(); err == nil {
			return filepath.Base(wd)
		}
	}
	return dir
}

// loadReport reads a plan from a file or stdin and analyses it.
func loadReport(ctx context.Context, o *options, arg string) (*model.Report, error) {
	var data []byte
	var err error
	source := arg
	dir := o.dir
	tool := ""
	if arg == "-" {
		data, err = io.ReadAll(bufio.NewReader(os.Stdin))
		source = "stdin"
		if dir == "" {
			dir = "."
		}
	} else {
		data, err = os.ReadFile(arg)
		source = filepath.Base(arg)
		if dir == "" {
			dir = filepath.Dir(arg)
		}
	}
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // a byte-order mark from some editors and shells
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] != '{' {
		// A binary plan file: let terraform (or tofu) convert it. A plan
		// piped on stdin is written to a temporary file first.
		runner, err := tfexec.Find(ctx, dir, o.bin)
		if err != nil {
			return nil, err
		}
		path := arg
		if arg == "-" {
			tmp, err := os.CreateTemp("", "interragate-*.tfplan")
			if err != nil {
				return nil, err
			}
			defer os.Remove(tmp.Name())
			if _, err := tmp.Write(data); err != nil {
				tmp.Close()
				return nil, err
			}
			tmp.Close()
			path = tmp.Name()
		}
		abs, _ := filepath.Abs(path)
		data, err = runner.ShowJSON(ctx, abs)
		if err != nil {
			return nil, err
		}
		tool = runner.Tool
	}
	p, err := plan.Parse(data)
	if err != nil {
		return nil, err
	}
	return analyze(ctx, p, o, dir, tool, source)
}

func analyze(ctx context.Context, p *plan.Plan, o *options, dir, tool, source string) (*model.Report, error) {
	cfg, err := loadConfig(o, dir)
	if err != nil {
		return nil, err
	}
	return analyzeWith(p, o, cfg, dir, tool, source, runScans(ctx, o, cfg, dir)), nil
}

// analyzeWith analyses with a configuration the caller has already loaded
// (or nil, for the defaults) and what scanners reported, if any ran.
func analyzeWith(p *plan.Plan, o *options, cfg *config.Config, dir, tool, source string, scans []analysis.ScanResult) *model.Report {
	o.cfg = cfg
	o.sourceDir = dir
	srcDir := dir
	if o.noSource || !hasConfigFiles(dir) {
		srcDir = ""
	}
	return analysis.Analyze(p, analysis.Options{
		SourceDir: srcDir,
		Source:    source,
		Tool:      tool,
		Config:    cfg,
		Generator: "interragate " + buildVersion(),
		Edits:     gitdiff.Load(srcDir, o.base, p.Configuration),
		Scans:     scans,
		SkipApply: o.skipApply,
	})
}

// scanCommands are the commands that show findings, so run the scanners
// --scan or the configuration's scan list asks for.
var scanCommands = map[string]bool{"plan": true, "show": true, "check": true, "report": true, "watch": true, "apply": true, "review": true, "all": true}

// scanList reads --scan: scanner names separated by commas, or none.
func scanList(s string) []string {
	var out []string
	for _, name := range strings.Split(s, ",") {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" && name != "none" {
			out = append(out, name)
		}
	}
	return out
}

// runScans runs the scanners asked for on the configuration in dir: those
// --scan names, or else the configuration's scan list. A scanner that is
// not installed or fails is reported in the report rather than stopping it.
func runScans(ctx context.Context, o *options, cfg *config.Config, dir string) []analysis.ScanResult {
	tools := scanList(o.scan)
	if o.scan == "" && cfg != nil {
		tools = cfg.Scan
	}
	if !o.scanning || len(tools) == 0 || o.noSource || !hasConfigFiles(dir) {
		return nil
	}
	var out []analysis.ScanResult
	for _, tool := range tools {
		if o.stderr != nil && isTerminal(o.stderr) {
			fmt.Fprintf(o.stderr, "Scanning with %s...\n", tool)
		}
		issues, err := scan.Run(ctx, tool, dir)
		if errors.Is(err, scan.ErrNotInstalled) {
			err = errors.New("it is not on PATH")
		}
		out = append(out, analysis.ScanResult{Tool: tool, Issues: issues, Err: err})
	}
	return out
}

func hasConfigFiles(dir string) bool {
	for _, pattern := range []string{"*.tf", "*.tofu", "*.tf.json"} {
		if m, _ := filepath.Glob(filepath.Join(dir, pattern)); len(m) > 0 {
			return true
		}
	}
	return false
}

func loadConfig(o *options, dir string) (*config.Config, error) {
	path := o.config
	if path == "" {
		found, ok := config.Find(dir)
		if !ok {
			return nil, nil
		}
		path = found
	}
	return readConfig(path)
}

// readConfig loads a configuration file, keeping the team's policies off
// the IDs of interragate's own rules.
func readConfig(path string) (*config.Config, error) {
	return config.Load(path, config.ReserveRuleIDs(rules.IDs()...))
}

// emit writes the report in the requested format, opens the HTML report if
// asked, and applies check's exit status.
func emit(cmd string, r *model.Report, o *options, stdout, stderr io.Writer) error {
	w := stdout
	var file *os.File
	if o.out != "" {
		f, err := os.Create(o.out)
		if err != nil {
			return err
		}
		file, w = f, f
		defer f.Close()
	}
	color := !o.noColor && os.Getenv("NO_COLOR") == "" && file == nil && isTerminal(stdout)
	width := 100
	if f, ok := stdout.(*os.File); ok && file == nil {
		if wd, _, err := term.GetSize(int(f.Fd())); err == nil && wd > 40 {
			width = min(wd, 120)
		}
	}
	threshold, err := failThreshold(o)
	if err != nil {
		return err
	}
	switch o.format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		err = enc.Encode(r)
	case "markdown":
		err = markdown.Render(w, r, markdown.Options{})
	case "github":
		err = annotations.Render(w, r, repoPrefix(o.sourceDir))
	case "sarif":
		err = sarif.Render(w, r, sarif.Options{Root: repoPrefix(o.sourceDir), Version: buildVersion()})
	case "mermaid":
		err = mermaid.Render(w, r, mermaid.Options{Max: 300})
	case "html":
		if file == nil && cmd == "report" {
			path, werr := writeHTML(r, "interragate-report.html")
			if werr != nil {
				return werr
			}
			fmt.Fprintf(stderr, "Wrote %s\n", path)
			if err := extraOutputs(r, o); err != nil {
				return err
			}
			if o.open {
				if err := openBrowser(path); err != nil {
					fmt.Fprintf(stderr, "Could not open a browser: %v.\n", err)
				}
			}
			return nil
		}
		err = html.Render(w, r)
	default:
		topts := text.Options{Color: color, Width: width, All: o.all, Diffs: o.diffs}
		if cmd == "check" {
			topts.MinSeverity = model.Low
			err = text.RenderFindings(w, r, topts)
		} else {
			switch {
			case o.hint != "":
				topts.Hint = o.hint
			case !o.open && cmd == "plan":
				topts.Hint = "Map and diffs, without planning again: interragate open"
			case !o.open && cmd == "show" && len(o.args) == 1:
				topts.Hint = "Map and diffs: interragate report " + o.args[0] + " --open"
			}
			err = text.Render(w, r, topts)
		}
	}
	if err != nil {
		return err
	}
	if err := extraOutputs(r, o); err != nil {
		return err
	}
	if o.open && o.format != "html" {
		path := o.htmlOut
		if path == "" {
			if path, err = writeHTML(r, ""); err != nil {
				return err
			}
		}
		if err := openBrowser(path); err != nil {
			fmt.Fprintf(stderr, "Report written to %s (could not open a browser: %v)\n", path, err)
		}
	}
	if cmd == "check" && threshold <= model.Critical && failing(r, threshold) > 0 {
		return exitError(2)
	}
	return nil
}

// extraOutputs writes the additional formats asked for with --markdown,
// --json and --html, so one run can feed a whole pipeline.
func extraOutputs(r *model.Report, o *options) error {
	write := func(path string, render func(io.Writer) error) error {
		if path == "" {
			return nil
		}
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		if err := render(f); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	if err := write(o.mdOut, func(w io.Writer) error { return markdown.Render(w, r, markdown.Options{}) }); err != nil {
		return err
	}
	if err := write(o.jsonOut, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}); err != nil {
		return err
	}
	return write(o.htmlOut, func(w io.Writer) error { return html.Render(w, r) })
}

// failThreshold is the lowest severity that fails check: the flag, then
// fail_on in .interragate.yaml, then high. "none" never fails.
// repoPrefix is the path from the git repository root to dir, so
// annotation paths resolve on GitHub.
func repoPrefix(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for root := abs; ; {
		if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
			rel, err := filepath.Rel(root, abs)
			if err != nil || rel == "." {
				return ""
			}
			return rel
		}
		parent := filepath.Dir(root)
		if parent == root {
			return ""
		}
		root = parent
	}
}

func failThreshold(o *options) (model.Severity, error) {
	name := o.failOn
	if name == "" && o.cfg != nil {
		name = o.cfg.FailOn
	}
	if name == "" {
		name = "high"
	}
	if strings.EqualFold(name, "none") {
		return model.Critical + 1, nil
	}
	return model.ParseSeverity(name)
}

func failing(r *model.Report, threshold model.Severity) int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity >= threshold {
			n++
		}
	}
	return n
}

// writeHTML writes the report to path, or to a temporary file when path is
// empty, and returns where it went.
func writeHTML(r *model.Report, path string) (string, error) {
	var f *os.File
	var err error
	if path == "" {
		f, err = os.CreateTemp("", "interragate-*.html")
	} else {
		f, err = os.Create(path)
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := html.Render(f, r); err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(f.Name())
	return abs, nil
}

// openBrowser opens a file in the default browser. It gives the opener a
// moment: one that fails straight away, as xdg-open does with no browser
// to hand the file to, is reported; one still running by then has handed
// over to the browser, or is the browser.
func openBrowser(path string) error {
	cmd := opener("file://" + filepath.ToSlash(path))
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			return fmt.Errorf("%s failed: %v", filepath.Base(cmd.Path), err)
		}
	case <-time.After(openerWait):
	}
	return nil
}

// opener is the command that opens url in the default browser; tests
// replace it.
var opener = func(url string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	}
	return exec.Command("xdg-open", url)
}

// openerWait is how long openBrowser waits to see whether the opener fails.
var openerWait = 2 * time.Second

func explain(r *model.Report, addr string, o *options, stdout io.Writer) error {
	var matches []*model.Change
	for _, ch := range r.Changes {
		if ch.Address == addr {
			matches = []*model.Change{ch}
			break
		}
		if ch.Resource == addr || strings.HasPrefix(ch.Address, addr+"[") {
			matches = append(matches, ch)
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("no resource %s in this plan", addr)
	}
	color := !o.noColor && os.Getenv("NO_COLOR") == "" && isTerminal(stdout)
	for i, ch := range matches {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		if err := text.RenderChange(stdout, r, ch, text.Options{Color: color}); err != nil {
			return err
		}
	}
	return nil
}

func listRules(w io.Writer, o *options) error {
	all := rules.All()
	// The team's own policies, from the nearest .interragate.yaml, follow
	// interragate's rules.
	dir := o.dir
	if dir == "" {
		dir = "."
	}
	cfg, err := loadConfig(o, dir)
	if err != nil {
		return err
	}
	policies, from := rules.Policies(cfg), ""
	if cfg != nil {
		from = filepath.Base(cfg.Path)
	}
	if o.format == "markdown" {
		fmt.Fprintln(w, "# Rules")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "interragate's rules look at what a plan does, not only at configuration, so they flag what a change introduces.")
		fmt.Fprintln(w, "Disable one with `disable: [rule-id]` in `.interragate.yaml`, or change its severity with `severity: {rule-id: low}`.")
		fmt.Fprintln(w, "Add rules of your own under `policies:` in the same file, as [Your own rules](https://github.com/Jeecabs/interragate/blob/main/docs/configuration.md#your-own-rules) describes; `interragate rules` lists them after these.")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "| Rule | Default | Category |")
		fmt.Fprintln(w, "|---|---|---|")
		for _, r := range all {
			fmt.Fprintf(w, "| [`%s`](#%s) | %s | %s |\n", r.ID, r.ID, r.Severity, r.Category)
		}
		for _, r := range all {
			fmt.Fprintf(w, "\n## %s\n\n**%s** · default severity %s · %s\n\n%s\n", r.ID, r.Title, r.Severity, r.Category, r.Doc)
		}
		if len(policies) > 0 {
			fmt.Fprintf(w, "\n# Policies\n\nThis team's own rules, from `%s`.\n\n| Policy | Severity |\n|---|---|\n", from)
			for _, r := range policies {
				fmt.Fprintf(w, "| [`%s`](#%s) | %s |\n", r.ID, r.ID, r.Severity)
			}
			for _, r := range policies {
				fmt.Fprintf(w, "\n## %s\n\n**%s** · severity %s · policy from `%s`\n\n%s\n", r.ID, r.Title, r.Severity, from, r.Doc)
			}
		}
		return nil
	}
	if o.format == "json" {
		type ruleDoc struct {
			ID, Title, Category, Doc string
			Severity                 model.Severity
			// Source is the file a policy comes from; interragate's own rules have
			// none.
			Source string `json:",omitempty"`
		}
		var docs []ruleDoc
		for _, r := range all {
			docs = append(docs, ruleDoc{ID: r.ID, Title: r.Title, Category: r.Category, Doc: r.Doc, Severity: r.Severity})
		}
		for _, r := range policies {
			docs = append(docs, ruleDoc{ID: r.ID, Title: r.Title, Category: r.Category, Doc: r.Doc, Severity: r.Severity, Source: from})
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(docs)
	}
	for _, r := range all {
		fmt.Fprintf(w, "%-30s %-9s %-13s %s\n", r.ID, r.Severity, r.Category, r.Title)
	}
	if len(policies) > 0 {
		fmt.Fprintf(w, "\nPolicies from %s:\n", from)
		for _, r := range policies {
			fmt.Fprintf(w, "%-30s %-9s %-13s %s\n", r.ID, r.Severity, r.Category, r.Title)
		}
	}
	// The services outside Terraform the consumer-* rules check plans
	// against, so a typo shows before a plan is needed to find it.
	if cfg != nil && len(cfg.Consumers) > 0 {
		fmt.Fprintf(w, "\nServices outside Terraform from %s:\n", from)
		for _, c := range cfg.Consumers {
			uses := make([]string, 0, len(c.Uses))
			for _, u := range c.Uses {
				uses = append(uses, u.Target.String())
			}
			name := string(c.Name)
			if p := consumers.PlatformName(c.Platform); p != "" {
				name += " (" + p + ")"
			}
			fmt.Fprintf(w, "%-30s uses %s\n", name, strings.Join(uses, ", "))
		}
	}
	return nil
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
