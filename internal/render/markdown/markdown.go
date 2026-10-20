// Package markdown renders a report as GitHub-flavoured Markdown, sized for
// a pull request comment.
package markdown

import (
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/render/mermaid"
)

// MaxLen keeps the comment under GitHub's 65,536 character limit.
const MaxLen = 60000

// Options control the Markdown output.
type Options struct {
	// Title replaces the default heading, e.g. to name the stack.
	Title string
	// ReportURL links to the full HTML report, e.g. a CI artifact.
	ReportURL string
}

var dots = map[model.Severity]string{
	model.Critical: "🔴", model.High: "🟠", model.Medium: "🟡", model.Low: "🔵", model.Info: "⚪", model.None: "⚪",
}

// Render writes the comment. Sections are added in order of importance
// and shortened, then dropped, to stay under MaxLen.
func Render(w io.Writer, r *model.Report, opts Options) error {
	var b strings.Builder
	title := opts.Title
	if title == "" {
		title = "interragate"
		if r.Plan.Dir != "" {
			title += " · " + r.Plan.Dir
		}
		if ws := r.Plan.Workspace; ws != "" && ws != "default" {
			title += " · " + ws
		}
	}
	fmt.Fprintf(&b, "### %s %s: %s\n\n", dots[r.Risk.Level], escapeText(title), riskName(r.Risk.Level))
	fmt.Fprintf(&b, "**%s**\n\n", escapeText(r.Risk.Headline))
	b.WriteString(counts(r.Summary) + "\n\n")

	if len(r.Narrative) > 0 {
		for _, s := range r.Narrative {
			b.WriteString(bullet(s))
		}
		b.WriteString("\n")
	}
	if r.Apply != nil && len(r.Apply.Narrative) > 0 {
		b.WriteString("**When you apply**\n\n")
		for _, s := range r.Apply.Narrative {
			b.WriteString(bullet(s))
		}
		b.WriteString("\n")
	}

	b.WriteString(consumersSection(r))

	footer := fmt.Sprintf("<sub>%s · %s %s plan", escapeText(r.Generator), toolName(r.Plan.Tool), escapeText(r.Plan.Version))
	if opts.ReportURL != "" {
		footer += fmt.Sprintf(" · [interactive report](%s)", opts.ReportURL)
	}
	footer += "</sub>\n"
	room := func(extra int) bool { return b.Len()+extra+len(footer) <= MaxLen }

	if acc := accessSection(r); acc != "" && room(len(acc)+2000) {
		b.WriteString(acc)
	}
	if len(r.Findings) > 0 {
		full := findingsSection(r.Findings, false)
		switch {
		case room(len(full)):
			b.WriteString(full)
		default:
			b.WriteString(compactFindings(r.Findings, MaxLen-b.Len()-len(footer)-200))
		}
	}
	if m := mapSection(r); m != "" && room(len(m)+1000) {
		b.WriteString(m)
	}
	if changes := changesSection(r); room(len(changes)) {
		b.WriteString(changes)
	} else if room(200) {
		b.WriteString("<details><summary><b>Changes</b></summary>\n\nThe list of changes is too long for a comment; see the full report.\n\n</details>\n\n")
	}
	if out := outputsSection(r); out != "" && room(len(out)) {
		b.WriteString(out)
	}
	b.WriteString(footer)
	_, err := io.WriteString(w, b.String())
	return err
}

// consumersSection says what the plan does to each service outside
// Terraform that the configuration file declares, the worst first. Owners
// are mentioned, which notifies them, only for a service whose declaration
// asks for it; otherwise their names are written so as not to.
func consumersSection(r *model.Report) string {
	if len(r.Consumers) == 0 {
		return ""
	}
	groups := model.Grouped(r.Consumers)
	var b strings.Builder
	b.WriteString("**Outside Terraform** · services you declared, which interragate cannot see\n\n")
	for _, c := range groups[model.StandingAffected] {
		name := "**" + escapeText(string(c.Name)) + "**"
		if u, err := url.Parse(c.Link); err == nil && c.Link != "" && (u.Scheme == "https" || u.Scheme == "http") {
			link := strings.NewReplacer("(", "%28", ")", "%29", " ", "%20", "\\", "%5C").Replace(c.Link)
			name = "**[" + escapeText(string(c.Name)) + "](" + link + ")**"
		}
		parts := []string{name}
		if c.Platform != "" {
			parts = append(parts, escapeText(c.Platform))
		}
		for _, o := range c.Owners {
			parts = append(parts, mention(o, c.Notify))
		}
		b.WriteString("- " + strings.Join(parts, " · ") + "\n")
		for _, e := range c.Effects {
			dot := ""
			if e.Severity > model.Info {
				dot = dots[e.Severity] + " "
			}
			b.WriteString("  - " + dot + escapeText(e.Text) + "\n")
		}
	}
	if len(groups[model.StandingAffected]) > 0 {
		b.WriteString("\n")
	}
	var notes []string
	names := func(cs []*model.Consumer) string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = escapeText(string(c.Name))
		}
		return strings.Join(out, ", ")
	}
	if cs := groups[model.StandingUnaffected]; len(cs) > 0 {
		notes = append(notes, "Not affected: "+names(cs)+".")
	}
	if cs := groups[model.StandingHidden]; len(cs) > 0 {
		notes = append(notes, "Left out by disable or ignore: "+names(cs)+".")
	}
	var missing []string
	for _, c := range r.Consumers {
		if len(c.Missing) == 0 {
			continue
		}
		uses := make([]string, len(c.Missing))
		for i, m := range c.Missing {
			uses[i] = inlineCode(m, false)
		}
		missing = append(missing, escapeText(string(c.Name))+" ("+strings.Join(uses, ", ")+")")
	}
	if len(missing) > 0 {
		notes = append(notes, "Declared to use what this plan does not have: "+strings.Join(missing, "; ")+".")
	}
	if len(notes) > 0 {
		b.WriteString(strings.Join(notes, " ") + "\n\n")
	}
	return b.String()
}

// handle matches a GitHub user or team, the only text written as a mention.
var handle = regexp.MustCompile(`^@[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:/[A-Za-z0-9._-]+)?$`)

// mention writes an owner: as a mention, which notifies them, when the
// service asks for that and it is a GitHub handle; otherwise so as not to.
func mention(owner string, notify bool) string {
	if notify && handle.MatchString(owner) {
		return owner
	}
	return escapeText(owner)
}

// mapSection draws what changes as a Mermaid flowchart, which GitHub and
// GitLab render in place: the shape of the change without leaving the
// pull request. A plan too large to draw legibly gets none.
func mapSection(r *model.Report) string {
	var d strings.Builder
	if err := mermaid.Render(&d, r, mermaid.Options{Max: 40}); err != nil || r.Summary.Total() == 0 {
		return ""
	}
	return "<details><summary><b>Map</b></summary>\n\n```mermaid\n" + d.String() + "```\n\n</details>\n\n"
}

// accessSection lists what the plan's policy and firewall changes allow
// and stop allowing, by resource. It starts open when something lets
// anyone or anything in, so a reviewer cannot miss it.
func accessSection(r *model.Report) string {
	var b strings.Builder
	n, wide := 0, false
	var changes []*model.Change
	for _, ch := range r.Changes {
		if len(ch.Access) > 0 {
			changes = append(changes, ch)
		}
	}
	// Whatever lets anyone or anything in goes first.
	widens := func(ch *model.Change) bool {
		for _, l := range ch.Access {
			if l.Wide {
				return true
			}
		}
		return false
	}
	sort.SliceStable(changes, func(i, j int) bool { return widens(changes[i]) && !widens(changes[j]) })
	for _, ch := range changes {
		n++
		fmt.Fprintf(&b, "%s\n", inlineCode(ch.Address, false))
		for i, l := range ch.Access {
			if i == 12 {
				fmt.Fprintf(&b, "- and %d more\n", len(ch.Access)-i)
				break
			}
			op := "**+**"
			if l.Op == "-" {
				op = "**−**"
			}
			text := escapeText(l.Text)
			if l.Wide {
				wide = true
				text = "**" + text + "**"
			}
			fmt.Fprintf(&b, "- %s %s\n", op, text)
		}
		b.WriteString("\n")
	}
	if n == 0 {
		return ""
	}
	open := ""
	if wide {
		open = " open"
	}
	return fmt.Sprintf("<details%s><summary><b>Access (%d %s)</b></summary>\n\n%s</details>\n\n", open, n, map[bool]string{true: "change", false: "changes"}[n == 1], b.String())
}

// bullet writes a sentence as a list item, marked with its severity when
// it is worth more than a note.
func bullet(s model.Sentence) string {
	if s.Severity <= model.Info {
		return fmt.Sprintf("- %s\n", escapeText(s.Text))
	}
	return fmt.Sprintf("- %s %s\n", dots[s.Severity], escapeText(s.Text))
}

func findingsSection(fs []*model.Finding, _ bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<details><summary><b>Findings (%d)</b></summary>\n\n", len(fs))
	for _, f := range fs {
		fmt.Fprintf(&b, "#### %s %s\n\n", dots[f.Severity], escapeText(f.Title))
		fmt.Fprintf(&b, "%s\n\n", escapeText(f.Message))
		if f.Remediation != "" {
			fmt.Fprintf(&b, "**Fix:** %s\n\n", escapeText(f.Remediation))
		}
		if f.Snippet != "" {
			fence := codeFence(f.Snippet)
			fmt.Fprintf(&b, "%shcl\n%s\n%s\n\n", fence, f.Snippet, fence)
		}
		fmt.Fprintf(&b, "<sub>%s · %s</sub>\n\n", f.Severity, f.Rule)
	}
	b.WriteString("</details>\n\n")
	return b.String()
}

// compactFindings lists findings one line each, within budget bytes.
func compactFindings(fs []*model.Finding, budget int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<details><summary><b>Findings (%d)</b></summary>\n\n", len(fs))
	for i, f := range fs {
		line := fmt.Sprintf("- %s **%s**: %s\n", dots[f.Severity], escapeText(f.Title), escapeText(firstSentence(f.Message)))
		if b.Len()+len(line) > budget-120 {
			fmt.Fprintf(&b, "\nAnd %d more findings; see the full report.\n", len(fs)-i)
			break
		}
		b.WriteString(line)
	}
	b.WriteString("\n</details>\n\n")
	return b.String()
}

func firstSentence(s string) string {
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

// codeFence returns a fence longer than any run of backticks in s.
func codeFence(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

func toolName(t string) string {
	if t == "opentofu" {
		return "OpenTofu"
	}
	return "Terraform"
}

func riskName(s model.Severity) string {
	switch s {
	case model.None:
		return "no changes"
	case model.Info:
		return "low risk"
	}
	return s.String() + " risk"
}

func counts(s model.Summary) string {
	var parts []string
	add := func(n int, sym, label string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("**%s%d** %s", sym, n, label))
		}
	}
	add(s.Create, "+", "create")
	add(s.Update, "~", "update")
	add(s.Replace, "-/+", "replace")
	add(s.Delete, "−", "destroy")
	add(s.Move, "→", "move")
	add(s.Import, "↓", "import")
	add(s.Forget, "⊘", "forget")
	line := strings.Join(parts, " · ")
	if line == "" {
		line = "No resource changes."
	}
	var extra []string
	if s.Cosmetic > 0 {
		extra = append(extra, fmt.Sprintf("%d cosmetic", s.Cosmetic))
	}
	if s.Indirect > 0 {
		extra = append(extra, fmt.Sprintf("%d follow from other changes", s.Indirect))
	}
	if s.Drifted > 0 {
		extra = append(extra, fmt.Sprintf("%d drifted", s.Drifted))
	}
	if len(extra) > 0 {
		line += " · " + strings.Join(extra, " · ")
	}
	return line
}

var symbols = map[model.Action]string{
	model.Create: "+", model.Update: "~", model.Replace: "-/+", model.Delete: "−",
	model.Read: "≤", model.Forget: "⊘", model.NoOp: "→",
}

func changesSection(r *model.Report) string {
	var rows []*model.Change
	for _, ch := range r.Changes {
		if ch.Action == model.NoOp && !ch.Moved && !ch.Importing {
			continue
		}
		rows = append(rows, ch)
	}
	if len(rows) == 0 {
		return ""
	}
	// With git to compare against, a reviewer of the branch wants the
	// changes nobody edited apart from the rest: they are the surprises.
	var mine, rest []*model.Change
	for _, ch := range rows {
		if ch.Origin != "" {
			mine = append(mine, ch)
		} else {
			rest = append(rest, ch)
		}
	}
	if r.Git == nil || len(mine) == 0 || len(rest) == 0 {
		return changeTable(fmt.Sprintf("Changes (%d)", len(rows)), rows)
	}
	return changeTable(fmt.Sprintf("Not tied to an edit (%d)", len(rest)), rest) +
		changeTable(fmt.Sprintf("From your edits (%d)", len(mine)), mine)
}

func changeTable(title string, rows []*model.Change) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<details><summary><b>%s</b></summary>\n\n", title)
	b.WriteString("| | Resource | What and why |\n|:-:|---|---|\n")
	for _, ch := range rows {
		sym := symbols[ch.Action]
		if ch.Importing {
			sym = "↓"
		}
		why := ch.Why
		switch {
		case ch.Cosmetic != "":
			why = "Cosmetic: " + ch.Cosmetic + " only."
		}
		sev := ""
		if ch.Severity >= model.Medium {
			sev = " " + dots[ch.Severity]
		}
		fmt.Fprintf(&b, "| %s | %s%s | **%s** %s |\n", sym, inlineCode(ch.Address, true), sev, ch.Label(), cell(why))
	}
	b.WriteString("\n</details>\n\n")
	return b.String()
}

func outputsSection(r *model.Report) string {
	var b strings.Builder
	n := 0
	for _, o := range r.Outputs {
		if o.Action == model.NoOp {
			continue
		}
		if n == 0 {
			b.WriteString("<details><summary><b>Outputs</b></summary>\n\n| | Output | |\n|:-:|---|---|\n")
		}
		n++
		note := ""
		if o.Sensitive {
			note = "sensitive"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", symbols[o.Action], inlineCode(o.Name, true), note)
	}
	if n == 0 {
		return ""
	}
	b.WriteString("\n</details>\n\n")
	return b.String()
}

// cell makes text safe inside a table cell.
// inlineCode writes s as a code span that nothing in it can end: an
// address can hold any for_each key, backticks, pipes and HTML included.
// The fence is longer than any run of backticks inside, and in a table a
// pipe is escaped, which GitHub reads as a pipe inside the code span.
func inlineCode(s string, inTable bool) string {
	s = strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	if inTable {
		s = strings.ReplaceAll(s, "|", "\\|")
	}
	fence := strings.Repeat("`", longest+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + s + fence
}

func cell(s string) string {
	return strings.ReplaceAll(escapeText(strings.ReplaceAll(s, "\n", " ")), "|", "\\|")
}

// plainEscaper neutralises Markdown and HTML outside code spans, and
// breaks @mentions so a plan value cannot ping anyone.
var plainEscaper = strings.NewReplacer(
	"\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "~", "\\~",
	"<", "&lt;", ">", "&gt;", "@", "@\u200b", "\n", " ",
)

// escapeText escapes prose that uses backticks for code spans. If the
// backticks do not pair up (a plan value contained one), nothing is
// treated as code.
func escapeText(s string) string {
	parts := strings.Split(s, "`")
	if len(parts)%2 == 0 {
		return plainEscaper.Replace(strings.ReplaceAll(s, "`", "'"))
	}
	for i := range parts {
		if i%2 == 0 {
			parts[i] = plainEscaper.Replace(parts[i])
		} else {
			parts[i] = strings.ReplaceAll(parts[i], "\n", " ")
		}
	}
	return strings.Join(parts, "`")
}
