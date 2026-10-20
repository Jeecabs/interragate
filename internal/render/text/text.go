// Package text renders a report for the terminal.
package text

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/rules"
)

// Options control terminal output.
type Options struct {
	Color bool
	Width int
	// All includes cosmetic updates and resources with no changes.
	All bool
	// Diffs prints attribute diffs under each change.
	Diffs bool
	// MinSeverity hides findings below it.
	MinSeverity model.Severity
	// Hint is printed at the end, e.g. how to open the report.
	Hint string
}

type printer struct {
	w    io.Writer
	opts Options
	err  error
	// causes are the causes of the change whose diff is being printed, by
	// top-level attribute, so an unknown value can say where it comes from.
	causes map[string]model.Cause
}

func (p *printer) printf(format string, args ...any) {
	if p.err != nil {
		return
	}
	_, p.err = io.WriteString(p.w, safeForTerminal(fmt.Sprintf(format, args...)))
}

// sgr matches a colour or weight sequence, the only escape interragate
// writes.
var sgr = regexp.MustCompile(`^\x1b\[[0-9;]*m`)

// safeForTerminal keeps text from a plan, a configuration or a scanner
// from driving the terminal: an escape sequence in a for_each key or a
// provisioner's command could move the cursor, clear the screen or set
// the window title. Colour sequences stay; every other control character
// but newline and tab is shown as U+FFFD.
func safeForTerminal(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 && r != '\n' && r != '\t' || r == 0x7f || r >= 0x80 && r < 0xa0 }) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if m := sgr.FindString(s[i:]); m != "" {
			b.WriteString(m)
			i += len(m)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f || r >= 0x80 && r < 0xa0 {
			r = utf8.RuneError
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

// ANSI styles.
const (
	reset   = "\x1b[0m"
	bold    = "\x1b[1m"
	dim     = "\x1b[2m"
	red     = "\x1b[31m"
	green   = "\x1b[32m"
	yellow  = "\x1b[33m"
	blue    = "\x1b[34m"
	magenta = "\x1b[35m"
	cyan    = "\x1b[36m"
	inverse = "\x1b[7m"
)

func (p *printer) style(s string, codes ...string) string {
	if !p.opts.Color || s == "" {
		return s
	}
	return strings.Join(codes, "") + s + reset
}

// code renders the narrative markup: `x` becomes bold cyan.
func (p *printer) code(s string) string {
	parts := strings.Split(s, "`")
	if len(parts) < 3 {
		return s
	}
	var b strings.Builder
	for i, part := range parts {
		if i%2 == 1 && i < len(parts)-1 {
			b.WriteString(p.style(part, cyan))
		} else {
			b.WriteString(part)
		}
	}
	return b.String()
}

// Render prints the full report.
func Render(w io.Writer, r *model.Report, opts Options) error {
	if opts.Width <= 0 {
		opts.Width = 100
	}
	p := &printer{w: w, opts: opts}
	p.header(r)
	p.narrative(r)
	p.apply(r)
	p.consumers(r)
	p.changes(r)
	p.findings(r, opts.MinSeverity)
	p.outputs(r)
	if opts.Hint != "" {
		p.printf("\n%s\n", p.style(opts.Hint, dim))
	}
	return p.err
}

// RenderFindings prints only the verdict and findings, for
// `interragate check`.
func RenderFindings(w io.Writer, r *model.Report, opts Options) error {
	if opts.Width <= 0 {
		opts.Width = 100
	}
	p := &printer{w: w, opts: opts}
	p.header(r)
	p.findings(r, opts.MinSeverity)
	return p.err
}

// RenderChange prints everything about one resource instance.
func RenderChange(w io.Writer, r *model.Report, ch *model.Change, opts Options) error {
	if opts.Width <= 0 {
		opts.Width = 100
	}
	p := &printer{w: w, opts: opts}
	sym, color := symbol(ch)
	p.printf("%s %s  %s\n", p.style(sym, color, bold), p.style(ch.Address, bold), p.style(ch.Label(), color))
	if ch.Noun != "" {
		line := rules.Article(ch.Noun)
		if ch.Location != nil {
			line += fmt.Sprintf(" · %s:%d", ch.Location.File, ch.Location.Line)
		}
		if len(ch.Owners) > 0 {
			line += " · owned by " + rules.List(ch.Owners, 4)
		}
		p.printf("%s\n", p.style(line, dim))
	}
	if ch.Why != "" {
		p.wrap(p.code(ch.Why), "  ", "  ")
	}
	if ch.Edit != nil {
		p.edit(r, ch.Edit)
	}
	if len(ch.Deploys) > 0 {
		p.printf("\n%s\n", p.style("Deploys", bold))
		for _, d := range ch.Deploys {
			to := d.To
			if d.Unknown {
				to = "(known after apply)"
			}
			line := d.What + " " + to
			switch {
			case d.What == "code":
				line = "new code"
			case d.From != "":
				line = d.What + " " + p.style(d.From, dim) + " → " + to
			}
			p.printf("  %s %s\n", p.style("↑", green, bold), line)
		}
	}
	p.access(ch.Access)
	if len(ch.Causes) > 0 {
		p.printf("\n%s\n", p.style("Because of", bold))
		for _, c := range ch.Causes {
			via := ""
			if c.Via != "" {
				via = "." + c.Via
			}
			p.printf("  %s ← %s%s %s\n", c.Attribute, c.Resource, via, p.style("("+causeVerb(c.Action)+")", dim))
		}
		if start := startsWith(r, ch); start != "" {
			p.wrap(p.code(start), "  ", "  ")
		}
	}
	var fs []*model.Finding
	for _, f := range r.Findings {
		for _, id := range ch.Findings {
			if f.ID == id {
				fs = append(fs, f)
			}
		}
	}
	if len(fs) > 0 {
		p.printf("\n%s\n", p.style("Findings", bold))
		for _, f := range fs {
			p.finding(f)
		}
	}
	p.applySteps(r, ch)
	if ch.Diff != nil && ch.Action != model.NoOp {
		p.printf("\n%s\n", p.style("Diff", bold))
		p.changeDiff(ch, "  ")
	}
	if ch.Dependents > 0 {
		depend := fmt.Sprintf("%d resources depend on this one.", ch.Dependents)
		if ch.Dependents == 1 {
			depend = "1 resource depends on this one."
		}
		p.printf("\n%s\n", p.style(depend, dim))
	}
	return p.err
}

// access prints what a change to a policy or firewall allows that it did
// not, and what it stops allowing.
func (p *printer) access(lines []model.AccessLine) {
	if len(lines) == 0 {
		return
	}
	iam, net := false, false
	for _, l := range lines {
		iam = iam || l.Kind == "iam"
		net = net || l.Kind == "network"
	}
	title := "Network access"
	switch {
	case iam && net:
		title = "Access"
	case iam:
		title = "Permissions"
	}
	p.printf("\n%s\n", p.style(title, bold))
	for _, l := range lines {
		op, color := "+", green
		if l.Op == "-" {
			op, color = "−", red
		}
		text := p.code(l.Text)
		if l.Wide {
			text = p.style(text, bold)
		}
		p.wrap(text, "  "+p.style(op, color, bold)+" ", "    ")
	}
}

// startsWith follows a change's causes back to the edit they begin with:
// "It starts with your edit to `var.azs`, in `main.tf`, through
// `module.network.aws_subnet.private[1]`."
func startsWith(r *model.Report, ch *model.Change) string {
	byAddr := map[string]*model.Change{}
	byResource := map[string]*model.Change{}
	for _, c := range r.Changes {
		byAddr[c.Address] = c
		if byResource[c.Resource] == nil {
			byResource[c.Resource] = c
		}
	}
	seen := map[string]bool{ch.Address: true}
	var through []string
	cur := ch
	for len(cur.Causes) > 0 {
		k := cur.Causes[0]
		next := byAddr[k.Address]
		if next == nil {
			next = byResource[k.Resource]
		}
		if next == nil || seen[next.Address] {
			return ""
		}
		seen[next.Address] = true
		through = append(through, next.Address)
		cur = next
		if cur.Edit != nil {
			break
		}
	}
	e := cur.Edit
	if e == nil || cur == ch {
		return ""
	}
	what := "`" + cur.Address + "`"
	steps := through[:len(through)-1]
	switch e.Kind {
	case "module":
		what, steps = "the call to `"+e.Module+"`", through
	case "input":
		what, steps = "`"+e.Input+"`", through
	case "provider":
		what, steps = "the provider block of `"+cur.Address+"`", through
	}
	text := fmt.Sprintf("It starts with your edit to %s, in `%s`", what, e.File)
	if len(steps) > 0 {
		quoted := make([]string, len(steps))
		for i, a := range steps {
			quoted[i] = "`" + a + "`"
		}
		text += ", through " + rules.List(quoted, 3)
	}
	return text + "."
}

// edit prints the lines of the configuration behind a change that differ
// from the git base.
func (p *printer) edit(r *model.Report, e *model.Edit) {
	since := "since the last commit"
	if r.Git != nil && r.Git.Base != "HEAD" {
		since = "since `" + r.Git.Base + "`"
	}
	removed := true
	for _, l := range e.Lines {
		removed = removed && l.Op == "-"
	}
	var what string
	switch {
	case e.Kind == "module" && removed:
		what = fmt.Sprintf("The call to `%s` was removed from %s %s.", e.Module, e.File, since)
	case e.Kind == "module":
		what = fmt.Sprintf("The call to `%s` changed in %s %s; this block did not.", e.Module, e.File, since)
	case e.Kind == "input":
		what = fmt.Sprintf("It reads `%s`, which changed in %s %s; its own block did not.", e.Input, e.File, since)
	case e.Kind == "provider":
		what = fmt.Sprintf("Its provider's block changed in %s %s; its own block did not.", e.File, since)
	case e.Kind == "added":
		what = fmt.Sprintf("New in %s %s.", e.File, since)
	case e.Kind == "removed":
		what = fmt.Sprintf("Removed from %s %s.", e.File, since)
	default:
		what = fmt.Sprintf("Changed in %s %s.", e.File, since)
	}
	p.printf("\n%s\n", p.style("Your edit", bold))
	p.wrap(p.code(what), "  ", "  ")
	room := max(p.opts.Width-4, 20)
	for _, l := range e.Lines {
		text := strings.Map(printable, l.Text)
		if utf8.RuneCountInString(text) > room {
			text = string([]rune(text)[:room-1]) + "…"
		}
		color := green
		if l.Op == "-" {
			color = red
		}
		if text == "" {
			p.printf("  %s\n", p.style(l.Op, color, bold))
			continue
		}
		p.printf("  %s %s\n", p.style(l.Op, color, bold), p.style(text, color))
	}
	if e.Omitted > 0 {
		p.printf("  %s\n", p.style(fmt.Sprintf("# (%d more changed lines)", e.Omitted), dim))
	}
}

// printable keeps a line from the configuration from moving the cursor or
// changing colours when it is printed: control characters other than tabs
// are shown as U+FFFD.
func printable(r rune) rune {
	if r != '\t' && (r < 0x20 || r == 0x7f) {
		return utf8.RuneError
	}
	return r
}

func (p *printer) header(r *model.Report) {
	tool := r.Plan.Tool
	if tool == "" {
		tool = "terraform"
	}
	meta := []string{}
	if r.Plan.Dir != "" {
		meta = append(meta, r.Plan.Dir)
	}
	meta = append(meta, tool+" "+r.Plan.Version)
	if ws := r.Plan.Workspace; ws != "" && ws != "default" {
		meta = append(meta, "workspace "+ws)
	}
	if r.Plan.Source != "" && r.Plan.Source != r.Plan.Dir {
		meta = append(meta, r.Plan.Source)
	}
	p.printf("%s  %s\n\n", p.style("interragate", bold), p.style(strings.Join(meta, " · "), dim))

	// The verdict reads as a sentence: the risk in its colour, then what
	// the plan does. With nothing to apply the headline says so alone.
	if r.Risk.Level == model.None {
		p.wrap(p.style(p.code(r.Risk.Headline), bold), "", "")
	} else {
		label := levelName(r.Risk.Level)
		label = strings.ToUpper(label[:1]) + label[1:] + "."
		p.wrap(p.style(label, levelColor(r.Risk.Level), bold)+" "+p.style(p.code(r.Risk.Headline), bold), "", "")
	}
	p.printf("\n%s\n", p.counts(r.Summary))
}

func levelName(s model.Severity) string {
	switch s {
	case model.None:
		return "no changes"
	case model.Info:
		return "low risk"
	}
	return s.String() + " risk"
}

func levelColor(s model.Severity) string {
	switch s {
	case model.Critical, model.High:
		return red
	case model.Medium:
		return yellow
	case model.Low:
		return blue
	}
	return green
}

func (p *printer) counts(s model.Summary) string {
	var parts []string
	add := func(n int, sym, label, color string) {
		if n > 0 {
			parts = append(parts, p.style(fmt.Sprintf("%s%d", sym, n), color, bold)+" "+label)
		}
	}
	add(s.Create, "+", "create", green)
	add(s.Update, "~", "update", yellow)
	add(s.Replace, "-/+", "replace", magenta)
	add(s.Delete, "-", "destroy", red)
	add(s.Move, "→", "move", blue)
	add(s.Import, "↓", "import", blue)
	add(s.Forget, "⊘", "forget", blue)
	add(s.Read, "<=", "read", cyan)
	if len(parts) == 0 {
		parts = append(parts, "no resource changes")
	}
	line := strings.Join(parts, "   ")
	var extra []string
	if s.Cosmetic > 0 {
		extra = append(extra, fmt.Sprintf("%d cosmetic", s.Cosmetic))
	}
	if s.Drifted > 0 {
		extra = append(extra, fmt.Sprintf("%d drifted", s.Drifted))
	}
	if s.Outputs > 0 {
		extra = append(extra, fmt.Sprintf("%d output changes", s.Outputs))
	}
	if len(extra) > 0 {
		line += "     " + p.style(strings.Join(extra, " · "), dim)
	}
	return line
}

// apply prints how long the apply should take and what goes missing
// meanwhile, under the counts, since both describe the plan as a whole.
// apply says how the apply should go: where it fails, how long it takes and
// what goes missing meanwhile.
func (p *printer) apply(r *model.Report) {
	if r.Apply == nil || len(r.Apply.Narrative) == 0 {
		return
	}
	p.printf("\n%s\n", p.style("When you apply", bold))
	for _, s := range r.Apply.Narrative {
		bullet := p.style("●", levelColor(s.Severity))
		if s.Severity <= model.Info {
			bullet = p.style("·", dim)
		}
		p.wrap(p.code(s.Text), "  "+bullet+" ", "    ")
	}
}

// consumers says what the plan does to each service outside Terraform the
// configuration file declares, the worst first, then which it leaves alone,
// which the configuration's settings leave out, and which it has nothing
// for.
func (p *printer) consumers(r *model.Report) {
	if len(r.Consumers) == 0 {
		return
	}
	groups := model.Grouped(r.Consumers)
	p.printf("\n%s  %s\n", p.style("Outside Terraform", bold), p.style("services you declared, which interragate cannot see", dim))
	for _, c := range groups[model.StandingAffected] {
		meta := c.Owners
		if c.Platform != "" {
			meta = append([]string{c.Platform}, c.Owners...)
		}
		p.printf("  %s  %s\n", p.style(clean(string(c.Name)), bold), p.style(clean(strings.Join(meta, " · ")), dim))
		for _, e := range c.Effects {
			bullet := p.style("●", levelColor(e.Severity))
			if e.Severity <= model.Info {
				bullet = p.style("·", dim)
			}
			p.wrap(p.code(e.Text), "    "+bullet+" ", "      ")
		}
	}
	note := func(text string) { p.wrap(p.code(text), "  "+p.style("·", dim)+" ", "    ") }
	if cs := groups[model.StandingUnaffected]; len(cs) > 0 {
		note("Not affected: " + serviceNames(cs) + ".")
	}
	if cs := groups[model.StandingHidden]; len(cs) > 0 {
		note("Left out by disable or ignore: " + serviceNames(cs) + ".")
	}
	if missing := missingUses(r.Consumers); missing != "" {
		note("Declared to use what this plan does not have: " + missing + ".")
	}
}

// serviceNames lists services by name for a note.
func serviceNames(cs []*model.Consumer) string {
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = clean(string(c.Name))
	}
	return strings.Join(names, ", ")
}

// missingUses lists, by service, what each is declared to use that the
// plan does not have: "reporting (`aws_db_instance.ordres`)".
func missingUses(cs []*model.Consumer) string {
	var parts []string
	for _, c := range cs {
		if len(c.Missing) == 0 {
			continue
		}
		uses := make([]string, len(c.Missing))
		for i, m := range c.Missing {
			uses[i] = "`" + strings.ReplaceAll(m, "`", "'") + "`"
		}
		parts = append(parts, clean(string(c.Name))+" ("+strings.Join(uses, ", ")+")")
	}
	return strings.Join(parts, "; ")
}

func (p *printer) narrative(r *model.Report) {
	if len(r.Narrative) == 0 {
		return
	}
	p.printf("\n%s\n", p.style("What matters", bold))
	for _, s := range r.Narrative {
		bullet := p.style("●", levelColor(s.Severity))
		if s.Severity <= model.Info {
			bullet = p.style("·", dim)
		}
		p.wrap(p.code(s.Text), "  "+bullet+" ", "    ")
	}
}

func symbol(ch *model.Change) (string, string) {
	switch ch.Action {
	case model.Create:
		if ch.Importing {
			return "↓", blue
		}
		return "+", green
	case model.Update:
		return "~", yellow
	case model.Replace:
		if ch.Order == "create-first" {
			return "+/-", magenta
		}
		return "-/+", magenta
	case model.Delete:
		return "-", red
	case model.Read:
		return "<=", cyan
	case model.Forget:
		return "⊘", blue
	case model.NoOp:
		if ch.Importing {
			return "↓", blue
		}
		if ch.Moved {
			return "→", blue
		}
	}
	return " ", dim
}

func (p *printer) changes(r *model.Report) {
	type group struct {
		module  string
		changes []*model.Change
	}
	var groups []*group
	index := map[string]*group{}
	hidden := 0
	for _, ch := range r.Changes {
		if !p.opts.All && (ch.Action == model.NoOp && !ch.Moved && !ch.Importing || ch.Cosmetic != "") {
			if ch.Cosmetic != "" {
				hidden++
			}
			continue
		}
		g := index[ch.Module]
		if g == nil {
			g = &group{module: ch.Module}
			index[ch.Module] = g
			groups = append(groups, g)
		}
		g.changes = append(g.changes, ch)
	}
	if len(groups) == 0 && hidden == 0 {
		return
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].module < groups[j].module })
	p.printf("\n%s\n", p.style("Changes", bold))
	width := 0
	for _, g := range groups {
		for _, ch := range g.changes {
			width = max(width, utf8.RuneCountInString(local(ch)))
		}
	}
	width = min(width, 56)
	for _, g := range groups {
		name := g.module
		if name == "" {
			name = "root module"
		}
		p.printf("  %s\n", p.style(name, dim))
		for _, ch := range g.changes {
			sym, color := symbol(ch)
			addr := local(ch)
			pad := max(0, width-utf8.RuneCountInString(addr))
			sev := sevTag(model.None)
			if ch.Severity >= model.Medium {
				sev = p.style(sevTag(ch.Severity), levelColor(ch.Severity), bold)
			}
			label := fmt.Sprintf("%-8s", ch.Label())
			desc := ch.Summary
			if ch.Edit != nil {
				mark := "edited"
				switch ch.Edit.Kind {
				case "module":
					mark = "module call edited"
				case "input":
					mark = ch.Edit.Input + " edited"
				case "provider":
					mark = "provider edited"
				}
				desc = strings.TrimSuffix(mark+" · "+desc, " · ")
			}
			room := p.opts.Width - (4 + 3 + 1 + width + 2 + 9 + 9)
			if room > 10 && utf8.RuneCountInString(desc) > room {
				desc = string([]rune(desc)[:room-1]) + "…"
			}
			p.printf("    %s %s%s  %s %s %s\n", p.style(fmt.Sprintf("%3s", sym), color, bold), addr, strings.Repeat(" ", pad), p.style(label, color), sev, p.style(desc, dim))
			if p.opts.Diffs && ch.Diff != nil && ch.Action != model.NoOp {
				p.changeDiff(ch, "          ")
			}
		}
	}
	if hidden > 0 {
		p.printf("  %s\n", p.style(fmt.Sprintf("%d cosmetic updates hidden (tags or formatting only); --all shows them", hidden), dim))
	}
}

// sevTag is a change's severity as a word, padded so the column lines up.
func sevTag(s model.Severity) string {
	switch s {
	case model.Critical, model.High, model.Medium:
		return fmt.Sprintf("%-8s", s.String())
	}
	return strings.Repeat(" ", 8)
}

func local(ch *model.Change) string {
	if ch.Module == "" {
		return ch.Address
	}
	return strings.TrimPrefix(ch.Address, ch.Module+".")
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}

func (p *printer) findings(r *model.Report, min model.Severity) {
	var fs []*model.Finding
	for _, f := range r.Findings {
		if f.Severity >= min {
			fs = append(fs, f)
		}
	}
	if len(fs) == 0 {
		if len(r.Findings) == 0 && r.Summary.Total() > 0 {
			p.printf("\n%s\n", p.style("No findings.", dim))
		}
		return
	}
	p.printf("\n%s\n", p.style(fmt.Sprintf("Findings (%d)", len(fs)), bold))
	for _, f := range fs {
		p.finding(f)
	}
	if n := len(r.Suppressed); n > 0 {
		p.printf("  %s\n", p.style(fmt.Sprintf("%d suppressed by .interragate.yaml", n), dim))
	}
}

func (p *printer) finding(f *model.Finding) {
	name := f.Severity.String()
	sev := fmt.Sprintf("%-9s", strings.ToUpper(name[:1])+name[1:])
	p.printf("\n  %s %s  %s\n", p.style(sev, levelColor(f.Severity), bold), p.style(f.Title, bold), p.style(f.Rule, dim))
	p.wrap(p.code(f.Message), "            ", "            ")
	if f.Remediation != "" {
		p.wrap(p.style("→ ", green)+p.code(f.Remediation), "            ", "              ")
	}
	if f.Snippet != "" {
		for _, line := range strings.Split(f.Snippet, "\n") {
			p.printf("              %s\n", p.style(line, cyan))
		}
	}
}

func (p *printer) outputs(r *model.Report) {
	var changed []*model.Output
	for _, o := range r.Outputs {
		if o.Action != model.NoOp {
			changed = append(changed, o)
		}
	}
	if len(changed) == 0 {
		return
	}
	p.printf("\n%s\n", p.style("Outputs", bold))
	for _, o := range changed {
		sym, color := "~", yellow
		switch o.Action {
		case model.Create:
			sym, color = "+", green
		case model.Delete:
			sym, color = "-", red
		}
		val := ""
		if o.Diff != nil {
			val = p.inline(o.Diff)
		}
		p.wrap(p.style(sym, color, bold)+" "+o.Name+" "+val, "  ", "      ")
	}
}

// inline renders a leaf diff on one line.
func (p *printer) inline(n *diff.Node) string {
	switch {
	case n.Sensitive:
		return p.style("(sensitive)", dim)
	case n.Kind == diff.Added && n.Unknown:
		return p.style("(known after apply)", dim)
	case n.Kind == diff.Added:
		return p.value(n.After, n, false)
	case n.Kind == diff.Removed:
		return p.style(p.value(n.Before, n, true), red)
	case n.Unknown:
		return p.value(n.Before, n, true) + " → " + p.style("(known after apply)", dim)
	case n.Kind == diff.Unchanged:
		return p.style(p.value(n.After, n, false), dim)
	}
	return p.value(n.Before, n, true) + " → " + p.value(n.After, n, false)
}

func (p *printer) value(v any, n *diff.Node, before bool) string {
	if len(n.Children) > 0 || n.Lines != nil {
		return "{…}"
	}
	s := diff.String(v)
	if max := p.opts.Width/2 - 4; max > 20 && utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max-1]) + "…"
	}
	return s
}

// applySteps says when a change happens in the simulated apply, how long it
// typically takes, and whether it is expected to fail.
func (p *printer) applySteps(r *model.Report, ch *model.Change) {
	if r.Apply == nil {
		return
	}
	var lines []string
	verbs := map[string]string{"create": "create", "update": "update", "destroy": "destroy", "read": "read"}
	for _, st := range r.Apply.Steps {
		if st.Address != ch.Address {
			continue
		}
		line := fmt.Sprintf("%s–%s  %s", model.Clock(st.Start), model.Clock(st.Start+st.Duration), verbs[st.Op])
		if st.Deposed {
			line += " the old object"
		}
		if st.Known {
			line += ", typically " + model.Roughly(st.Duration)
		}
		if st.Fails != "" {
			line += p.style("  expected to fail", red)
		}
		lines = append(lines, line)
	}
	for _, g := range r.Apply.Gaps {
		if g.Address == ch.Address {
			lines = append(lines, "Does not exist for "+model.Roughly(g.To-g.From)+".")
		}
	}
	if len(lines) == 0 {
		return
	}
	p.printf("\n%s\n", p.style("In the apply", bold))
	for _, l := range lines {
		p.printf("  %s\n", l)
	}
}

// changeDiff prints a change's diff, with the causes of its unknown values.
func (p *printer) changeDiff(ch *model.Change, indent string) {
	p.causes = map[string]model.Cause{}
	for _, c := range ch.Causes {
		p.causes[c.Attribute] = c
	}
	p.diffNode(ch.Diff, indent, true)
	p.causes = nil
}

// diffNode prints a diff the way terraform plan does, with sigils.
func (p *printer) diffNode(root *diff.Node, indent string, top bool) {
	width := 0
	for _, c := range root.Children {
		width = max(width, utf8.RuneCountInString(c.Key))
	}
	for _, c := range root.Children {
		p.diffChild(c, indent, width, root.Type == "list", top)
	}
	if root.Hidden > 0 {
		what := "attribute"
		if root.Type == "list" {
			what = "element"
		}
		if root.Hidden != 1 {
			what += "s"
		}
		p.printf("%s  %s\n", indent, p.style(fmt.Sprintf("# (%d unchanged %s hidden)", root.Hidden, what), dim))
	}
}

func sigil(k diff.Kind) (string, string) {
	switch k {
	case diff.Added:
		return "+", green
	case diff.Removed:
		return "-", red
	case diff.Changed:
		return "~", yellow
	}
	return " ", dim
}

func (p *printer) diffChild(n *diff.Node, indent string, width int, inList, top bool) {
	sym, color := sigil(n.Kind)
	key := ""
	if !inList {
		key = n.Key + strings.Repeat(" ", max(0, width-utf8.RuneCountInString(n.Key))) + " = "
	}
	forces := ""
	if n.Forces {
		forces = p.style("  # forces replacement", red)
	}
	// An unknown value says which change it waits for, as a comment.
	if c, ok := p.causes[n.Key]; ok && top && n.Unknown {
		from := c.Resource
		if c.Via != "" {
			from += "." + c.Via
		}
		lead := "  # from "
		if forces != "" {
			lead = "; from "
		}
		forces += p.style(lead+from+" ("+causeVerb(c.Action)+")", dim)
	}
	prefix := indent + p.style(sym, color, bold) + " " + key
	switch {
	case n.Sensitive:
		p.printf("%s%s%s\n", prefix, p.style("(sensitive)", dim), forces)
	case n.Lines != nil:
		p.printf("%s<<-EOT%s\n", prefix, forces)
		for _, l := range n.Lines {
			switch l.Op {
			case "skip":
				p.printf("%s      %s\n", indent, p.style(fmt.Sprintf("# (%d unchanged lines hidden)", l.Skip), dim))
			case "note":
				p.printf("%s      %s\n", indent, p.style("# ("+l.Text+")", dim))
			case "+":
				p.printf("%s    %s %s\n", indent, p.style("+", green, bold), p.style(l.Text, green))
			case "-":
				p.printf("%s    %s %s\n", indent, p.style("-", red, bold), p.style(l.Text, red))
			default:
				p.printf("%s      %s\n", indent, l.Text)
			}
		}
		p.printf("%s  EOT\n", indent)
	case len(n.Children) > 0:
		open, close := "{", "}"
		if n.Type == "list" {
			open, close = "[", "]"
		}
		if n.Encoding == "json" {
			open, close = "jsonencode("+open, close+")"
		}
		tail := ""
		if n.Unknown {
			tail = " → " + p.style("(known after apply)", dim)
		}
		p.printf("%s%s%s\n", prefix, open, forces)
		p.diffNode(n, indent+"    ", false)
		p.printf("%s  %s%s\n", indent, close, tail)
	default:
		p.printf("%s%s%s\n", prefix, p.inline(n), forces)
	}
}

func causeVerb(a model.Action) string {
	switch a {
	case model.Create:
		return "new"
	case model.Replace:
		return "replaced"
	case model.Update:
		return "updated"
	case model.Read:
		return "read during apply"
	case model.Delete:
		return "destroyed"
	}
	return string(a)
}

// wrap prints text wrapped to the terminal width with a first-line prefix
// and a continuation indent.
func (p *printer) wrap(s, first, rest string) {
	p.wrapAt(s, first, rest, visibleLen(first))
}

func (p *printer) wrapAt(s, first, rest string, firstCol int) {
	width := p.opts.Width
	words := strings.Fields(s)
	if len(words) == 0 {
		p.printf("%s\n", first)
		return
	}
	line := first
	col := firstCol
	start := true
	for _, w := range words {
		wl := visibleLen(w)
		if !start && col+1+wl > width {
			p.printf("%s\n", line)
			line = rest
			col = visibleLen(rest)
			start = true
		}
		if !start {
			line += " "
			col++
		}
		line += w
		col += wl
		start = false
	}
	p.printf("%s\n", line)
}

// visibleLen counts runes, ignoring ANSI escapes.
func visibleLen(s string) int {
	n := 0
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		case r == '\x1b':
			inEsc = true
		default:
			n++
		}
	}
	return n
}

// clean keeps a name from the configuration file on one line.
func clean(s string) string { return strings.Join(strings.Fields(s), " ") }
