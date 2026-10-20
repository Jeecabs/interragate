// Package mermaid draws the part of a plan that changes as a Mermaid
// flowchart, which GitHub and GitLab render in pull request comments: each
// resource that changes, grouped by module and marked the way terraform
// plan marks it, with the dependencies between them.
package mermaid

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
)

// Options adjust the drawing.
type Options struct {
	// Max is the most resources to draw; a larger plan gets a sentence
	// saying so instead. 0 means 50.
	Max int
}

// ErrTooBig is returned, with nothing written, when the plan changes more
// resources than Options.Max.
type ErrTooBig struct{ Resources, Max int }

func (e ErrTooBig) Error() string {
	return fmt.Sprintf("%d resources change, more than a diagram of %d can show", e.Resources, e.Max)
}

var glyph = map[model.Action]string{
	model.Create: "+", model.Update: "~", model.Replace: "-/+", model.Delete: "-", model.Read: "<=", model.Forget: "forget",
}

// classes colour each action as the report does, in strokes only, so the
// drawing works on light and dark pages alike. A destroy is dashed.
const classes = `  classDef create fill:transparent,stroke:#2f9e44,stroke-width:2px
  classDef update fill:transparent,stroke:#d99a00,stroke-width:2px
  classDef replace fill:transparent,stroke:#7c6ff0,stroke-width:2px
  classDef delete fill:transparent,stroke:#d55181,stroke-width:2px,stroke-dasharray:5 3
  classDef read fill:transparent,stroke:#8a877f,stroke-width:1px
  classDef keep fill:transparent,stroke:#8a877f,stroke-width:1px,stroke-dasharray:2 2,color:#8a877f
`

// Render writes the flowchart, without the code fence around it.
func Render(w io.Writer, r *model.Report, opts Options) error {
	if opts.Max <= 0 {
		opts.Max = 50
	}
	g := r.Graph
	if g == nil {
		g = &model.Graph{}
	}
	byID := map[string]*model.GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	// What each resource's instances do, leaving out updates that only
	// touch tags or formatting, as the report does until asked.
	acts := map[string]map[model.Action]int{}
	for _, ch := range r.Changes {
		if ch.Action == model.NoOp || ch.Cosmetic != "" || byID[ch.Resource] == nil {
			continue
		}
		if acts[ch.Resource] == nil {
			acts[ch.Resource] = map[model.Action]int{}
		}
		acts[ch.Resource][ch.Action]++
	}
	changed := map[string]bool{}
	for id := range acts {
		changed[id] = true
	}
	if len(changed) > opts.Max {
		return ErrTooBig{len(changed), opts.Max}
	}
	// An unchanged resource between two that change is drawn faintly, so
	// the chain that links them is not lost.
	up, down := map[string][]string{}, map[string][]string{}
	for _, e := range g.Edges {
		down[e[0]] = append(down[e[0]], e[1])
		up[e[1]] = append(up[e[1]], e[0])
	}
	shown := map[string]bool{}
	for id := range changed {
		shown[id] = true
	}
	for _, n := range g.Nodes {
		if changed[n.ID] {
			continue
		}
		if anyIn(up[n.ID], changed) && anyIn(down[n.ID], changed) {
			shown[n.ID] = true
		}
	}

	var b strings.Builder
	b.WriteString("flowchart LR\n")
	b.WriteString(classes)
	ids := map[string]string{}
	var order []string
	for _, n := range g.Nodes {
		if shown[n.ID] {
			order = append(order, n.ID)
		}
	}
	sort.Strings(order)
	for i, id := range order {
		ids[id] = fmt.Sprintf("n%d", i)
	}
	// Resources at the top level first, then each module's in a box.
	byModule := map[string][]string{}
	var modules []string
	for _, id := range order {
		m := byID[id].Module
		if byModule[m] == nil && m != "" {
			modules = append(modules, m)
		}
		byModule[m] = append(byModule[m], id)
	}
	sort.Strings(modules)
	node := func(indent, id string) {
		n := byID[id]
		name := n.Type + "." + n.Name
		if n.Mode == "data" {
			name = "data." + name
		}
		label, class := name, "keep"
		if a := acts[id]; len(a) > 0 {
			// The worst action colours it; the label says what each
			// instance does: "-/+ aws_subnet.private ×2", or
			// "aws_subnet.private · -/+ 1 · - 1" when they differ.
			kinds := make([]model.Action, 0, len(a))
			for k := range a {
				kinds = append(kinds, k)
			}
			sort.Slice(kinds, func(i, j int) bool { return kinds[i].Rank() > kinds[j].Rank() })
			class = string(kinds[0])
			// Non-breaking spaces keep a mark with what it marks when
			// Mermaid wraps a long label.
			if len(kinds) == 1 {
				label = glyph[kinds[0]] + "\u00a0" + name
				if a[kinds[0]] > 1 {
					label += fmt.Sprintf("\u00a0×%d", a[kinds[0]])
				}
			} else {
				for _, k := range kinds {
					label += fmt.Sprintf(" · %s\u00a0%d", glyph[k], a[k])
				}
			}
		}
		fmt.Fprintf(&b, "%s%s[\"%s\"]:::%s\n", indent, ids[id], escape(label), class)
	}
	for _, id := range byModule[""] {
		node("  ", id)
	}
	for i, m := range modules {
		fmt.Fprintf(&b, "  subgraph m%d[\"%s\"]\n", i, escape(m))
		for _, id := range byModule[m] {
			node("    ", id)
		}
		b.WriteString("  end\n")
	}
	var edges []string
	for _, e := range g.Edges {
		if shown[e[0]] && shown[e[1]] {
			edges = append(edges, fmt.Sprintf("  %s --> %s\n", ids[e[0]], ids[e[1]]))
		}
	}
	sort.Strings(edges)
	for _, e := range edges {
		b.WriteString(e)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func anyIn(ids []string, set map[string]bool) bool {
	for _, id := range ids {
		if set[id] {
			return true
		}
	}
	return false
}

// escape makes text safe inside a quoted Mermaid label.
var escaper = strings.NewReplacer(`"`, "#quot;", "<", "#lt;", ">", "#gt;", "\n", " ")

func escape(s string) string { return escaper.Replace(s) }
