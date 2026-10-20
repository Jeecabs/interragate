package analysis

import (
	"bytes"
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/gitdiff"
	"github.com/Jeecabs/interragate/internal/graph"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
	"github.com/Jeecabs/interragate/internal/rules"
	"github.com/Jeecabs/interragate/internal/source"
)

// maxEditLines caps the lines kept for one change's edit, so a large new
// block does not swamp the report.
const maxEditLines = 40

// Edit kinds.
const (
	editEdited   = "edited"
	editAdded    = "added"
	editRemoved  = "removed"
	editModule   = "module"
	editInput    = "input"
	editProvider = "provider"
)

// edits attaches to each change the edit to its configuration it comes
// from, when the configuration was compared with a git base.
func (a *analyzer) edits(r *model.Report) {
	d := a.opts.Edits
	if d == nil {
		return
	}
	r.Git = &model.GitInfo{Base: d.Base, Commit: d.Commit, Branch: d.Branch, Dirty: d.Dirty, Files: []string{}}
	for _, f := range d.Files {
		r.Git.Files = append(r.Git.Files, f.Path)
	}
	for _, p := range d.Providers {
		r.Git.Providers = append(r.Git.Providers, model.ProviderChange{Source: p.Source, From: p.From, To: p.To})
	}
	// Without edits to configuration files, or the source to find them in,
	// no change can be tied to one; in a destroy plan -destroy explains
	// every change. The rules that ask where a change comes from go by the
	// same test.
	if !rules.Traced(r) {
		return
	}
	in := newInputs(a.plan.Configuration)
	e := newEditor(a.src, d, in)
	e.markSecrets(r, a.raw)
	// Blocks that exist come first: a block renamed without a moved block
	// is matched with its old name there, and the destroy of the old name
	// then shows the same edit.
	for _, ch := range r.Changes {
		if ch.Location != nil {
			module, key := blockKey(ch)
			ch.Edit = e.block(module, key)
		}
	}
	for _, ch := range r.Changes {
		if ch.Edit == nil && ch.Location == nil && (ch.Action == model.Delete || ch.Action == model.Forget) {
			kind := "resource"
			if ch.Mode == "data" {
				kind = "data"
			}
			if m := a.src.Modules[moduleConfig(ch.Module)]; m != nil {
				ch.Edit = e.removed(slashDir(m.Dir), kind, ch.Type, ch.Name)
			}
		}
	}
	// A change to tags_all alone (tags can move with it as the provider
	// re-attributes them) comes from the provider's default_tags, whatever
	// else the resource reads, so every such change is tied to the same
	// edit wherever it is.
	for _, ch := range r.Changes {
		if ch.Edit == nil && acts(ch) && onlyTagsAll(a.full[ch]) {
			ch.Edit = e.provider(ch.Provider)
		}
	}
	// A change inside a module whose own block was not edited follows from
	// the module's inputs, so the edit to the call explains it.
	for _, ch := range r.Changes {
		if ch.Edit == nil && ch.Module != "" && acts(ch) {
			ch.Edit = e.moduleCall(moduleConfig(ch.Module))
		}
	}
	// A change that no block edit explains may read a variable or local
	// that was edited: in its arguments, count or for_each, through other
	// locals, or through the arguments of the module calls above it. The
	// count and for_each of those calls decide whether it exists at all.
	e.varEdits, e.varSet = e.tfvars(a.plan.Variables)
	e.planVars = a.plan.Variables
	for _, ch := range r.Changes {
		if ch.Edit != nil || !acts(ch) {
			continue
		}
		module, key := blockKey(ch)
		var refs []inputRef
		if ch.Location != nil {
			for _, ref := range in.resource(module, key) {
				refs = append(refs, inputRef{module, ref})
			}
		}
		for p := module; p != ""; {
			parent, _ := splitModule(p)
			for _, ref := range in.repetition(p) {
				refs = append(refs, inputRef{parent, ref})
			}
			p = parent
		}
		for _, ref := range refs {
			if ed := e.inputEdit(ref.module, ref.ref, map[string]bool{}); ed != nil {
				ch.Edit = ed
				break
			}
		}
	}
	// A replacement triggered by replace_triggered_by follows from what
	// triggered it, and a data source read during the apply because
	// something it depends on changes follows from that change.
	after := map[string][]string{}
	for _, ch := range r.Changes {
		switch {
		case ch.Reason == "replace_by_triggers":
			if b := a.block(ch); b != nil {
				for _, t := range b.Lifecycle.ReplaceTriggeredBy {
					if parts := graph.Traversal(t); len(parts) >= 2 {
						id := parts[0] + "." + parts[1]
						if m := moduleConfig(ch.Module); m != "" {
							id = m + "." + id
						}
						after[ch.Address] = append(after[ch.Address], id)
					}
				}
			}
		case ch.Action == model.Read && (ch.Reason == "read_because_dependency_pending" || ch.Reason == ""):
			after[ch.Address] = a.waitsFor(ch)
		}
	}
	origins(r, after)
	// Edits are source text, which can hold a secret the plan never marks
	// sensitive: a password in a JSON string, an environment variable. What
	// sounds secret is masked too, and values the plan does mark are
	// redacted with the rest of the report.
	for _, ch := range r.Changes {
		if ch.Edit != nil {
			for i := range ch.Edit.Lines {
				ch.Edit.Lines[i].Text = maskSecretLiteral(ch.Edit.Lines[i].Text)
			}
		}
	}
}

// waitsFor lists the resources a data source waits for without reading a
// value from them: those named in its depends_on, or in that of a module
// call above it. When one of them changes, the read moves to the apply.
func (a *analyzer) waitsFor(ch *model.Change) []string {
	n := a.graph.Nodes[ch.Resource]
	if n == nil {
		return nil
	}
	read := map[string]bool{}
	for attr, refs := range n.Attrs {
		for _, ref := range refs {
			if attr != "depends_on" {
				read[ref.Resource] = true
			}
		}
	}
	var out []string
	for _, ref := range n.Attrs["depends_on"] {
		out = append(out, ref.Resource)
	}
	// What else it waits for comes from the depends_on of the module calls
	// above it, or what their count and for_each read.
	for _, up := range n.Up {
		if !read[up] {
			out = append(out, up)
		}
	}
	return out
}

// origins marks each change that does something with where it comes from:
// an edit, or a cause that comes from one. after maps a change to the
// resources whose changes bring it about without it reading a value from
// them: a replacement to what triggers it (replace_triggered_by), a data
// source read during the apply to what it waits for.
func origins(r *model.Report, after map[string][]string) {
	byAddr := map[string]*model.Change{}
	byResource := map[string][]*model.Change{}
	for _, ch := range r.Changes {
		byAddr[ch.Address] = ch
		byResource[ch.Resource] = append(byResource[ch.Resource], ch)
	}
	known := map[string]bool{}
	var follows func(ch *model.Change) bool
	follows = func(ch *model.Change) bool {
		if v, ok := known[ch.Address]; ok {
			return v
		}
		known[ch.Address] = false // a cycle explains nothing
		v := false
		for _, c := range ch.Causes {
			if up := byAddr[c.Address]; up != nil && (up.Edit != nil || follows(up)) {
				v = true
				break
			}
		}
		for _, id := range after[ch.Address] {
			for _, up := range byResource[id] {
				v = v || acts(up) && (up.Edit != nil || follows(up))
			}
		}
		known[ch.Address] = v
		return v
	}
	for _, ch := range r.Changes {
		switch {
		case !acts(ch):
		case ch.Edit != nil:
			ch.Origin = "edit"
		case follows(ch):
			ch.Origin = "follows"
		}
	}
}

// secretWords are the words in a name that say it holds a secret.
const secretWords = `password|passwd|passphrase|pwd|secret|token|credential|private_?key|api_?key|access_?key|account_?key|shared_?key|signing_?key|connection_?string|authorization`

// secretName matches a name that sounds secret.
var secretName = regexp.MustCompile(`(?i)` + secretWords)

// secretKey finds, anywhere on a line, an argument or key whose name sounds
// secret and what assigns it a value: password = , "api_key": , or
// \"password\": in JSON written inside a string.
var secretKey = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_.-])(?:\\?")?[a-z0-9_.-]*(?:` + secretWords + `)[a-z0-9_.-]*(?:\\?")?\s*[=:]\s*`)

// reference matches the start of a value that names where it comes from
// rather than what it is: var.password, random_password.db.result, a
// function call such as jsonencode(. What a reference starts with (var,
// local, a resource type, a function) is in lower case; a token such as
// a JWT, eyJhbGciOi..., is not.
var reference = regexp.MustCompile(`^[a-z_][a-z0-9_-]*[.\[(]`)

// maskSecretLiteral hides the values written out for secret-sounding
// arguments and keys in a line, wherever they are on it. References and
// lone interpolations are kept: they say where a value comes from, not what
// it is. A function call is kept too, and what it is given is looked at in
// turn: jsonencode({ password = "..." }).
func maskSecretLiteral(line string) string {
	var b strings.Builder
	rest := line
	for {
		loc := secretKey.FindStringIndex(rest)
		if loc == nil {
			break
		}
		b.WriteString(rest[:loc[1]])
		rest = rest[loc[1]:]
		n, masked := secretValue(rest)
		b.WriteString(masked)
		rest = rest[n:]
	}
	b.WriteString(rest)
	return b.String()
}

// secretValue reads the value at the start of s, after a secret-sounding
// key, and says how long it is and how it reads masked. A value it keeps
// has length 0, so what follows is looked at too.
func secretValue(s string) (int, string) {
	hide := `"` + diff.SensitivePlaceholder + `"`
	switch {
	case strings.HasPrefix(s, `\"`):
		// JSON in a string: the value ends at the next escaped quote.
		end := strings.Index(s[2:], `\"`)
		if end < 0 {
			return len(s), `\"` + diff.SensitivePlaceholder
		}
		if keeps(s[2 : 2+end]) {
			return 0, ""
		}
		return 2 + end + 2, `\"` + diff.SensitivePlaceholder + `\"`
	case strings.HasPrefix(s, `"`), strings.HasPrefix(s, "'"):
		quote := s[0]
		for i := 1; i < len(s); i++ {
			switch s[i] {
			case '\\':
				i++
			case quote:
				if keeps(s[1:i]) {
					return 0, ""
				}
				if quote == '\'' {
					hide = "'" + diff.SensitivePlaceholder + "'"
				}
				return i + 1, hide
			}
		}
		// The string goes on past the end of the line.
		return len(s), s[:1] + diff.SensitivePlaceholder
	}
	word := s
	if i := strings.IndexAny(s, " \t,;"); i >= 0 {
		word = s[:i]
	}
	switch {
	case word == "", reference.MatchString(s), strings.HasPrefix(s, "<<"), strings.ContainsAny(s[:1], "{[($"),
		word == "null", word == "true", word == "false":
		return 0, ""
	}
	// A value written without quotes, as in YAML or a shell script: the
	// rest of the line may be part of it.
	return len(s), diff.SensitivePlaceholder
}

// keeps reports whether a quoted value can stay: it is empty, or a single
// interpolation that says where the value comes from.
func keeps(v string) bool {
	return v == "" || strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") && strings.Count(v, "${") == 1 && strings.Count(v, "}") == 1
}

// onlyTagsAll reports whether a diff changes tags_all and nothing but tags.
func onlyTagsAll(root *diff.Node) bool {
	if root == nil {
		return false
	}
	all := false
	for _, n := range root.Children {
		switch {
		case n.Kind == diff.Unchanged:
		case n.Key == "tags_all":
			all = true
		case n.Key != "tags":
			return false
		}
	}
	return all
}

// inputs follows a configuration's references to the variables and
// locals they read.
type inputs struct {
	modules map[string]*plan.ConfigModule // by configuration path
	calls   map[string]*plan.ModuleCall   // by the path of the module each calls
	refs    map[string][]string           // by module path and block key, what each resource reads
}

func newInputs(cfg *plan.Configuration) *inputs {
	in := &inputs{modules: map[string]*plan.ConfigModule{}, calls: map[string]*plan.ModuleCall{}, refs: map[string][]string{}}
	var walk func(path string, m *plan.ConfigModule)
	walk = func(path string, m *plan.ConfigModule) {
		if m == nil {
			return
		}
		in.modules[path] = m
		for _, r := range m.Resources {
			k := r.Type + "." + r.Name
			if r.Mode == "data" {
				k = "data." + k
			}
			refs := refsIn(r.Expressions)
			for _, x := range []*plan.Expression{r.CountExpression, r.ForEachExpression} {
				if x != nil {
					refs = append(refs, x.References...)
				}
			}
			in.refs[path+"|"+k] = refs
		}
		for name, call := range m.ModuleCalls {
			child := "module." + name
			if path != "" {
				child = path + "." + child
			}
			in.calls[child] = call
			walk(child, call.Module)
		}
	}
	if cfg != nil {
		walk("", cfg.RootModule)
	}
	return in
}

// resource lists the references in a resource's arguments, count and
// for_each.
func (in *inputs) resource(module, key string) []string { return in.refs[module+"|"+key] }

// repetition lists the references in the count and for_each of the call to
// a module, which its parent module evaluates.
func (in *inputs) repetition(module string) []string {
	call := in.calls[module]
	if call == nil {
		return nil
	}
	var out []string
	for _, x := range []*plan.Expression{call.CountExpression, call.ForEachExpression} {
		if x != nil {
			out = append(out, x.References...)
		}
	}
	return out
}

// inputRef is a reference and the module it is made in.
type inputRef struct{ module, ref string }

// refsIn gathers the references in an expression tree: an expression, a
// nested block or a list of blocks.
func refsIn(expr any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				if k == "references" {
					out = append(out, rules.Strings(c)...)
					continue
				}
				if k != "constant_value" {
					walk(c)
				}
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(expr)
	return out
}

// inputName reads the variable or local a reference names: "var.azs" for
// var.azs[0], "local.name" for local.name.
func inputName(ref string) string {
	parts := strings.SplitN(ref, ".", 3)
	if len(parts) < 2 || parts[0] != "var" && parts[0] != "local" {
		return ""
	}
	name, _, _ := strings.Cut(parts[1], "[")
	return parts[0] + "." + name
}

// inputEdit finds the edit a reference reaches: to the variable or local it
// names, to a local that one reads, or, for a module's variable, to what
// the module call above it passes in.
func (e *editor) inputEdit(module, ref string, seen map[string]bool) *model.Edit {
	name := inputName(ref)
	if name == "" || seen[module+"|"+name] {
		return nil
	}
	seen[module+"|"+name] = true
	m := e.src.Modules[module]
	if strings.HasPrefix(name, "local.") {
		if m == nil {
			return nil
		}
		if b := m.Inputs[name]; b != nil {
			if ed := e.input(name, b); ed != nil {
				return ed
			}
		}
		for _, r := range m.Locals[strings.TrimPrefix(name, "local.")] {
			if ed := e.inputEdit(module, r, seen); ed != nil {
				return ed
			}
		}
		return nil
	}
	// A root variable takes what a variable file in effect sets, and its
	// default only when the plan's value is the default: a file that did
	// not change, or -var, may set it.
	if module == "" {
		if ed := e.varEdits[name]; ed != nil {
			return ed
		}
		if e.varSet[name] || !e.usesDefault(strings.TrimPrefix(name, "var.")) {
			return nil
		}
	}
	// A module's variable takes what its call passes in, and its default
	// only when the call passes nothing.
	if call := e.in.calls[module]; call != nil {
		if arg, ok := call.Expressions[strings.TrimPrefix(name, "var.")]; ok {
			parent, _ := splitModule(module)
			for _, r := range refsIn(arg) {
				if ed := e.inputEdit(parent, r, seen); ed != nil {
					return ed
				}
			}
			return nil
		}
	}
	if m == nil {
		return nil
	}
	b := m.Inputs[name]
	if b == nil {
		return nil
	}
	ed := e.input(name, b)
	if e.sensitiveVars(module)[strings.TrimPrefix(name, "var.")] {
		var was *source.Block
		if f := e.files[b.File]; f != nil {
			was = e.oldModule(f).Inputs[name]
		}
		hideAll(ed, b, was)
	}
	return ed
}

// usesDefault reports whether the plan's value for a root variable is the
// default it is declared with, or the plan does not say.
func (e *editor) usesDefault(name string) bool {
	v, ok := e.planVars[name]
	root := e.in.modules[""]
	if !ok || root == nil || root.Variables[name] == nil {
		return true
	}
	return sameDefault(v.Value, root.Variables[name].Default)
}

// sameDefault compares a variable's value in the plan with its default as
// written. The plan holds the value after conversion to the variable's
// type, so 3 and "3" are the same, an attribute a default object leaves out
// is null, and a set may come back in another order.
func sameDefault(v, def any) bool {
	switch x := v.(type) {
	case map[string]any:
		y, ok := def.(map[string]any)
		if !ok {
			return false
		}
		for k, xv := range x {
			if !sameDefault(xv, y[k]) {
				return false
			}
		}
		for k, yv := range y {
			if _, ok := x[k]; !ok && yv != nil {
				return false
			}
		}
		return true
	case []any:
		y, ok := def.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		ordered, used := true, make([]bool, len(y))
		for i := range x {
			ordered = ordered && sameDefault(x[i], y[i])
		}
		if ordered {
			return true
		}
	next:
		for _, xv := range x {
			for j, yv := range y {
				if !used[j] && sameDefault(xv, yv) {
					used[j] = true
					continue next
				}
			}
			return false
		}
		return true
	}
	if diff.Equal(v, def) {
		return true
	}
	a, aok := scalarText(v)
	b, bok := scalarText(def)
	return aok && bok && a == b
}

// scalarText writes a string, number or bool as text.
func scalarText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

// tfvars reads the variable files that changed, wherever they are: one
// passed with -var-file can live in a directory of its own. A file counts
// for a variable when the value it sets is the value the plan used;
// prod.tfvars beside staging.tfvars only counts for the plan made with it.
func (e *editor) tfvars(vars map[string]plan.Variable) (map[string]*model.Edit, map[string]bool) {
	edits, set := map[string]*model.Edit{}, map[string]bool{}
	paths := make([]string, 0, len(e.files))
	for p := range e.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		f := e.files[p]
		if f.Deleted || f.New == nil || !(strings.HasSuffix(p, ".tfvars") || strings.HasSuffix(p, ".tfvars.json")) {
			continue
		}
		attrs := varFile(p, f.New)
		now, was := varLines(attrs), varLines(varFile(p, f.Old))
		for name, attr := range attrs {
			v, ok := vars[name]
			if !ok || !sameValue(attr.Expr, v.Value) {
				continue
			}
			set["var."+name] = true
			b := &source.Block{File: p, Line: attr.Range.Start.Line, EndLine: attr.Range.End.Line}
			if ed := e.input("var."+name, b); ed != nil {
				e.hideVarFile(ed, name, now, was)
				edits["var."+name] = ed
			}
		}
	}
	return edits, set
}

// varFile reads the variables a variable file sets. The lines are joined
// with the newline the last one ends with: a heredoc's closing marker is
// only one when a newline follows it.
func varFile(name string, lines []string) hcl.Attributes {
	if lines == nil {
		return nil
	}
	src := []byte(strings.Join(lines, "\n") + "\n")
	var file *hcl.File
	if strings.HasSuffix(name, ".json") {
		file, _ = hclparse.NewParser().ParseJSON(src, name)
	} else {
		file, _ = hclparse.NewParser().ParseHCL(src, name)
	}
	if file == nil || file.Body == nil {
		return nil
	}
	attrs, _ := file.Body.JustAttributes()
	return attrs
}

// varLines maps each line of a variable file to the variable whose value
// it is part of.
func varLines(attrs hcl.Attributes) map[int]spot {
	out := map[int]spot{}
	for name, a := range attrs {
		for n := a.Range.Start.Line; n <= a.Range.End.Line; n++ {
			out[n] = spot{name: name, first: n == a.Range.Start.Line}
		}
	}
	return out
}

// hideVarFile masks the lines of an edit to a variable file that set a
// variable declared sensitive, now or in the base: every line when the
// edit's own variable is one, else the lines of another such variable
// that a hunk carried along. now and was map the file's lines as it is
// and as it was.
func (e *editor) hideVarFile(ed *model.Edit, name string, now, was map[int]spot) {
	secret := e.sensitiveVars("")
	for i := range ed.Lines {
		l := &ed.Lines[i]
		at := now
		if l.Op == "-" {
			at = was
		}
		if s, ok := at[l.Line]; secret[name] || ok && secret[s.name] {
			l.Text = hidden(l.Text, s)
		}
	}
}

// sameValue reports whether an expression in a variable file evaluates to
// the value the plan used.
func sameValue(expr hcl.Expression, want any) bool {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || !v.IsWhollyKnown() {
		return false
	}
	b, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var got any
	if dec.Decode(&got) != nil {
		return false
	}
	return diff.Equal(got, want)
}

// input is the edit to a variable block or a local's definition, or nil
// when git reports no change to it.
func (e *editor) input(name string, b *source.Block) *model.Edit {
	f := e.files[b.File]
	if f == nil {
		return nil
	}
	id := b.File + ":" + strconv.Itoa(b.Line) + ":" + name
	if ed, ok := e.memo[id]; ok {
		return ed
	}
	first, last := b.Line, max(b.EndLine, b.Line)
	inside := func(n int) bool { return n >= first && n <= last }
	var lines []model.EditLine
	for _, h := range f.Hunks {
		if !touches(h, span{first: first, last: last}) {
			continue
		}
		j := 0
		for _, l := range h.Lines {
			keep := false
			switch {
			case l.Op == "+":
				keep = inside(l.Num)
			case h.NewLines > 0:
				keep = inside(h.NewStart + min(j, h.NewLines-1))
				j++
			default:
				keep = first <= h.NewStart && h.NewStart < last
			}
			if keep {
				lines = append(lines, model.EditLine{Op: l.Op, Line: l.Num, Text: l.Text})
			}
		}
	}
	ed := finish(f.Path, editInput, lines)
	if ed != nil {
		ed.Input = name
	}
	e.memo[id] = ed
	return ed
}

// acts reports whether a change does anything, a move or import included.
func acts(ch *model.Change) bool {
	return ch.Action != model.NoOp || ch.Moved || ch.Importing
}

func blockKey(ch *model.Change) (module, key string) {
	key = ch.Type + "." + ch.Name
	if ch.Mode == "data" {
		key = "data." + key
	}
	return moduleConfig(ch.Module), key
}

// editor matches the hunks of a git diff with the blocks the source index
// knows.
type editor struct {
	src   *source.Index
	in    *inputs
	files map[string]*gitdiff.File // by path relative to the Terraform directory
	spans map[string][]span        // per file: the blocks in it now, in order
	olds  map[string]*source.Module
	oldAt map[string]map[int]string // per file: the block that started at each line in the base
	memo  map[string]*model.Edit    // by file and first line of a block
	calls map[string]*model.Edit    // by module path
	gone  map[string]*model.Edit    // removed blocks, by directory and key
	heads map[string]map[string]removedAt
	// renamed holds, by file and line in the base, the edit of the block an
	// old block became when it was renamed without a moved block.
	renamed map[string]*model.Edit
	// varEdits are root variables whose value in a variable file in effect
	// changed; varSet are those such a file sets, whose defaults are moot.
	varEdits map[string]*model.Edit
	varSet   map[string]bool
	planVars map[string]plan.Variable
	// secrets names, by directory and block key, the arguments of a block
	// that hold a secret (see markSecrets); sensitive names, by module, the
	// variables declared sensitive.
	secrets   map[string]map[string]bool
	sensitive map[string]map[string]bool
}

// span is a resource, data or module block as it is now.
type span struct {
	module, key string
	first, last int
}

// oldBlock is a block as it was in the base.
type oldBlock struct {
	file        *gitdiff.File
	key         string
	block       *source.Block
	first, last int
	lines       []string
}

func newEditor(src *source.Index, d *gitdiff.Diff, in *inputs) *editor {
	e := &editor{src: src, in: in, files: map[string]*gitdiff.File{}, spans: map[string][]span{},
		olds: map[string]*source.Module{}, oldAt: map[string]map[int]string{}, memo: map[string]*model.Edit{},
		calls: map[string]*model.Edit{}, gone: map[string]*model.Edit{}, heads: map[string]map[string]removedAt{},
		renamed: map[string]*model.Edit{}, secrets: map[string]map[string]bool{}, sensitive: map[string]map[string]bool{}}
	for _, f := range d.Files {
		e.files[f.Path] = f
	}
	modules := make([]string, 0, len(src.Modules))
	for p := range src.Modules {
		modules = append(modules, p)
	}
	sort.Strings(modules)
	seen := map[string]bool{}
	for _, p := range modules {
		m := src.Modules[p]
		keys := make([]string, 0, len(m.Blocks))
		for k := range m.Blocks {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b := m.Blocks[k]
			// Two calls to one local module share its blocks.
			id := b.File + ":" + strconv.Itoa(b.Line)
			if e.files[b.File] == nil || seen[id] {
				continue
			}
			seen[id] = true
			e.spans[b.File] = append(e.spans[b.File], span{module: p, key: k, first: b.Line, last: max(b.EndLine, b.Line)})
		}
	}
	for _, spans := range e.spans {
		sort.Slice(spans, func(i, j int) bool { return spans[i].first < spans[j].first })
	}
	return e
}

// block is the edit to a block that exists now, or nil when git reports
// no change to it.
func (e *editor) block(module, key string) *model.Edit {
	b := e.src.Block(module, key)
	if b == nil || e.files[b.File] == nil {
		return nil
	}
	id := b.File + ":" + strconv.Itoa(b.Line)
	if ed, ok := e.memo[id]; ok {
		return ed
	}
	f := e.files[b.File]
	sp := span{module: module, key: key, first: b.Line, last: max(b.EndLine, b.Line)}
	var hunks []*gitdiff.Hunk
	for _, h := range f.Hunks {
		if touches(h, sp) {
			hunks = append(hunks, h)
		}
	}
	var ed *model.Edit
	switch {
	case len(hunks) == 0:
	case f.New != nil && sp.last <= len(f.New):
		ed = e.fromContent(f, sp, hunks)
	default:
		// The block as it was is not known, so nor is which removed lines
		// held a secret.
		ed = e.fromHunks(f, sp, hunks)
		hideArgs(ed, e.secretArgs(module, key), b, nil)
	}
	e.memo[id] = ed
	return ed
}

// touches reports whether a hunk changes the block at sp. A deletion
// touches it when it happens inside the block. An insertion that only gives
// it closing braces or blank lines is git lining a new block up against
// this one, not an edit to it.
func touches(h *gitdiff.Hunk, sp span) bool {
	if h.NewLines == 0 {
		return sp.first <= h.NewStart && h.NewStart < sp.last
	}
	if h.NewStart+h.NewLines-1 < sp.first || h.NewStart > sp.last {
		return false
	}
	if h.OldLines > 0 {
		return true
	}
	for _, l := range h.Lines {
		if l.Op == "+" && l.Num >= sp.first && l.Num <= sp.last && !trivial(l.Text) {
			return true
		}
	}
	return false
}

// trivial matches lines a diff can slide between neighbouring blocks:
// blank ones and closing brackets.
func trivial(s string) bool {
	return strings.Trim(s, " \t}]),") == ""
}

// fromContent diffs the block with the block it was in the base. git
// matches lines across the whole file, so where an edit runs up to a new
// block it can pair this block's closing lines with the new block's; a
// diff of the two blocks alone says what changed in this one.
func (e *editor) fromContent(f *gitdiff.File, sp span, hunks []*gitdiff.Hunk) *model.Edit {
	now := f.New[sp.first-1 : sp.last]
	b := e.src.Block(sp.module, sp.key)
	secret := e.secretArgs(sp.module, sp.key)
	old := e.counterpart(f, sp)
	lines := e.leading(f, sp, hunks, old)
	if old == nil {
		for i, t := range now {
			lines = append(lines, model.EditLine{Op: "+", Line: sp.first + i, Text: t})
		}
		ed := finish(f.Path, editAdded, lines)
		hideArgs(ed, secret, b, nil)
		return ed
	}
	n := len(lines)
	oi, ni := old.first, sp.first
	for _, op := range diff.LineEdits(old.lines, now) {
		switch op.Op {
		case "-":
			lines = append(lines, model.EditLine{Op: "-", Line: oi, Text: op.Text})
			oi++
		case "+":
			lines = append(lines, model.EditLine{Op: "+", Line: ni, Text: op.Text})
			ni++
		default:
			oi++
			ni++
		}
	}
	if len(lines) == n {
		// The block reads as it did, as when it only moved to another file.
		return nil
	}
	ed := finish(f.Path, editEdited, lines)
	// A block renamed without a moved block held what the plan says of the
	// old name as well as the new.
	hideArgs(ed, union(secret, e.secrets[path.Dir(old.file.Path)+"|"+old.key]), b, old.block)
	if old.file != f && ed != nil {
		// The block came from another file; its line numbers would mislead
		// next to this one's. Every removed line is the block's: those just
		// above a block only count from its own file.
		for i := range ed.Lines {
			if ed.Lines[i].Op == "-" {
				ed.Lines[i].Line = 0
			}
		}
	}
	if old.file == f && !e.exists(sp.module, e.keyAt(f, old.first)) {
		e.renamed[f.Path+":"+strconv.Itoa(old.first)] = ed
	}
	return ed
}

// counterpart finds the block as it was in the base: under the same name
// in this file or another in its directory, under the name a moved block
// gives, or, for a block renamed without one, the block of the same type
// whose lines git matched with this one's.
func (e *editor) counterpart(f *gitdiff.File, sp span) *oldBlock {
	if ob := e.oldBlock(f, sp.key); ob != nil {
		return ob
	}
	if m := e.src.Modules[sp.module]; m != nil {
		for _, mv := range m.Moved {
			if unkeyed(mv.To) == sp.key {
				if ob := e.oldBlock(f, unkeyed(mv.From)); ob != nil {
					return ob
				}
			}
		}
	}
	for _, g := range e.inDir(path.Dir(f.Path)) {
		if g != f {
			if ob := e.oldBlock(g, sp.key); ob != nil {
				return ob
			}
		}
	}
	votes := map[string]int{}
	best := ""
	for n := sp.first; n <= sp.last; n++ {
		m, ok := f.OldLine(n)
		if !ok {
			continue
		}
		for key, b := range e.oldBlocks(f) {
			if m >= b.Line && m <= max(b.EndLine, b.Line) && kindOf(key) == kindOf(sp.key) && !e.exists(sp.module, key) {
				votes[key]++
				if votes[key] > votes[best] || votes[key] == votes[best] && key < best {
					best = key
				}
			}
		}
	}
	if best == "" {
		return nil
	}
	return e.oldBlock(f, best)
}

// oldModule reads a file as it was in the base: its blocks, and the
// variables, locals and providers in it. It is empty when that is not
// known.
func (e *editor) oldModule(f *gitdiff.File) *source.Module {
	if m, ok := e.olds[f.Path]; ok {
		return m
	}
	m := &source.Module{Blocks: map[string]*source.Block{}, Inputs: map[string]*source.Block{}}
	if f.Old != nil {
		name := f.Path
		if f.OldPath != "" {
			name = f.OldPath
		}
		m = source.Parse(name, []byte(strings.Join(f.Old, "\n")+"\n"))
	}
	e.olds[f.Path] = m
	return m
}

// oldBlocks reads the blocks of a file as it was in the base.
func (e *editor) oldBlocks(f *gitdiff.File) map[string]*source.Block { return e.oldModule(f).Blocks }

func (e *editor) oldBlock(f *gitdiff.File, key string) *oldBlock {
	b := e.oldBlocks(f)[key]
	if b == nil {
		return nil
	}
	last := max(b.EndLine, b.Line)
	if b.Line < 1 || last > len(f.Old) {
		return nil
	}
	return &oldBlock{file: f, key: key, block: b, first: b.Line, last: last, lines: f.Old[b.Line-1 : last]}
}

// keyAt names the block that started at a line of a file in the base.
func (e *editor) keyAt(f *gitdiff.File, line int) string { return e.startsAt(f)[line] }

// startsAt maps the lines blocks started at in a file in the base to their
// keys.
func (e *editor) startsAt(f *gitdiff.File) map[int]string {
	if at, ok := e.oldAt[f.Path]; ok {
		return at
	}
	at := map[int]string{}
	for key, b := range e.oldBlocks(f) {
		at[b.Line] = key
	}
	e.oldAt[f.Path] = at
	return at
}

// exists reports whether a module has a block of that name now.
func (e *editor) exists(module, key string) bool {
	m := e.src.Modules[module]
	return m != nil && m.Blocks[key] != nil
}

// leading collects the changed lines just above a block in the hunks that
// change it: a comment, or the moved or import block that goes with it.
func (e *editor) leading(f *gitdiff.File, sp span, hunks []*gitdiff.Hunk, old *oldBlock) []model.EditLine {
	floor := 0
	for _, s := range e.spans[f.Path] {
		if s.last < sp.first {
			floor = max(floor, s.last)
		}
	}
	oldFloor := 0
	if old != nil && old.file == f {
		for _, b := range e.oldBlocks(f) {
			if last := max(b.EndLine, b.Line); last < old.first {
				oldFloor = max(oldFloor, last)
			}
		}
	}
	var out []model.EditLine
	for _, h := range hunks {
		for _, l := range h.Lines {
			switch {
			case l.Op == "+" && l.Num > floor && l.Num < sp.first:
			case l.Op == "-" && old != nil && old.file == f && l.Num > oldFloor && l.Num < old.first:
			default:
				continue
			}
			out = append(out, model.EditLine{Op: l.Op, Line: l.Num, Text: l.Text})
		}
	}
	return out
}

// fromHunks takes the edit from the hunks alone, for when the file's
// content is not known. A hunk that also changes another block gives this
// one only the lines that fall within it.
func (e *editor) fromHunks(f *gitdiff.File, sp span, hunks []*gitdiff.Hunk) *model.Edit {
	var lines []model.EditLine
	for _, h := range hunks {
		shared := false
		for _, s := range e.spans[f.Path] {
			if s.first != sp.first && touches(h, s) {
				shared = true
			}
		}
		if !shared {
			for _, l := range h.Lines {
				lines = append(lines, model.EditLine{Op: l.Op, Line: l.Num, Text: l.Text})
			}
			continue
		}
		lines = append(lines, e.clip(f, sp, h)...)
	}
	// The block is new when its first line is, unless that line replaced
	// the header of another block of the same type: a rename.
	kind, firstAdded := editEdited, false
	for _, l := range lines {
		if l.Op == "+" && l.Line == sp.first {
			firstAdded = true
		}
	}
	if firstAdded {
		kind = editAdded
		for _, l := range lines {
			if l.Op == "-" {
				if k, ok := headerKey(l.Text); ok && kindOf(k) == kindOf(sp.key) {
					kind = editEdited
				}
			}
		}
	}
	return finish(f.Path, kind, lines)
}

// clip keeps the lines of a hunk that belong to the block at sp: those
// from the end of the block before it to its own end. Removed lines go
// with the added line in the same place, or with the block the removal
// happened in when nothing was added.
func (e *editor) clip(f *gitdiff.File, sp span, h *gitdiff.Hunk) []model.EditLine {
	floor := 0
	for _, s := range e.spans[f.Path] {
		if s.last < sp.first {
			floor = max(floor, s.last)
		}
	}
	inside := func(n int) bool { return n > floor && n <= sp.last }
	var out []model.EditLine
	j := 0
	for _, l := range h.Lines {
		keep := false
		switch {
		case l.Op == "+":
			keep = inside(l.Num)
		case h.NewLines > 0:
			keep = inside(h.NewStart + min(j, h.NewLines-1))
			j++
		default:
			keep = floor <= h.NewStart && h.NewStart < sp.last
		}
		if keep {
			out = append(out, model.EditLine{Op: l.Op, Line: l.Num, Text: l.Text})
		}
	}
	// What git lined up across the boundary is closing braces and blank
	// lines; they belong to neither block's edit.
	for len(out) > 0 && trivial(out[0].Text) {
		out = out[1:]
	}
	for len(out) > 0 && trivial(out[len(out)-1].Text) {
		out = out[:len(out)-1]
	}
	return out
}

// removed finds a block that no longer exists among the lines removed from
// the files in its module's directory. kind is "resource", "data" or
// "module"; labels are its type and name, or the module's name.
func (e *editor) removed(dir, kind string, labels ...string) *model.Edit {
	key := strings.Join(labels, ".")
	if kind != "resource" {
		key = kind + "." + key
	}
	id := dir + " " + key
	if ed, ok := e.gone[id]; ok {
		return ed
	}
	var ed *model.Edit
	if at, ok := e.headers(dir)[key]; ok {
		f, l := at.file, at.hunk.Lines[at.i]
		if r := e.renamed[f.Path+":"+strconv.Itoa(l.Num)]; r != nil {
			c := *r
			c.Kind = editRemoved
			ed = &c
		} else {
			var lines []model.EditLine
			var was *source.Block
			if ob := e.oldBlock(f, e.keyAt(f, l.Num)); ob != nil {
				for k, t := range ob.lines {
					lines = append(lines, model.EditLine{Op: "-", Line: ob.first + k, Text: t})
				}
				was = ob.block
			}
			if lines == nil {
				lines = removedLines(at.hunk.Lines[at.i:])
			}
			ed = finish(f.Path, editRemoved, lines)
			hideArgs(ed, e.secrets[dir+"|"+key], nil, was)
		}
	}
	e.gone[id] = ed
	return ed
}

// removedAt is where git removed the first line of a block.
type removedAt struct {
	file *gitdiff.File
	hunk *gitdiff.Hunk
	i    int // the line's index in the hunk
}

// headers finds, once for a directory, the blocks whose first line the
// diff removed, by key. A plan can destroy thousands of them, each looked
// up here. The file as it was says which lines start a block, in .tf.json
// too; a line that reads like the start of one stands in for what it does
// not say.
func (e *editor) headers(dir string) map[string]removedAt {
	if idx, ok := e.heads[dir]; ok {
		return idx
	}
	idx := map[string]removedAt{}
	for _, f := range e.inDir(dir) {
		starts := e.startsAt(f)
		for _, h := range f.Hunks {
			for i, l := range h.Lines {
				if l.Op != "-" {
					continue
				}
				key, ok := starts[l.Num]
				if !ok {
					key, ok = headerKey(l.Text)
				}
				if _, seen := idx[key]; ok && !seen {
					idx[key] = removedAt{file: f, hunk: h, i: i}
				}
			}
		}
	}
	e.heads[dir] = idx
	return idx
}

// removedLines reads a removed block from its header to its closing
// brace, among the lines a hunk removed.
func removedLines(lines []gitdiff.Line) []model.EditLine {
	var out []model.EditLine
	depth := 0
	for _, l := range lines {
		if l.Op != "-" {
			break
		}
		out = append(out, model.EditLine{Op: "-", Line: l.Num, Text: l.Text})
		depth += braces(l.Text)
		if depth <= 0 {
			break
		}
	}
	return out
}

// braces counts the braces a line opens, less those it closes, outside
// quoted strings and comments.
func braces(s string) int {
	n, quoted := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quoted && c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case quoted:
		case c == '#' || c == '/' && i+1 < len(s) && s[i+1] == '/':
			return n
		case c == '{':
			n++
		case c == '}':
			n--
		}
	}
	return n
}

// moduleCall is the edit to the innermost call, on the way down to a
// module, that changed: what a change inside it follows from when its own
// block did not change.
func (e *editor) moduleCall(module string) *model.Edit {
	if ed, ok := e.calls[module]; ok {
		return ed
	}
	var ed *model.Edit
	for p := module; p != "" && ed == nil; p, _ = splitModule(p) {
		parent, name := splitModule(p)
		pm := e.src.Modules[parent]
		if pm == nil {
			continue
		}
		var found *model.Edit
		if pm.Blocks["module."+name] != nil {
			found = e.block(parent, "module."+name)
		} else {
			found = e.removed(slashDir(pm.Dir), "module", name)
		}
		if found != nil {
			c := *found
			c.Kind, c.Module = editModule, p
			ed = &c
		}
	}
	e.calls[module] = ed
	return ed
}

// provider is the edit to the root module's block for a provider, which
// its default_tags are set in.
func (e *editor) provider(name string) *model.Edit {
	m := e.src.Modules[""]
	if m == nil || m.Inputs["provider."+name] == nil {
		return nil
	}
	ed := e.input("provider."+name, m.Inputs["provider."+name])
	if ed == nil {
		return nil
	}
	return &model.Edit{File: ed.File, Kind: editProvider, Input: ed.Input, Lines: ed.Lines, Omitted: ed.Omitted}
}

// splitModule splits "module.a.module.b" into "module.a" and "b".
func splitModule(p string) (parent, name string) {
	if i := strings.LastIndex(p, ".module."); i >= 0 {
		return p[:i], p[i+len(".module."):]
	}
	return "", strings.TrimPrefix(p, "module.")
}

// inDir lists the changed files directly in a directory, by path.
func (e *editor) inDir(dir string) []*gitdiff.File {
	var out []*gitdiff.File
	for p, f := range e.files {
		if path.Dir(p) == dir {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func slashDir(dir string) string { return path.Clean(filepath.ToSlash(dir)) }

// finish trims blank lines from the ends of an edit and caps its length.
func finish(file, kind string, lines []model.EditLine) *model.Edit {
	for len(lines) > 0 && strings.TrimSpace(lines[0].Text) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1].Text) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}
	ed := &model.Edit{File: file, Kind: kind, Lines: lines}
	if len(lines) > maxEditLines {
		ed.Lines, ed.Omitted = lines[:maxEditLines], len(lines)-maxEditLines
	}
	return ed
}

// unkeyed drops the instance key from an address in a moved block.
func unkeyed(addr string) string {
	if i := strings.IndexByte(addr, '['); i >= 0 {
		return addr[:i]
	}
	return addr
}

// kindOf is the part of a block key before its name: the resource type,
// "data." and the type, or "module".
func kindOf(key string) string {
	if i := strings.LastIndexByte(key, '.'); i >= 0 {
		return key[:i]
	}
	return key
}

var header = regexp.MustCompile(`^\s*(resource|data|module)\s+"?([\w-]+)"?(?:\s+"?([\w-]+)"?)?\s*\{`)

// headerKey reads the block key from the first line of a resource, data
// or module block.
func headerKey(s string) (string, bool) {
	m := header.FindStringSubmatch(s)
	switch {
	case m == nil:
		return "", false
	case m[1] == "module":
		return "module." + m[2], m[3] == ""
	case m[3] == "":
		return "", false
	case m[1] == "data":
		return "data." + m[2] + "." + m[3], true
	}
	return m[2] + "." + m[3], true
}
