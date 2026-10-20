package analysis

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

// minSecret is the shortest sensitive string redact looks for elsewhere.
// Shorter values ("prod", "true") would blank out ordinary text.
const minSecret = 6

// sensitive is what redact looks for: values, wherever they appear, and
// lines, which only count as a line of their own.
type sensitive struct {
	values []string // longest first, so a value containing another goes whole
	lines  map[string]bool
}

// secrets collects every string the plan marks sensitive: values under
// sensitive masks, sensitive outputs, and the values and defaults of
// variables declared sensitive. Each is also looked for in the forms the
// source writes it in, where the report quotes source: escaped inside an
// HCL string; line by line, as a heredoc spreads a key or certificate; and,
// in a sensitive JSON document, the strings under names that sound secret,
// as jsonencode({ password = "..." }) writes them. The rest of such a
// document (a user name, a host, an engine) is usually written elsewhere
// too, where it is no secret.
func secrets(p *plan.Plan) sensitive {
	values, lines := map[string]bool{}, map[string]bool{}
	add := func(s string) {
		if len(strings.TrimSpace(s)) < minSecret {
			return
		}
		values[s] = true
		if escaped := hclEscape(s); escaped != s {
			values[escaped] = true
		}
		// A line is matched alone, as an edit shows a heredoc's lines: as
		// part of other text, a line such as "apiVersion: v1" would blank
		// out ordinary text.
		if strings.Contains(s, "\n") {
			for _, line := range strings.Split(s, "\n") {
				if line = strings.TrimSpace(line); len(line) >= minSecret {
					lines[line] = true
				}
			}
		}
	}
	var leaves func(v any, secret bool)
	leaves = func(v any, secret bool) {
		switch x := v.(type) {
		case string:
			if secret {
				add(x)
			}
		case map[string]any:
			for k, c := range x {
				leaves(c, secret || secretName.MatchString(k))
			}
		case []any:
			for _, c := range x {
				leaves(c, secret)
			}
		}
	}
	var collect func(v, mask any, all bool)
	collect = func(v, mask any, all bool) {
		all = all || diff.Flag(mask)
		switch x := v.(type) {
		case string:
			if !all {
				return
			}
			add(x)
			if doc, ok := diff.DecodeJSON(x); ok {
				leaves(doc, false)
			}
		case map[string]any:
			for k, c := range x {
				collect(c, diff.Child(mask, k), all)
			}
		case []any:
			for i, c := range x {
				collect(c, diff.Child(mask, i), all)
			}
		}
	}
	changes := append(append([]*plan.ResourceChange(nil), p.ResourceChanges...), p.ResourceDrift...)
	for _, rc := range changes {
		collect(rc.Change.Before, rc.Change.BeforeSensitive, false)
		collect(rc.Change.After, rc.Change.AfterSensitive, false)
	}
	for _, oc := range p.OutputChanges {
		collect(oc.Before, oc.BeforeSensitive, false)
		collect(oc.After, oc.AfterSensitive, false)
	}
	if p.Configuration != nil && p.Configuration.RootModule != nil {
		var walk func(m *plan.ConfigModule, root bool)
		walk = func(m *plan.ConfigModule, root bool) {
			if m == nil {
				return
			}
			for name, v := range m.Variables {
				if v != nil && v.Sensitive {
					if root {
						collect(p.Variables[name].Value, true, true)
					}
					collect(v.Default, true, true)
				}
			}
			for _, call := range m.ModuleCalls {
				if call != nil {
					walk(call.Module, false)
				}
			}
		}
		walk(p.Configuration.RootModule, true)
	}
	out := sensitive{values: make([]string, 0, len(values)), lines: lines}
	for s := range values {
		out.values = append(out.values, s)
	}
	sort.Slice(out.values, func(i, j int) bool {
		a, b := out.values[i], out.values[j]
		return len(a) > len(b) || len(a) == len(b) && a < b
	})
	return out
}

// hclEscape writes a string as it reads between the quotes of an HCL
// string literal.
func hclEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "${", "$${", "%{", "%%{").Replace(s)
}

// redact removes every sensitive string from the report, wherever it
// appears. Terraform only masks a value where its schema says so, and a
// sensitive value often reappears in an attribute that is not marked (for
// example terraform_data.output). A report is meant to be shared, so it
// errs on the side of hiding. The report's strings are searched as they
// are, not as JSON escapes them, for every value at once: a plan can mark
// thousands.
func redact(r *model.Report, s sensitive) *model.Report {
	if len(s.values) == 0 && len(s.lines) == 0 {
		return r
	}
	data, err := json.Marshal(r)
	if err != nil {
		return r
	}
	var doc any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if dec.Decode(&doc) != nil {
		return r
	}
	pairs := make([]string, 0, 2*len(s.values))
	for _, v := range s.values {
		pairs = append(pairs, v, diff.SensitivePlaceholder)
	}
	rep := strings.NewReplacer(pairs...)
	hide := func(text string) string {
		text = rep.Replace(text)
		if len(s.lines) == 0 {
			return text
		}
		lines := strings.Split(text, "\n")
		for i, l := range lines {
			if body := strings.TrimLeft(l, " \t"); s.lines[strings.TrimSpace(body)] {
				lines[i] = l[:len(l)-len(body)] + diff.SensitivePlaceholder
			}
		}
		return strings.Join(lines, "\n")
	}
	replaced := false
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			if t := hide(x); t != x {
				replaced = true
				return t
			}
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, c := range x {
				if t := hide(k); t != k {
					replaced, k = true, t
				}
				out[k] = walk(c)
			}
			return out
		case []any:
			for i, c := range x {
				x[i] = walk(c)
			}
		}
		return v
	}
	doc = walk(doc)
	if !replaced {
		return r
	}
	if data, err = json.Marshal(doc); err != nil {
		return r
	}
	var out model.Report
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return r
	}
	return &out
}
