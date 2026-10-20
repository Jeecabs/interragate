package html

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate"
	"github.com/Jeecabs/interragate/internal/model"
)

//go:embed assets/roots.html
var rootsHTML string

var rootsPage = template.Must(template.New("roots").Parse(rootsHTML))

// Root is one Terraform root module in an index of several.
type Root struct {
	Name   string        // its directory, relative to where interragate looked
	Href   string        // its report, relative to the index
	Report *model.Report // nil when the plan failed
	Error  string        // why the plan failed
}

type rootRow struct {
	Name, Href, Verdict, Headline, Error string
	Icon                                 template.HTML
	Counts                               []rootCount
	rank                                 int
}

type rootCount struct{ Kind, Text string }

// RenderRoots writes a page listing several roots' reports, failed plans
// first and then by risk, each line linking to that root's report. facts is
// a quiet line under the headline: where, with what, when.
func RenderRoots(w io.Writer, title, facts string, roots []Root) error {
	var rows []rootRow
	tally := map[string]int{}
	for _, r := range roots {
		row := rootRow{Name: r.Name, Href: r.Href}
		if r.Report == nil {
			row.Verdict, row.Error, row.rank = "Plan failed", r.Error, 100
			row.Icon = shape("critical")
			tally["failed"]++
		} else {
			level := r.Report.Risk.Level
			row.Verdict, row.Headline, row.rank = verdict(level), r.Report.Risk.Headline, int(level)
			row.Icon = shape(level.String())
			row.Counts = counts(r.Report.Summary)
			tally[verdict(level)]++
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank > rows[j].rank
		}
		return rows[i].Name < rows[j].Name
	})
	var parts []string
	for _, k := range []string{"failed", "Critical risk", "High risk", "Medium risk", "Low risk", "No changes"} {
		n := tally[k]
		if n == 0 {
			continue
		}
		switch k {
		case "failed":
			parts = append(parts, fmt.Sprintf("%d failed to plan", n))
		case "No changes":
			parts = append(parts, fmt.Sprintf("%d with no changes", n))
		default:
			parts = append(parts, fmt.Sprintf("%d %s", n, strings.ToLower(k)))
		}
	}
	headline := fmt.Sprintf("%d roots", len(roots))
	if len(roots) == 1 {
		headline = "1 root"
	}
	if len(parts) > 0 {
		headline += ": " + join(parts) + "."
	}
	return rootsPage.Execute(w, map[string]any{
		"Title":     title,
		"Licences":  interragate.LicenceNotices(),
		"Headline":  headline,
		"Facts":     facts,
		"Rows":      rows,
		"CSS":       template.CSS(appCSS),
		"GeistSans": template.CSS(base64.StdEncoding.EncodeToString(geistSans)),
		"GeistMono": template.CSS(base64.StdEncoding.EncodeToString(geistMono)),
	})
}

func verdict(s model.Severity) string {
	switch s {
	case model.None:
		return "No changes"
	case model.Info, model.Low:
		return "Low risk"
	}
	name := s.String()
	return strings.ToUpper(name[:1]) + name[1:] + " risk"
}

// counts writes a summary in Terraform's notation: −2 -/+1 ~5 +3.
func counts(s model.Summary) []rootCount {
	var out []rootCount
	for _, c := range []struct {
		n          int
		kind, sign string
	}{{s.Delete, "delete", "−"}, {s.Replace, "replace", "-/+"}, {s.Update, "update", "~"}, {s.Create, "create", "+"}} {
		if c.n > 0 {
			out = append(out, rootCount{c.kind, fmt.Sprintf("%s%d", c.sign, c.n)})
		}
	}
	return out
}

// shape is the severity mark the report uses: octagon, triangle, diamond
// or dot, in the severity's colour.
func shape(level string) template.HTML {
	color := map[string]string{"critical": "--sev-critical", "high": "--sev-high", "medium": "--sev-medium"}[level]
	if color == "" {
		color = "--sev-low"
	}
	var d string
	switch level {
	case "critical":
		d = `<polygon points="5.2,1 10.8,1 15,5.2 15,10.8 10.8,15 5.2,15 1,10.8 1,5.2"/>`
	case "high":
		d = `<polygon points="8,1.5 15,14 1,14"/>`
	case "medium":
		d = `<polygon points="8,1 15,8 8,15 1,8"/>`
	default:
		d = `<circle cx="8" cy="8" r="5"/>`
	}
	return template.HTML(`<svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true" style="fill:var(` + color + `)">` + d + `</svg>`)
}

func join(parts []string) string {
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}
