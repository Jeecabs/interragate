package analysis

import (
	"regexp"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
	"github.com/Jeecabs/interragate/internal/source"
)

// The lines of an edit are source text, and a report is shared, so the
// lines that hold a secret are masked by what the configuration and the
// plan say of them, not by how they read: the lines of an argument whose
// value the plan marks sensitive, all the lines of a variable declared
// sensitive, those of a module argument the module declares sensitive.
// Heredocs and multi-line objects are masked to their last line. What
// sounds secret is masked as well (maskSecretLiteral), and the values the
// plan marks are redacted from the whole report (redact).

// markSecrets records, for each block, the arguments that hold a value the
// plan marks sensitive, before or after, in any change to any instance of
// it. Blocks are known by directory and key, as two calls to one module
// share its source. The arguments of a module call are the variables of the
// module it calls, secret when the module declares them sensitive, now or
// in the base.
func (e *editor) markSecrets(r *model.Report, raw map[*model.Change]*plan.ResourceChange) {
	mark := func(id, name string) {
		if e.secrets[id] == nil {
			e.secrets[id] = map[string]bool{}
		}
		e.secrets[id][name] = true
	}
	for _, ch := range r.Changes {
		rc, m := raw[ch], e.src.Modules[moduleConfig(ch.Module)]
		if rc == nil || m == nil {
			continue
		}
		_, key := blockKey(ch)
		id := slashDir(m.Dir) + "|" + key
		for _, mask := range []any{rc.Change.BeforeSensitive, rc.Change.AfterSensitive} {
			switch x := mask.(type) {
			case bool:
				if x {
					mark(id, "*")
				}
			case map[string]any:
				for k, v := range x {
					if diff.Marked(v) {
						mark(id, k)
					}
				}
			}
		}
	}
	for module := range e.in.calls {
		parent, name := splitModule(module)
		if pm := e.src.Modules[parent]; pm != nil {
			for v := range e.sensitiveVars(module) {
				mark(slashDir(pm.Dir)+"|module."+name, v)
			}
		}
	}
}

// secretArgs names the arguments of a block in a module that hold a
// secret; "*" stands for all of them.
func (e *editor) secretArgs(module, key string) map[string]bool {
	if m := e.src.Modules[module]; m != nil {
		return e.secrets[slashDir(m.Dir)+"|"+key]
	}
	return nil
}

// sensitiveVars names the variables a module declares sensitive, in the
// configuration now or in the base: a value rotated in the same edit that
// drops sensitive = true was a secret all the same.
func (e *editor) sensitiveVars(module string) map[string]bool {
	if s, ok := e.sensitive[module]; ok {
		return s
	}
	s := map[string]bool{}
	if m := e.in.modules[module]; m != nil {
		for name, v := range m.Variables {
			if v != nil && v.Sensitive {
				s[name] = true
			}
		}
	}
	read := func(inputs map[string]*source.Block) {
		for k, b := range inputs {
			if name, ok := strings.CutPrefix(k, "var."); ok && b.Sensitive {
				s[name] = true
			}
		}
	}
	if m := e.src.Modules[module]; m != nil {
		read(m.Inputs)
		for _, f := range e.inDir(slashDir(m.Dir)) {
			read(e.oldModule(f).Inputs)
		}
	}
	e.sensitive[module] = s
	return s
}

// union merges two sets of argument names.
func union(a, b map[string]bool) map[string]bool {
	if len(b) == 0 {
		return a
	}
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// spot is where a line is in a block: in the argument named, and whether
// on the line the argument starts.
type spot struct {
	name  string
	first bool
}

// spots maps each line of a block to the argument it is part of. keep
// picks the arguments to map; nil maps them all.
func spots(b *source.Block, keep func(source.Arg) bool) map[int]spot {
	if b == nil {
		return nil
	}
	out := map[int]spot{}
	for _, a := range b.Args {
		if keep != nil && !keep(a) {
			continue
		}
		for n := a.Line; n <= max(a.EndLine, a.Line); n++ {
			out[n] = spot{name: a.Name, first: n == a.Line}
		}
	}
	return out
}

// hideArgs masks the lines of an edit to a block that hold a secret: those
// of an argument secret names, where its value is written out. now and was
// are the block as it is and as it was in the base. When the block as it
// was is not known, neither is where its secrets were, and every line
// removed from it is masked.
func hideArgs(ed *model.Edit, secret map[string]bool, now, was *source.Block) {
	if ed == nil || len(secret) == 0 {
		return
	}
	held := func(a source.Arg) bool { return a.Literal && (secret[a.Name] || secret["*"]) }
	nowAt, wasAt := spots(now, held), spots(was, held)
	for i := range ed.Lines {
		l := &ed.Lines[i]
		at := nowAt
		if l.Op == "-" {
			at = wasAt
		}
		if s, ok := at[l.Line]; ok || at == nil {
			l.Text = hidden(l.Text, s)
		}
	}
}

// hideAll masks every line of an edit to a variable declared sensitive:
// its default, and anything else that may quote the value, such as a
// validation. now and was are the variable's block as it is and as it
// was, which name the argument a line starts.
func hideAll(ed *model.Edit, now, was *source.Block) {
	if ed == nil {
		return
	}
	nowAt, wasAt := spots(now, nil), spots(was, nil)
	for i := range ed.Lines {
		l := &ed.Lines[i]
		at := nowAt
		if l.Op == "-" {
			at = wasAt
		}
		l.Text = hidden(l.Text, at[l.Line])
	}
}

// blockHeader matches the first line of a block with nothing after its
// opening brace, such as validation { or variable "db_password" {.
var blockHeader = regexp.MustCompile(`^[\w-]+(\s+("[^"]*"|[\w-]+))*\s*\{$`)

// hidden is a line of a secret as an edit shows it: its indent and, on the
// line an argument starts, the argument's name. Blank lines, closing
// brackets and the header of a block hold nothing, and stay as they are.
func hidden(text string, s spot) string {
	body := strings.TrimSpace(text)
	indent := text[:len(text)-len(strings.TrimLeft(text, " \t"))]
	if trivial(body) || blockHeader.MatchString(body) {
		return text
	}
	if s.first && s.name != "" {
		for _, name := range []string{s.name, `"` + s.name + `"`} {
			rest, ok := strings.CutPrefix(body, name)
			if t := strings.TrimLeft(rest, " \t"); ok && (strings.HasPrefix(t, "=") || strings.HasPrefix(t, ":")) {
				return indent + body[:len(body)-len(t)+1] + " " + diff.SensitivePlaceholder
			}
		}
	}
	return indent + diff.SensitivePlaceholder
}
