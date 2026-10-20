package text

import (
	"io"
	"strings"

	"github.com/Jeecabs/interragate/internal/delta"
	"github.com/Jeecabs/interragate/internal/model"
)

// RenderDelta prints how a plan differs from the one before it, for
// interragate watch and interragate diff: a lead line saying which plans
// these are, the new verdict and counts, then each difference, most
// important first.
func RenderDelta(w io.Writer, r *model.Report, d *delta.Delta, lead string, opts Options) error {
	if opts.Width <= 0 {
		opts.Width = 100
	}
	p := &printer{w: w, opts: opts}
	if lead != "" {
		p.printf("%s\n", lead)
	}
	verdict := levelName(r.Risk.Level)
	verdict = strings.ToUpper(verdict[:1]) + verdict[1:]
	p.printf("  %s  %s\n", p.style(verdict, levelColor(r.Risk.Level), bold), p.counts(r.Summary))
	for _, s := range d.Sentences {
		var bullet string
		switch {
		case s.Severity == model.None:
			bullet = p.style("✓", green)
		case s.Severity == model.Info:
			bullet = p.style("·", dim)
		default:
			bullet = p.style("●", levelColor(s.Severity))
		}
		p.wrap(p.code(s.Text), "  "+bullet+" ", "    ")
	}
	return p.err
}
