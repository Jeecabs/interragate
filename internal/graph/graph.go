// Package graph builds a resource-level dependency graph from the
// configuration embedded in a plan.
//
// Terraform records references per expression ("aws_vpc.main.id",
// "module.network.vpc_id", "var.name"). The graph follows them through module
// inputs and outputs (and locals, when the source is available) down to the
// resources they ultimately come from, so every edge connects two resources.
package graph

import (
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/plan"
)

// Ref is a reference to a resource, optionally to one of its attributes.
type Ref struct {
	Resource  string `json:"resource"`
	Attribute string `json:"attribute,omitempty"`
	// Whole marks a resource reached only because a module is referred to
	// as a whole (module.x rather than module.x.out): Terraform waits for
	// everything in the module, but no value comes from it.
	Whole bool `json:"-"`
}

// Node is one resource or data block. IDs are static configuration
// addresses without instance keys, so every instance of a resource (and of
// a module) shares a node.
type Node struct {
	ID     string `json:"id"`
	Module string `json:"module,omitempty"`
	Mode   string `json:"mode"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	// Up lists direct dependencies; Down lists direct dependents.
	Up   []string `json:"up,omitempty"`
	Down []string `json:"down,omitempty"`
	// Attrs maps each top-level argument to the resources it reads from.
	Attrs map[string][]Ref `json:"-"`
	// Terraform also waits for resources none of this one's values come
	// from, so they are kept apart from Up. Provider lists those its
	// provider's configuration reads: the provider is configured before
	// anything uses it. Modules lists every resource in the modules it
	// refers to as a whole.
	Provider []string `json:"-"`
	Modules  []string `json:"-"`
}

// Graph is the dependency graph of one configuration.
type Graph struct {
	Nodes map[string]*Node
	// Providers maps each provider configuration, by its key in the plan's
	// configuration, to the resources it reads.
	Providers map[string][]string
	// Outputs maps each root module output to the resources its value
	// comes from.
	Outputs map[string][]Ref
}

// Locals resolves a local value to the references in its expression. module
// is the configuration module path ("" or "module.a.module.b").
type Locals func(module, name string) (refs []string, ok bool)

// Build constructs the graph. locals may be nil.
func Build(cfg *plan.Configuration, locals Locals) *Graph {
	g := &Graph{Nodes: map[string]*Node{}, Providers: map[string][]string{}, Outputs: map[string][]Ref{}}
	if cfg == nil || cfg.RootModule == nil {
		return g
	}
	b := &builder{g: g, locals: locals, scopes: map[string]*scope{}, providers: g.Providers}
	root := &scope{mod: cfg.RootModule}
	b.collect(root)
	// A provider configuration is resolved in the module that declares it;
	// resources name it by its key, whichever module they are in.
	for _, key := range sortedKeys(cfg.ProviderConfig) {
		pc := cfg.ProviderConfig[key]
		if pc == nil || b.scopes[pc.ModuleAddress] == nil {
			continue
		}
		var deps []string
		for _, t := range b.resolveAll(b.scopes[pc.ModuleAddress], collectRefs(pc.Expressions), map[string]bool{}) {
			deps = append(deps, t.Resource)
		}
		if deps = dedupe(deps); len(deps) > 0 {
			b.providers[key] = deps
		}
	}
	b.connect(root)
	for _, name := range sortedKeys(cfg.RootModule.Outputs) {
		if out := cfg.RootModule.Outputs[name]; out != nil && out.Expression != nil {
			if refs := b.resolveAll(root, out.Expression.References, map[string]bool{}); len(refs) > 0 {
				g.Outputs[name] = refs
			}
		}
	}
	for _, n := range g.Nodes {
		n.Up = dedupe(n.Up)
		n.Down = dedupe(n.Down)
		n.Provider = dedupe(n.Provider)
		n.Modules = dedupe(n.Modules)
	}
	return g
}

// Downstream returns every resource that transitively depends on id.
func (g *Graph) Downstream(id string) []string {
	return g.walk(id, func(n *Node) []string { return n.Down })
}

// Upstream returns every resource id transitively depends on.
func (g *Graph) Upstream(id string) []string {
	return g.walk(id, func(n *Node) []string { return n.Up })
}

func (g *Graph) walk(id string, next func(*Node) []string) []string {
	seen := map[string]bool{id: true}
	queue := []string{id}
	var out []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		n := g.Nodes[cur]
		if n == nil {
			continue
		}
		for _, m := range next(n) {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
				queue = append(queue, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

type scope struct {
	prefix string // "module.a.module.b", or "" for the root module
	mod    *plan.ConfigModule
	parent *scope
	call   *plan.ModuleCall
	// inherited are dependencies every resource in the module gets from
	// depends_on on the module call (or an ancestor's).
	inherited []string
	// inheritedWhole are modules every resource in the module waits for
	// as a whole, from a bare module reference in its count or for_each:
	// an order to wait in, not a value read.
	inheritedWhole []string
	children       map[string]*scope
}

type builder struct {
	g      *Graph
	locals Locals
	scopes map[string]*scope // by module path
	// providers maps a provider configuration's key to the resources its
	// configuration reads.
	providers map[string][]string
}

func (s *scope) address(local string) string {
	if s.prefix == "" {
		return local
	}
	return s.prefix + "." + local
}

// collect creates nodes for every resource and links scopes.
func (b *builder) collect(s *scope) {
	b.scopes[s.prefix] = s
	for _, r := range s.mod.Resources {
		local := r.Type + "." + r.Name
		if r.Mode == "data" {
			local = "data." + local
		}
		id := s.address(local)
		b.g.Nodes[id] = &Node{ID: id, Module: s.prefix, Mode: r.Mode, Type: r.Type, Name: r.Name, Attrs: map[string][]Ref{}}
	}
	s.children = map[string]*scope{}
	for _, name := range sortedKeys(s.mod.ModuleCalls) {
		call := s.mod.ModuleCalls[name]
		if call.Module == nil {
			continue
		}
		child := &scope{prefix: s.address("module." + name), mod: call.Module, parent: s, call: call}
		s.children[name] = child
		b.collect(child)
	}
}

func (b *builder) connect(s *scope) {
	for _, r := range s.mod.Resources {
		local := r.Type + "." + r.Name
		if r.Mode == "data" {
			local = "data." + local
		}
		n := b.g.Nodes[s.address(local)]
		add := func(attr string, refs []string) {
			for _, ref := range specific(refs) {
				for _, target := range b.resolve(s, ref, map[string]bool{}) {
					switch {
					case target.Resource == n.ID:
					case target.Whole:
						n.Modules = append(n.Modules, target.Resource)
					default:
						n.Attrs[attr] = append(n.Attrs[attr], target)
						b.link(target.Resource, n.ID)
					}
				}
			}
		}
		for _, dep := range b.providers[r.ProviderConfigKey] {
			if dep != n.ID {
				n.Provider = append(n.Provider, dep)
			}
		}
		for attr, expr := range r.Expressions {
			add(attr, collectRefs(expr))
		}
		if r.CountExpression != nil {
			add("count", r.CountExpression.References)
		}
		if r.ForEachExpression != nil {
			add("for_each", r.ForEachExpression.References)
		}
		for _, target := range b.dependsOn(s, r.DependsOn) {
			if target != n.ID {
				n.Attrs["depends_on"] = append(n.Attrs["depends_on"], Ref{Resource: target})
				b.link(target, n.ID)
			}
		}
		for _, dep := range s.inherited {
			if dep != n.ID {
				b.link(dep, n.ID)
			}
		}
		for _, dep := range s.inheritedWhole {
			if dep != n.ID {
				n.Modules = append(n.Modules, dep)
			}
		}
	}
	for _, name := range sortedKeys(s.children) {
		child := s.children[name]
		var inherited []string
		whole := append([]string(nil), s.inheritedWhole...)
		inherited = append(inherited, s.inherited...)
		inherited = append(inherited, b.dependsOn(s, child.call.DependsOn)...)
		// How many instances of a module exist depends on its count or
		// for_each, so everything inside depends on what those read.
		var repetition []string
		if child.call.CountExpression != nil {
			repetition = append(repetition, child.call.CountExpression.References...)
		}
		if child.call.ForEachExpression != nil {
			repetition = append(repetition, child.call.ForEachExpression.References...)
		}
		for _, t := range b.resolveAll(s, repetition, map[string]bool{}) {
			if t.Whole {
				whole = append(whole, t.Resource)
				continue
			}
			inherited = append(inherited, t.Resource)
		}
		child.inherited = dedupe(inherited)
		child.inheritedWhole = dedupe(whole)
		b.connect(child)
	}
}

// dependsOn resolves depends_on entries. A bare module reference means
// every resource in that module, whether or not it has outputs.
func (b *builder) dependsOn(s *scope, refs []string) []string {
	var out []string
	for _, ref := range refs {
		parts := Traversal(ref)
		if len(parts) == 2 && parts[0] == "module" {
			if child := s.children[parts[1]]; child != nil {
				out = append(out, b.resourcesIn(child)...)
			}
			continue
		}
		for _, t := range b.resolve(s, ref, map[string]bool{}) {
			out = append(out, t.Resource)
		}
	}
	return out
}

// resourcesIn lists every resource in a module and its descendants.
func (b *builder) resourcesIn(s *scope) []string {
	var out []string
	for _, r := range s.mod.Resources {
		local := r.Type + "." + r.Name
		if r.Mode == "data" {
			local = "data." + local
		}
		out = append(out, s.address(local))
	}
	for _, name := range sortedKeys(s.children) {
		out = append(out, b.resourcesIn(s.children[name])...)
	}
	return out
}

func (b *builder) link(from, to string) {
	src, dst := b.g.Nodes[from], b.g.Nodes[to]
	if src == nil || dst == nil {
		return
	}
	dst.Up = append(dst.Up, from)
	src.Down = append(src.Down, to)
}

// resolve follows a reference made from scope s to the resources behind it.
func (b *builder) resolve(s *scope, ref string, seen map[string]bool) []Ref {
	key := s.prefix + "|" + ref
	if seen[key] {
		return nil
	}
	seen[key] = true
	parts := Traversal(ref)
	if len(parts) == 0 {
		return nil
	}
	switch parts[0] {
	case "count", "each", "path", "terraform", "self", "provider", "ephemeral":
		return nil
	case "var":
		if len(parts) < 2 || s.call == nil {
			return nil
		}
		return b.resolveAll(s.parent, collectRefs(s.call.Expressions[parts[1]]), seen)
	case "local":
		if len(parts) < 2 || b.locals == nil {
			return nil
		}
		refs, ok := b.locals(s.prefix, parts[1])
		if !ok {
			return nil
		}
		return b.resolveAll(s, refs, seen)
	case "module":
		if len(parts) < 2 {
			return nil
		}
		child := s.children[parts[1]]
		if child == nil {
			return nil
		}
		if len(parts) >= 3 {
			if out := child.mod.Outputs[parts[2]]; out != nil && out.Expression != nil {
				return b.resolveAll(child, out.Expression.References, seen)
			}
			return nil
		}
		// The whole module: its value is every output, and Terraform waits
		// for everything in it before reading that.
		var refs []Ref
		for _, name := range sortedKeys(child.mod.Outputs) {
			if out := child.mod.Outputs[name]; out.Expression != nil {
				refs = append(refs, b.resolveAll(child, out.Expression.References, seen)...)
			}
		}
		for _, id := range b.resourcesIn(child) {
			refs = append(refs, Ref{Resource: id, Whole: true})
		}
		return refs
	case "data":
		if len(parts) < 3 {
			return nil
		}
		id := s.address("data." + parts[1] + "." + parts[2])
		if b.g.Nodes[id] == nil {
			return nil
		}
		return []Ref{{Resource: id, Attribute: at(parts, 3)}}
	default:
		if len(parts) < 2 {
			return nil
		}
		id := s.address(parts[0] + "." + parts[1])
		if b.g.Nodes[id] == nil {
			return nil
		}
		return []Ref{{Resource: id, Attribute: at(parts, 2)}}
	}
}

func (b *builder) resolveAll(s *scope, refs []string, seen map[string]bool) []Ref {
	if s == nil {
		return nil
	}
	var out []Ref
	for _, r := range specific(refs) {
		out = append(out, b.resolve(s, r, seen)...)
	}
	return out
}

// specific drops a bare module reference ("module.network") when the same
// list also names one of its outputs ("module.network.vpc_id"). Terraform
// records both, but only the output is actually read.
func specific(refs []string) []string {
	var out []string
	for _, r := range refs {
		parts := Traversal(r)
		if len(parts) == 2 && parts[0] == "module" {
			covered := false
			for _, other := range refs {
				op := Traversal(other)
				if len(op) > 2 && op[0] == "module" && op[1] == parts[1] {
					covered = true
					break
				}
			}
			if covered {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

func at(parts []string, i int) string {
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

// Traversal splits a reference such as module.a["x"].out[0].id into its
// names, dropping index steps: [module a out id].
func Traversal(ref string) []string {
	var parts []string
	var cur strings.Builder
	depth := 0
	inString := false
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		switch {
		case inString:
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
		case c == '"' && depth > 0:
			inString = true
		case c == '[':
			depth++
		case c == ']':
			if depth > 0 {
				depth--
			}
		case depth > 0:
		case c == '.':
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// collectRefs gathers every reference in an expression tree, which may be a
// single expression, a nested block (an object of expressions) or a list of
// blocks.
func collectRefs(expr any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if refs, ok := x["references"].([]any); ok {
				for _, r := range refs {
					if s, ok := r.(string); ok {
						out = append(out, s)
					}
				}
			}
			for k, c := range x {
				if k == "references" || k == "constant_value" {
					continue
				}
				walk(c)
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

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sort.Strings(in)
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
