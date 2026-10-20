// Package source reads a configuration's .tf files to learn what the JSON
// plan leaves out: where each block is defined, what locals refer to, and
// lifecycle settings such as prevent_destroy and create_before_destroy.
//
// Everything here is best effort. A plan can always be explained without
// source; reading it only makes the explanation sharper.
package source

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	hcljson "github.com/hashicorp/hcl/v2/json"
	"github.com/zclconf/go-cty/cty"

	"github.com/Jeecabs/interragate/internal/plan"
)

// Index is what was learned from the source of every module.
type Index struct {
	Root    string
	Modules map[string]*Module // keyed by configuration module path
	// Locked maps provider source addresses to the version in
	// .terraform.lock.hcl.
	Locked map[string]string
}

// Module is one module's source.
type Module struct {
	Path   string // "" for the root, "module.a.module.b" below it
	Dir    string // relative to the root module's directory
	Locals map[string][]string
	// Blocks holds resource, data and module blocks: "aws_vpc.main",
	// "data.aws_ami.ubuntu", "module.network".
	Blocks map[string]*Block
	Moved  []Moved
	// Inputs holds where each variable block, local value and provider
	// block is written: "var.azs" covers the variable block, "local.name"
	// the lines of the local's definition, "provider.aws" the first aws
	// provider block.
	Inputs map[string]*Block
}

// Block is a resource, data or module block.
type Block struct {
	File      string    `json:"file"`
	Line      int       `json:"line"`
	EndLine   int       `json:"end_line,omitempty"`
	Lifecycle Lifecycle `json:"lifecycle,omitzero"`
	// Provisioners are the commands the block runs when it is created or,
	// for destroy-time ones, destroyed.
	Provisioners []Provisioner `json:"provisioners,omitempty"`
	// Sensitive is set on a variable block that says sensitive = true.
	Sensitive bool `json:"sensitive,omitempty"`
	// Args are the block's arguments and nested blocks, in the order they
	// are written, so a line of the block can be traced to the argument it
	// is part of.
	Args []Arg `json:"-"`
}

// Arg is an argument or nested block within a block, and the lines it
// spans: a heredoc or a multi-line object takes more than one.
type Arg struct {
	// Name is the argument's name, or a nested block's type (a dynamic
	// block's label).
	Name          string
	Line, EndLine int
	// Literal is set when the value is written out, at least in part: a
	// string, number or heredoc, or anything in .tf.json. A value that only
	// refers to others, such as var.password, is not, unless a comment on
	// its lines could say anything.
	Literal bool
}

// Provisioner is a provisioner block: what kind, when it runs and the
// command it runs, as written in the source.
type Provisioner struct {
	Type    string `json:"type"`           // local-exec, remote-exec or file
	Destroy bool   `json:"destroy"`        // when = destroy
	Command string `json:"command"`        // local-exec's command, or remote-exec's first inline command
	More    int    `json:"more,omitempty"` // further inline commands
}

// Lifecycle holds literal lifecycle settings. Pointers are nil when unset or
// not a literal.
type Lifecycle struct {
	PreventDestroy      *bool    `json:"prevent_destroy,omitempty"`
	CreateBeforeDestroy *bool    `json:"create_before_destroy,omitempty"`
	IgnoreChanges       []string `json:"ignore_changes,omitempty"`
	ReplaceTriggeredBy  []string `json:"replace_triggered_by,omitempty"`
}

// Moved is a moved block.
type Moved struct {
	From, To string
	File     string
	Line     int
}

// Load reads the root module in dir and every module the plan's
// configuration calls whose source can be found locally. Source is only
// ever a bonus, so a panic in the HCL library becomes an error.
func Load(dir string, cfg *plan.Configuration) (idx *Index, err error) {
	defer func() {
		if r := recover(); r != nil {
			idx, err = nil, fmt.Errorf("reading configuration: %v", r)
		}
	}()
	return load(dir, cfg)
}

func load(dir string, cfg *plan.Configuration) (*Index, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	idx := &Index{Root: root, Modules: map[string]*Module{}}
	dirs := moduleDirs(root, cfg)
	parser := hclparse.NewParser()
	for _, path := range sortedKeys(dirs) {
		m := &Module{Path: path, Dir: dirs[path], Locals: map[string][]string{}, Blocks: map[string]*Block{}, Inputs: map[string]*Block{}}
		if err := m.parse(parser, root); err != nil {
			if path == "" {
				return nil, err
			}
			continue
		}
		idx.Modules[path] = m
	}
	idx.Locked = lockedVersions(parser, filepath.Join(root, ".terraform.lock.hcl"))
	return idx, nil
}

// LockedVersions reads a .terraform.lock.hcl file: provider source
// addresses mapped to the versions locked for them. A missing or broken
// file gives an empty map.
func LockedVersions(path string) map[string]string {
	return lockedVersions(hclparse.NewParser(), path)
}

// LockedVersionsIn reads the provider versions in the content of a
// dependency lock file, such as the one in a git commit.
func LockedVersionsIn(content []byte) map[string]string {
	f, diags := hclparse.NewParser().ParseHCL(content, ".terraform.lock.hcl")
	if diags.HasErrors() || f == nil {
		return map[string]string{}
	}
	return versionsIn(f)
}

func lockedVersions(parser *hclparse.Parser, path string) map[string]string {
	f, diags := parser.ParseHCLFile(path)
	if diags.HasErrors() || f == nil {
		return map[string]string{}
	}
	return versionsIn(f)
}

func versionsIn(f *hcl.File) map[string]string {
	out := map[string]string{}
	content, _, _ := f.Body.PartialContent(&hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "provider", LabelNames: []string{"source"}}}})
	if content == nil {
		return out
	}
	for _, b := range content.Blocks {
		attrs, _ := b.Body.JustAttributes()
		if a := attrs["version"]; a != nil {
			if v, d := a.Expr.Value(nil); !d.HasErrors() && v.Type().FriendlyName() == "string" {
				out[b.Labels[0]] = v.AsString()
			}
		}
	}
	return out
}

// Locals resolves a local value to the references in its expression.
func (idx *Index) Locals(module, name string) ([]string, bool) {
	if idx == nil {
		return nil, false
	}
	m := idx.Modules[module]
	if m == nil {
		return nil, false
	}
	refs, ok := m.Locals[name]
	return refs, ok
}

// Block finds the source of a resource given its configuration address.
func (idx *Index) Block(module, local string) *Block {
	if idx == nil {
		return nil
	}
	m := idx.Modules[module]
	if m == nil {
		return nil
	}
	return m.Blocks[local]
}

// moduleDirs maps configuration module paths to directories relative to
// root, using the module manifest written by init when it exists.
// ModuleDirs maps module paths ("" for the root, "module.network") to
// their directories relative to root: from the module manifest init wrote
// when there is one, else from the local sources in the configuration.
func ModuleDirs(root string, cfg *plan.Configuration) map[string]string {
	return moduleDirs(root, cfg)
}

func moduleDirs(root string, cfg *plan.Configuration) map[string]string {
	dirs := map[string]string{"": "."}
	dataDir := os.Getenv("TF_DATA_DIR")
	if dataDir == "" {
		dataDir = ".terraform"
	}
	if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(root, dataDir)
	}
	if data, err := os.ReadFile(filepath.Join(dataDir, "modules", "modules.json")); err == nil {
		var manifest struct {
			Modules []struct{ Key, Dir string }
		}
		if json.Unmarshal(data, &manifest) == nil {
			for _, m := range manifest.Modules {
				if m.Key == "" {
					continue
				}
				dirs["module."+strings.ReplaceAll(m.Key, ".", ".module.")] = filepath.Clean(m.Dir)
			}
			return dirs
		}
	}
	if cfg == nil || cfg.RootModule == nil {
		return dirs
	}
	var walk func(prefix, dir string, m *plan.ConfigModule)
	walk = func(prefix, dir string, m *plan.ConfigModule) {
		for name, call := range m.ModuleCalls {
			if !strings.HasPrefix(call.Source, "./") && !strings.HasPrefix(call.Source, "../") {
				continue
			}
			path := "module." + name
			if prefix != "" {
				path = prefix + "." + path
			}
			child := filepath.Clean(filepath.Join(dir, call.Source))
			dirs[path] = child
			if call.Module != nil {
				walk(path, child, call.Module)
			}
		}
	}
	walk("", ".", cfg.RootModule)
	return dirs
}

// Outline reads the resource, data and module blocks of one file held in
// memory, keyed as in Module.Blocks. It is for source that is not on disk,
// such as a file as it was in an earlier commit; a syntax error only loses
// the blocks it hides.
func Outline(name string, src []byte) map[string]*Block { return Parse(name, src).Blocks }

// Parse reads one file held in memory as if it were a module of its own:
// its blocks, and the variables, locals and providers in Inputs.
func Parse(name string, src []byte) (m *Module) {
	empty := func() *Module {
		return &Module{Locals: map[string][]string{}, Blocks: map[string]*Block{}, Inputs: map[string]*Block{}}
	}
	defer func() {
		if r := recover(); r != nil {
			m = empty()
		}
	}()
	parser := hclparse.NewParser()
	var f *hcl.File
	if strings.HasSuffix(name, ".json") {
		f, _ = parser.ParseJSON(src, name)
	} else {
		f, _ = parser.ParseHCL(src, name)
	}
	m = empty()
	if f != nil && f.Body != nil {
		m.read(f.Body, name, false, f.Bytes)
	}
	return m
}

var fileSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "resource", LabelNames: []string{"type", "name"}},
		{Type: "data", LabelNames: []string{"type", "name"}},
		{Type: "ephemeral", LabelNames: []string{"type", "name"}},
		{Type: "module", LabelNames: []string{"name"}},
		{Type: "locals"},
		{Type: "moved"},
		{Type: "variable", LabelNames: []string{"name"}},
		{Type: "provider", LabelNames: []string{"name"}},
	},
}

var resourceSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{{Type: "lifecycle"}, {Type: "provisioner", LabelNames: []string{"type"}}},
}

// provisioners reads a resource's provisioner blocks, keeping commands as
// they are written, interpolations and all.
func provisioners(body hcl.Body, text []byte) []Provisioner {
	content, _, _ := body.PartialContent(resourceSchema)
	if content == nil {
		return nil
	}
	written := func(e hcl.Expression) string {
		r := e.Range()
		if text == nil || r.Start.Byte < 0 || r.End.Byte > len(text) || r.Start.Byte >= r.End.Byte {
			return ""
		}
		s := strings.TrimSpace(string(text[r.Start.Byte:r.End.Byte]))
		switch {
		case strings.HasPrefix(s, "<<"):
			// A heredoc: its first line of script, and a sign there is more.
			lines := strings.Split(s, "\n")
			for i, l := range lines[1:] {
				if l = strings.TrimSpace(l); l != "" {
					if i+2 < len(lines)-1 {
						l += " …"
					}
					return l
				}
			}
			return ""
		case len(s) > 1 && s[0] == '"' && s[len(s)-1] == '"':
			return s[1 : len(s)-1]
		}
		return s
	}
	var out []Provisioner
	for _, block := range content.Blocks {
		if block.Type != "provisioner" || len(block.Labels) != 1 {
			continue
		}
		p := Provisioner{Type: block.Labels[0]}
		attrs, _ := block.Body.JustAttributes()
		if a := attrs["when"]; a != nil {
			p.Destroy = hcl.ExprAsKeyword(a.Expr) == "destroy"
		}
		if a := attrs["command"]; a != nil {
			p.Command = written(a.Expr)
		}
		if a := attrs["inline"]; a != nil {
			if items, diags := hcl.ExprList(a.Expr); !diags.HasErrors() && len(items) > 0 {
				p.Command, p.More = written(items[0]), len(items)-1
			}
		}
		if a := attrs["script"]; a != nil && p.Command == "" {
			p.Command = written(a.Expr)
		}
		out = append(out, p)
	}
	return out
}

func (m *Module) parse(parser *hclparse.Parser, root string) error {
	dir := filepath.Join(root, m.Dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			names[e.Name()] = true
		}
	}
	var files []string
	for name := range names {
		switch {
		case strings.HasSuffix(name, ".tofu"), strings.HasSuffix(name, ".tofu.json"):
			files = append(files, name)
		case strings.HasSuffix(name, ".tf"):
			// OpenTofu prefers x.tofu over x.tf when both exist.
			if !names[strings.TrimSuffix(name, ".tf")+".tofu"] {
				files = append(files, name)
			}
		case strings.HasSuffix(name, ".tf.json"):
			if !names[strings.TrimSuffix(name, ".tf.json")+".tofu.json"] {
				files = append(files, name)
			}
		}
	}
	sort.Strings(files)
	for _, name := range files {
		path := filepath.Join(dir, name)
		var f *hcl.File
		if strings.HasSuffix(name, ".json") {
			f, _ = parser.ParseJSONFile(path)
		} else {
			f, _ = parser.ParseHCLFile(path)
		}
		if f == nil || f.Body == nil {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		m.read(f.Body, filepath.ToSlash(rel), strings.HasSuffix(name, "_override.tf"), f.Bytes)
	}
	return nil
}

func (m *Module) read(body hcl.Body, file string, override bool, text []byte) {
	content, _, _ := body.PartialContent(fileSchema)
	if content == nil {
		return
	}
	for _, block := range content.Blocks {
		switch block.Type {
		case "resource", "data", "module":
			local := ""
			switch {
			case block.Type == "module" && len(block.Labels) == 1:
				local = "module." + block.Labels[0]
			case block.Type == "resource" && len(block.Labels) == 2:
				local = block.Labels[0] + "." + block.Labels[1]
			case block.Type == "data" && len(block.Labels) == 2:
				local = "data." + block.Labels[0] + "." + block.Labels[1]
			default:
				continue
			}
			if _, exists := m.Blocks[local]; exists && override {
				continue
			}
			b := &Block{File: file, Line: block.DefRange.Start.Line, EndLine: endLine(block), Args: args(block.Body, text)}
			b.Lifecycle = lifecycle(block.Body)
			b.Provisioners = provisioners(block.Body, text)
			m.Blocks[local] = b
		case "locals":
			attrs, _ := block.Body.JustAttributes()
			for name, attr := range attrs {
				m.Locals[name] = refs(attr.Expr)
				m.Inputs["local."+name] = &Block{File: file, Line: attr.Range.Start.Line, EndLine: attr.Range.End.Line}
			}
		case "provider":
			// The first block for a provider, which default_tags and the
			// like usually live in; aliases share its edit.
			if key := "provider." + block.Labels[0]; len(block.Labels) == 1 && m.Inputs[key] == nil {
				m.Inputs[key] = &Block{File: file, Line: block.DefRange.Start.Line, EndLine: endLine(block), Args: args(block.Body, text)}
			}
		case "variable":
			if len(block.Labels) == 1 {
				b := &Block{File: file, Line: block.DefRange.Start.Line, EndLine: endLine(block), Args: args(block.Body, text)}
				content, _, _ := block.Body.PartialContent(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "sensitive"}}})
				if a := content.Attributes["sensitive"]; a != nil {
					s := literalBool(a.Expr)
					b.Sensitive = s != nil && *s
				}
				m.Inputs["var."+block.Labels[0]] = b
			}
		case "moved":
			attrs, _ := block.Body.JustAttributes()
			mv := Moved{File: file, Line: block.DefRange.Start.Line}
			if a := attrs["from"]; a != nil {
				mv.From = traversalString(a.Expr)
			}
			if a := attrs["to"]; a != nil {
				mv.To = traversalString(a.Expr)
			}
			m.Moved = append(m.Moved, mv)
		}
	}
}

// endLine is the line a block's closing brace is on. A .tf.json block has
// no syntax tree to ask, but its body knows where its object closes.
func endLine(block *hcl.Block) int {
	if sb, ok := block.Body.(*hclsyntax.Body); ok {
		return sb.SrcRange.End.Line
	}
	if hcljson.IsJSONBody(block.Body) {
		return block.Body.MissingItemRange().End.Line
	}
	return 0
}

// args reads the arguments and nested blocks of a block's body, with the
// lines each spans.
func args(body hcl.Body, text []byte) []Arg {
	var out []Arg
	if sb, ok := body.(*hclsyntax.Body); ok {
		for name, a := range sb.Attributes {
			out = append(out, Arg{Name: name, Line: a.SrcRange.Start.Line, EndLine: a.SrcRange.End.Line,
				Literal: literal(a.Expr) || commented(text, a.SrcRange)})
		}
		for _, b := range sb.Blocks {
			name := b.Type
			if b.Type == "dynamic" && len(b.Labels) == 1 {
				name = b.Labels[0]
			}
			out = append(out, Arg{Name: name, Line: b.TypeRange.Start.Line, EndLine: b.Body.SrcRange.End.Line, Literal: true})
		}
	} else if hcljson.IsJSONBody(body) {
		attrs, _ := body.JustAttributes()
		for name, a := range attrs {
			out = append(out, Arg{Name: name, Line: a.Range.Start.Line, EndLine: a.Range.End.Line, Literal: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// literal reports whether an expression writes out a value of its own
// anywhere in it, rather than only referring to others.
func literal(expr hclsyntax.Expression) bool {
	found := false
	hclsyntax.VisitAll(expr, func(n hclsyntax.Node) hcl.Diagnostics {
		if _, ok := n.(*hclsyntax.LiteralValueExpr); ok {
			found = true
		}
		return nil
	})
	return found
}

// commented reports whether the lines a range spans could hold a comment,
// which can say anything. When the text is not there to tell, they could.
func commented(text []byte, r hcl.Range) bool {
	if r.Start.Byte < 0 || r.End.Byte > len(text) || r.Start.Byte > r.End.Byte {
		return true
	}
	start := bytes.LastIndexByte(text[:r.Start.Byte], '\n') + 1
	end := len(text)
	if i := bytes.IndexByte(text[r.End.Byte:], '\n'); i >= 0 {
		end = r.End.Byte + i
	}
	s := text[start:end]
	return bytes.Contains(s, []byte("#")) || bytes.Contains(s, []byte("//")) || bytes.Contains(s, []byte("/*"))
}

func lifecycle(body hcl.Body) Lifecycle {
	var lc Lifecycle
	content, _, _ := body.PartialContent(resourceSchema)
	if content == nil {
		return lc
	}
	for _, block := range content.Blocks {
		if block.Type != "lifecycle" {
			continue
		}
		attrs, _ := block.Body.JustAttributes()
		for name, attr := range attrs {
			switch name {
			case "prevent_destroy":
				lc.PreventDestroy = literalBool(attr.Expr)
			case "create_before_destroy":
				lc.CreateBeforeDestroy = literalBool(attr.Expr)
			case "ignore_changes":
				if hcl.ExprAsKeyword(attr.Expr) == "all" {
					lc.IgnoreChanges = []string{"all"}
					continue
				}
				exprs, diags := hcl.ExprList(attr.Expr)
				if diags.HasErrors() {
					continue
				}
				for _, e := range exprs {
					if s := traversalString(e); s != "" {
						lc.IgnoreChanges = append(lc.IgnoreChanges, s)
					}
				}
			case "replace_triggered_by":
				exprs, diags := hcl.ExprList(attr.Expr)
				if diags.HasErrors() {
					continue
				}
				for _, e := range exprs {
					if s := traversalString(e); s != "" {
						lc.ReplaceTriggeredBy = append(lc.ReplaceTriggeredBy, s)
					}
				}
			}
		}
	}
	return lc
}

func literalBool(expr hcl.Expression) *bool {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || !v.IsKnown() || v.IsNull() || v.Type().FriendlyName() != "bool" {
		return nil
	}
	b := v.True()
	return &b
}

func refs(expr hcl.Expression) []string {
	var out []string
	for _, t := range expr.Variables() {
		if s := renderTraversal(t); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func traversalString(expr hcl.Expression) string {
	t, diags := hcl.AbsTraversalForExpr(expr)
	if diags.HasErrors() {
		return ""
	}
	return renderTraversal(t)
}

func renderTraversal(t hcl.Traversal) string {
	var b strings.Builder
	for _, step := range t {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			b.WriteString(s.Name)
		case hcl.TraverseAttr:
			b.WriteString("." + s.Name)
		case hcl.TraverseIndex:
			key := s.Key
			switch {
			case !key.IsKnown() || key.IsNull():
				b.WriteString("[*]")
			case key.Type() == cty.String:
				b.WriteString(`["` + key.AsString() + `"]`)
			case key.Type() == cty.Number:
				b.WriteString("[" + key.AsBigFloat().Text('f', -1) + "]")
			case key.Type() == cty.Bool:
				b.WriteString(fmt.Sprintf("[%t]", key.True()))
			default:
				b.WriteString("[*]")
			}
		}
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
