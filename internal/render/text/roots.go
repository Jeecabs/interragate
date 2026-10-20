package text

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Jeecabs/interragate/internal/model"
)

// Root is one Terraform root module in a run over several.
type Root struct {
	Name   string
	Report *model.Report // nil when the plan failed
	Error  string        // why the plan failed
}

// RenderRoots prints a line per root, failed plans first and then by
// risk: the verdict, the root, its headline and its changes.
func RenderRoots(w io.Writer, roots []Root, opts Options) error {
	if opts.Width <= 0 {
		opts.Width = 100
	}
	p := &printer{w: w, opts: opts}
	sorted := append([]Root(nil), roots...)
	rank := func(r Root) int {
		if r.Report == nil {
			return 100
		}
		return int(r.Report.Risk.Level)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if rank(sorted[i]) != rank(sorted[j]) {
			return rank(sorted[i]) > rank(sorted[j])
		}
		return sorted[i].Name < sorted[j].Name
	})
	width := 0
	for _, r := range sorted {
		width = max(width, utf8.RuneCountInString(r.Name))
	}
	for _, r := range sorted {
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(r.Name))
		if r.Report == nil {
			p.printf("  %s %s%s  %s\n", p.style("●", red), p.style(r.Name, bold), pad, p.style("plan failed", red, bold))
			first, _, _ := strings.Cut(strings.TrimSpace(r.Error), "\n")
			p.wrap(first, "    ", "    ")
			continue
		}
		level := r.Report.Risk.Level
		dot := p.style("●", levelColor(level))
		if level <= model.Info {
			dot = p.style("·", dim)
		}
		label := strings.ToUpper(levelName(level)[:1]) + levelName(level)[1:]
		line := "  " + dot + " " + p.style(r.Name, bold) + pad + "  " + p.style(label, levelColor(level))
		if counts := p.shortCounts(r.Report.Summary); counts != "" {
			line += strings.Repeat(" ", 13-len(label)+2) + counts
		}
		p.printf("%s\n", line)
		if level > model.None {
			p.wrap(r.Report.Risk.Headline, "    ", "    ")
		}
	}
	return p.err
}

// shortCounts writes a summary in Terraform's notation: -2 -/+1 ~5 +3.
func (p *printer) shortCounts(s model.Summary) string {
	var parts []string
	for _, c := range []struct {
		n           int
		sign, color string
	}{{s.Delete, "-", red}, {s.Replace, "-/+", magenta}, {s.Update, "~", yellow}, {s.Create, "+", green}} {
		if c.n > 0 {
			parts = append(parts, p.style(fmt.Sprintf("%s%d", c.sign, c.n), c.color))
		}
	}
	return strings.Join(parts, " ")
}
