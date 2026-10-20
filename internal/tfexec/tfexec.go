// Package tfexec finds and runs the Terraform or OpenTofu binary for a
// working directory.
package tfexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Runner runs one binary in one working directory. When the context a
// command runs with ends, as it does when interragate is interrupted, the
// command stops the way Terraform expects (see runCommand).
type Runner struct {
	Bin     string // path to the binary
	Tool    string // "terraform" or "opentofu"
	Dir     string
	Version string // e.g. "1.16.6", when Find checked that the binary runs
	// NoColor asks for plain output where Terraform's output is shown: by
	// apply, and by plan and init with -v. Output interragate only captures,
	// for an error message or the live page, is always plain.
	NoColor bool
}

// Name is the command users type: terraform or tofu.
func (r *Runner) Name() string { return filepath.Base(r.Bin) }

// ShowJSON converts a saved plan file to JSON.
func (r *Runner) ShowJSON(ctx context.Context, planFile string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(r.Bin, "show", "-json", "-no-color", planFile)
	cmd.Dir = r.Dir
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := runCommand(ctx, cmd); err != nil {
		return nil, fmt.Errorf("%s show -json %s: %w\n%s", r.Name(), planFile, err, plain(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Plan runs `plan -out=planFile` with extra arguments. Terraform's own output
// goes to log (which may be io.Discard); on failure it is returned in the
// error so the user sees what went wrong.
func (r *Runner) Plan(ctx context.Context, planFile string, args []string, log io.Writer) error {
	return r.run(ctx, log, append([]string{"plan", "-input=false", "-out=" + planFile}, args...)...)
}

// Init runs `init -input=false`, which installs the providers and modules
// the configuration needs and connects to its backend. It never upgrades
// providers that are already locked, and fails rather than prompting, for
// example when a backend change needs its state migrated.
func (r *Runner) Init(ctx context.Context, log io.Writer) error {
	return r.run(ctx, log, "init", "-input=false")
}

// Apply runs `apply` on a saved plan file, so what is applied is exactly
// what was planned. Terraform talks to the terminal directly: its output
// streams as usual (the plan file means it never asks for input).
func (r *Runner) Apply(ctx context.Context, planFile string, stdout, stderr io.Writer) error {
	args := []string{"apply", "-input=false"}
	if r.NoColor {
		args = append(args, "-no-color")
	}
	cmd := exec.Command(r.Bin, append(args, planFile)...)
	cmd.Dir = r.Dir
	cmd.Stdin = os.Stdin
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := runCommand(ctx, cmd); err != nil {
		return &CommandError{Err: err, Shown: true, Command: r.Name() + " apply"}
	}
	return nil
}

func (r *Runner) run(ctx context.Context, log io.Writer, args ...string) error {
	if log == nil {
		log = io.Discard
	}
	sub := args[0]
	if r.NoColor || log == io.Discard {
		args = append([]string{sub, "-no-color"}, args[1:]...)
	}
	var captured bytes.Buffer
	cmd := exec.Command(r.Bin, args...)
	cmd.Dir = r.Dir
	cmd.Stdout = io.MultiWriter(&captured, log)
	cmd.Stderr = cmd.Stdout
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1")
	if err := runCommand(ctx, cmd); err != nil {
		return &CommandError{
			Err:     err,
			Output:  plain(captured.String()),
			Shown:   log != io.Discard,
			Command: r.Name() + " " + sub,
		}
	}
	return nil
}

// escapes matches terminal escape sequences: colours and other control
// sequences, and links.
var escapes = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")

// plain is captured output without escape sequences, which mean nothing
// in an error message or on the live page, and without surrounding space.
func plain(s string) string {
	return strings.TrimSpace(escapes.ReplaceAllString(s, ""))
}

// CommandError is a failed Terraform command, with its output.
type CommandError struct {
	Err     error
	Output  string
	Shown   bool   // the output was already printed as it ran
	Command string // what the user would type, e.g. "terraform plan"
}

func (e *CommandError) Error() string {
	msg := fmt.Sprintf("%s failed:\n\n%s", e.Command, e.Output)
	if e.Output == "" || e.Shown {
		msg = fmt.Sprintf("%s failed: %v", e.Command, e.Err)
	}
	if h := e.Hint(); h != "" {
		msg = strings.TrimRight(msg, "\n") + "\n\n" + h
	}
	return msg
}

func (e *CommandError) Unwrap() error { return e.Err }

// DataDir is where Terraform keeps what init installs and the selected
// workspace for dir: TF_DATA_DIR when set (relative to dir, as Terraform
// resolves it), else .terraform.
func DataDir(dir string) string {
	if d := os.Getenv("TF_DATA_DIR"); d != "" {
		if filepath.IsAbs(d) {
			return d
		}
		return filepath.Join(dir, d)
	}
	return filepath.Join(dir, ".terraform")
}

// Workspace is the workspace commands in dir use: TF_WORKSPACE when set,
// else the one init or workspace select recorded, else "default".
func Workspace(dir string) string {
	if w := os.Getenv("TF_WORKSPACE"); w != "" {
		return w
	}
	if b, err := os.ReadFile(filepath.Join(DataDir(dir), "environment")); err == nil {
		if w := strings.TrimSpace(string(b)); w != "" {
			return w
		}
	}
	return "default"
}

// Initialised reports whether init has left anything in dir's data
// directory. Init in a configuration that uses only built-in providers
// creates none, so a directory can be ready without it.
func Initialised(dir string) bool {
	_, err := os.Stat(DataDir(dir))
	return !errors.Is(err, fs.ErrNotExist)
}

// asksForInit matches the advice Terraform and OpenTofu print when a
// command needs init first: modules or providers missing, a lock file that
// no longer matches the configuration, or a backend not yet set up.
var asksForInit = regexp.MustCompile(`\b(terraform|tofu) init\b`)

// NeedsInit reports whether err is a command that failed because the
// directory needs init.
func NeedsInit(err error) bool {
	var ce *CommandError
	return errors.As(err, &ce) && asksForInit.MatchString(ce.Output)
}

// initReason is the line in which Terraform says why the backend needs init.
var initReason = regexp.MustCompile(`(?m)^\s*Reason: (.+?)\s*$`)

// BackendChange says why the backend needs init again, when err is a
// command that failed because the backend changed since the last init:
// "Backend configuration block has changed". That init has to be told
// -migrate-state or -reconfigure, what happens to the state, which is for
// the user to decide. A backend set up for the first time needs only a
// plain init, so it is not a change.
func BackendChange(err error) string {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return ""
	}
	if strings.Contains(ce.Output, "Backend configuration changed") {
		return "Backend configuration changed"
	}
	if !strings.Contains(ce.Output, "initialization required") {
		return ""
	}
	m := initReason.FindStringSubmatch(ce.Output)
	if m == nil || strings.HasPrefix(m[1], "Initial configuration") {
		return ""
	}
	return m[1]
}

// NeedsApproval reports whether err is an init that stopped because moving
// existing state to a new backend needs the user to say yes.
func NeedsApproval(err error) bool {
	var ce *CommandError
	return errors.As(err, &ce) && strings.Contains(ce.Output, "Can't ask approval for state migration")
}
