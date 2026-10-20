package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/tfexec"
)

// confirmInput is where interragate apply reads the confirmation; tests swap
// it.
var confirmInput io.Reader = os.Stdin

// stdinIsTerminal reports whether someone is there to confirm; tests swap it.
var stdinIsTerminal = func() bool { return isTerminal(os.Stdin) }

// applyCmd plans, explains the plan, and applies exactly that plan once
// confirmed. The confirmation grows with the risk: "yes" for an ordinary
// plan, the directory's name when a finding reaches --fail-on (high by
// default). --auto-approve skips the question for scripts, but still
// refuses a plan with such a finding unless --fail-on none says otherwise.
func applyCmd(ctx context.Context, o *options, stdout, stderr io.Writer) error {
	if len(o.args) > 0 {
		return fmt.Errorf("interragate apply takes no arguments; pass plan arguments after --, as in interragate apply -- -var-file=prod.tfvars")
	}
	// Say so before planning, not after a plan nobody can confirm.
	if !o.autoApprove && !stdinIsTerminal() {
		return errors.New("interragate apply needs someone at a terminal to confirm, or --auto-approve")
	}
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
	r, _, err := pl.plan(ctx, planFile)
	if err != nil {
		return err
	}
	if err := emit("apply", r, o, stdout, stderr); err != nil {
		return err
	}
	if !applies(r) {
		fmt.Fprintln(stdout, "\nNothing to apply.")
		return nil
	}

	threshold, err := failThreshold(o)
	if err != nil {
		return err
	}
	risky := failing(r, threshold)
	name := dir
	if abs, err := filepath.Abs(dir); err == nil {
		name = filepath.Base(abs)
	}
	switch {
	case o.autoApprove && risky > 0:
		return fmt.Errorf("not applying: %s at or above %s (see above); fix them, or pass --fail-on with a higher level, or --fail-on none, to apply anyway", plural(risky, "finding"), threshold)
	case o.autoApprove:
	default:
		want := "yes"
		target := ""
		if ws := r.Plan.Workspace; ws != "" && ws != "default" {
			target = " to the " + ws + " workspace"
		}
		question := fmt.Sprintf("\nApply %s%s with %s? Only \"yes\" applies it: ", describeTotal(r), target, runner.Name())
		switch {
		case expectsFailure(r):
			// A failed apply stops part way, which can leave a replaced
			// resource destroyed and not yet created again.
			want = name
			question = fmt.Sprintf("\ninterragate expects this apply to fail part way (see \"When you apply\"). To apply it%s anyway, type the directory's name (%s): ", target, name)
		case risky > 0:
			want = name
			question = fmt.Sprintf("\nThis plan has %s at or above %s. To apply it%s anyway, type the directory's name (%s): ", plural(risky, "finding"), threshold, target, name)
		}
		answer, _ := ask(ctx, bufio.NewReader(confirmInput), stdout, question)
		if ctx.Err() != nil || strings.TrimSpace(answer) != want {
			fmt.Fprintln(stdout, "Not applied.")
			return stoppedOr(ctx, 1)
		}
	}
	// A stop that came while the plan was shown still counts.
	if ctx.Err() != nil {
		fmt.Fprintln(stdout, "\nNot applied.")
		return stoppedOr(ctx, 1)
	}
	fmt.Fprintf(stderr, "\nApplying the plan above with %s.\n\n", runner.Name())
	return runner.Apply(ctx, planFile, stdout, stderr)
}

// expectsFailure reports whether interragate expects a step of the apply to
// fail.
func expectsFailure(r *model.Report) bool {
	if r.Apply == nil {
		return false
	}
	for _, st := range r.Apply.Steps {
		if st.Fails != "" {
			return true
		}
	}
	return false
}

// ask prints question and reads a line of input, unless ctx ends first, as
// it does when Ctrl-C is pressed at the question.
func ask(ctx context.Context, in *bufio.Reader, out io.Writer, question string) (string, error) {
	fmt.Fprint(out, question)
	type reply struct {
		line string
		err  error
	}
	replied := make(chan reply, 1)
	go func() {
		line, err := in.ReadString('\n')
		replied <- reply{line, err}
	}()
	select {
	case <-ctx.Done():
		fmt.Fprintln(out)
		return "", context.Cause(ctx)
	case r := <-replied:
		return r.line, r.err
	}
}

// stoppedOr is the exit status for a command that did not finish: the
// signal's when interragate was asked to stop, else code.
func stoppedOr(ctx context.Context, code int) exitError {
	var st *tfexec.Stopped
	if errors.As(context.Cause(ctx), &st) {
		return exitError(st.ExitCode())
	}
	return exitError(code)
}

// applies reports whether applying the plan would do anything. Terraform
// 1.8 and later say so in the plan. For earlier versions, changes to
// outputs count as well as to resources, and so does drift, which is what
// a -refresh-only plan exists to record in the state.
func applies(r *model.Report) bool {
	if a := r.Plan.Applyable; a != nil {
		return *a
	}
	return r.Summary.Total() > 0 || r.Summary.Outputs > 0 || len(r.Drift) > 0
}

// describeTotal says what the plan does, briefly: "3 changes".
func describeTotal(r *model.Report) string {
	s := r.Summary
	switch {
	case s.Total() > 0:
		return plural(s.Total(), "change")
	case s.Outputs > 0:
		return plural(s.Outputs, "output change")
	case len(r.Drift) > 0:
		return plural(len(r.Drift), "change") + " made outside " + r.Plan.ToolName()
	}
	return "this plan"
}
