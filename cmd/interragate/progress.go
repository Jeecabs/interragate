package main

import (
	"io"
	"os"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/Jeecabs/interragate/internal/tfexec"
)

// planProgress turns Terraform's plan events into one line of status: how
// many resources have been refreshed, how many changes are planned, and
// what is being looked at now.
type planProgress struct {
	lead      string
	width     int
	show      func(string)
	refreshed int
	planned   int
	current   string
	last      time.Time
}

func (p *planProgress) event(e tfexec.Event) {
	switch e.Type {
	case "refresh_start", "apply_start": // apply_start is a data source read during plan
		p.current = e.Address
	case "refresh_complete", "apply_complete":
		p.refreshed++
		if p.current == e.Address {
			p.current = ""
		}
	case "planned_change":
		p.planned++
	default:
		return
	}
	// Redrawing for every event would flicker on a big plan.
	if time.Since(p.last) < 80*time.Millisecond {
		return
	}
	p.last = time.Now()
	p.show(p.line())
}

func (p *planProgress) line() string {
	s := p.lead + ": refreshed " + plural(p.refreshed, "resource")
	if p.planned > 0 {
		s += ", " + plural(p.planned, "change") + " so far"
	} else if p.current != "" {
		s += ", now " + p.current
	}
	// A line that wraps cannot be redrawn in place.
	if max := p.width - 2; max > 10 && utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max-1])
	}
	return s + "…"
}

// lineWidth is the terminal's width, or 80 when w is not a terminal.
func lineWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if wd, _, err := term.GetSize(int(f.Fd())); err == nil && wd > 0 {
			return wd
		}
	}
	return 80
}
