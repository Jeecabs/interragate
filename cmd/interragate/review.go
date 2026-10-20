package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/render/text"
)

// reviewItem is one change that needs a look, and what the reviewer said.
type reviewItem struct {
	ch       *model.Change
	reviewed bool
	note     string
}

// needsLook matches the report's review checklist: destroys, replacements,
// and anything with a finding of medium or worse.
func needsLook(ch *model.Change) bool {
	if ch.Action == model.NoOp && !ch.Moved && !ch.Importing {
		return false
	}
	return ch.Action == model.Delete || ch.Action == model.Replace || ch.Severity >= model.Medium
}

// reviewCmd walks through the changes that need a look, worst first, in the
// terminal: each in full, with its diff, then a question. Enter marks it
// reviewed, any longer answer is kept as a note, n leaves it unreviewed and
// q stops. The answers come out as the same Markdown checklist the report's
// review produces, for a pull request or a change ticket.
func reviewCmd(ctx context.Context, o *options, stdout, stderr io.Writer) error {
	if len(o.args) > 1 {
		return errors.New("interragate review takes one plan at most; without one it plans here first")
	}
	if len(o.args) == 1 && (o.noInit || o.verbose || len(o.extra) > 0) {
		return errors.New("interragate review reads the plan it is given, so --no-init, --verbose and plan arguments after -- only apply without one")
	}
	if !stdinIsTerminal() {
		return errors.New("interragate review asks a question per change, so it needs a terminal")
	}
	var r *model.Report
	var err error
	if len(o.args) == 1 {
		r, err = loadReport(ctx, o, o.args[0])
	} else {
		r, err = planHere(ctx, o, stderr)
	}
	if err != nil {
		return err
	}
	var items []*reviewItem
	for _, ch := range r.Changes {
		if needsLook(ch) {
			items = append(items, &reviewItem{ch: ch})
		}
	}
	if len(items) == 0 {
		fmt.Fprintln(stdout, "Nothing in this plan needs a look: no destroys, no replacements and no findings of medium or worse.")
		return nil
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].ch, items[j].ch
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		if a.Action.Rank() != b.Action.Rank() {
			return a.Action.Rank() > b.Action.Rank()
		}
		return a.Address < b.Address
	})

	color := !o.noColor && os.Getenv("NO_COLOR") == "" && isTerminal(stdout)
	topts := text.Options{Color: color, Width: termWidth(stdout), Diffs: true}
	in := bufio.NewReader(confirmInput)
	fmt.Fprintf(stdout, "%s need a look before this plan is applied. Enter marks one reviewed, a note says what you checked, n leaves it for later, q stops.\n", plural(len(items), "change"))
	for i, it := range items {
		fmt.Fprintf(stdout, "\n%s\n\n", dimmed(fmt.Sprintf("%d of %d", i+1, len(items)), color))
		if err := text.RenderChange(stdout, r, it.ch, topts); err != nil {
			return err
		}
		line, err := ask(ctx, in, stdout, "\nReviewed? ")
		if ctx.Err() != nil {
			// Ctrl-C abandons the review; q is the way to stop and keep it.
			fmt.Fprintln(stdout, "Stopped without writing the review notes.")
			return stoppedOr(ctx, 1)
		}
		answer := strings.TrimSpace(line)
		if err != nil && answer == "" {
			break
		}
		switch strings.ToLower(answer) {
		case "", "y", "yes":
			it.reviewed = true
		case "n", "no":
		case "q", "quit":
			goto done
		default:
			it.reviewed, it.note = true, answer
		}
	}
done:
	notes := reviewMarkdown(r, items)
	if o.out != "" {
		if err := os.WriteFile(o.out, []byte(notes), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "\nWrote the review notes to %s.\n", o.out)
		return nil
	}
	fmt.Fprintf(stdout, "\n%s", notes)
	return nil
}

// planHere plans the current directory (or -C) as interragate does.
func planHere(ctx context.Context, o *options, stderr io.Writer) (*model.Report, error) {
	dir := o.dir
	if dir == "" {
		dir = "."
	}
	if !hasConfigFiles(dir) {
		return nil, noConfigError(dir)
	}
	runner, err := newRunner(ctx, o, dir, stderr)
	if err != nil {
		return nil, err
	}
	pl := &planner{o: o, runner: runner, dir: dir, stderr: stderr, progress: isTerminal(stderr) && !o.verbose}
	r, _, err := planTemp(ctx, pl)
	return r, err
}

// reviewMarkdown is the checklist the report's "Copy review notes" makes.
func reviewMarkdown(r *model.Report, items []*reviewItem) string {
	findings := map[string]*model.Finding{}
	for _, f := range r.Findings {
		findings[f.ID] = f
	}
	var b strings.Builder
	head := "### Plan review"
	if r.Plan.Dir != "" {
		head += ": " + r.Plan.Dir
	}
	meta := []string{r.Plan.ToolName() + " " + r.Plan.Version}
	if ws := r.Plan.Workspace; ws != "" && ws != "default" {
		meta = append(meta, "workspace "+ws)
	}
	if t, err := time.Parse(time.RFC3339, r.Plan.Timestamp); err == nil {
		meta = append(meta, "planned "+t.Local().Format("Jan 2, 2006, 3:04 PM"))
	}
	done := 0
	for _, it := range items {
		if it.reviewed {
			done++
		}
	}
	fmt.Fprintf(&b, "%s\n\n_%s. Reviewed %d of %s that need a look._\n\n", head, strings.Join(meta, ", "), done, plural(len(items), "change"))
	for _, it := range items {
		ch := it.ch
		mark := " "
		if it.reviewed {
			mark = "x"
		}
		label := ch.Label()
		label = strings.ToUpper(label[:1]) + label[1:]
		line := fmt.Sprintf("- [%s] %s `%s`", mark, label, ch.Address)
		if ch.Severity >= model.Low {
			line += " (" + ch.Severity.String() + ")"
		}
		why := ""
		for _, id := range ch.Findings {
			if f := findings[id]; f != nil {
				why = f.Title
				break
			}
		}
		if why == "" && ch.Why != "" {
			why = strings.TrimSuffix(strings.ReplaceAll(firstSentenceOf(ch.Why), "`", ""), ".")
		}
		if why != "" {
			line += ": " + why
		}
		if it.note != "" {
			line += "  \n  " + it.note
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func firstSentenceOf(s string) string {
	inCode := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '`':
			inCode = !inCode
		case '.':
			if !inCode && (i+1 == len(s) || s[i+1] == ' ') {
				return s[:i+1]
			}
		}
	}
	return s
}

func dimmed(s string, color bool) string {
	if !color {
		return s
	}
	return "\x1b[2m" + s + "\x1b[0m"
}
