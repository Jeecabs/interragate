package diff

// Leaves returns the changed leaves of a diff: attributes whose value is
// added, removed, changed, sensitive or becomes unknown. Composite values
// that become unknown count as a single leaf.
func (n *Node) Leaves() []*Node {
	var out []*Node
	var visit func(*Node)
	visit = func(c *Node) {
		if c.Kind == Unchanged {
			return
		}
		if c.Unknown || c.Sensitive || len(c.Children) == 0 {
			out = append(out, c)
			return
		}
		for _, cc := range c.Children {
			visit(cc)
		}
	}
	if n != nil {
		for _, c := range n.Children {
			visit(c)
		}
	}
	return out
}

// Root returns the top-level attribute a node sits under.
func (n *Node) Root() string {
	if len(n.path) == 0 {
		return ""
	}
	s, _ := n.path[0].(string)
	return s
}

// Prune drops unchanged descendants of changed composites, counting them in
// Hidden, so a diff shows what changes. keep can preserve unchanged nodes
// that help identify the object, such as id and name.
func (n *Node) Prune(keep func(*Node) bool) {
	if n == nil || len(n.Children) == 0 {
		return
	}
	kept := n.Children[:0]
	for _, c := range n.Children {
		if c.Kind == Unchanged && (keep == nil || !keep(c)) {
			n.Hidden++
			continue
		}
		c.Prune(nil)
		kept = append(kept, c)
	}
	n.Children = kept
}

// LookupBefore finds the node at a path, resolving list indexes against the
// before value. It returns the deepest node it could reach.
func (n *Node) LookupBefore(path []any) *Node {
	cur := n
	for _, step := range path {
		var next *Node
		switch s := step.(type) {
		case string:
			for _, c := range cur.Children {
				if c.Key == s {
					next = c
					break
				}
			}
		case int:
			for _, c := range cur.Children {
				if c.beforeIdx == s {
					next = c
					break
				}
			}
		}
		if next == nil {
			return cur
		}
		cur = next
	}
	return cur
}

// Clone copies a diff tree, so one copy can be pruned for display while
// the other stays complete for analysis.
func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	c := *n
	if n.Children != nil {
		c.Children = make([]*Node, len(n.Children))
		for i, child := range n.Children {
			c.Children[i] = child.Clone()
			c.Children[i].parent = &c
		}
	}
	return &c
}

// Walk visits every node depth-first until fn returns false.
func (n *Node) Walk(fn func(*Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	for _, c := range n.Children {
		c.Walk(fn)
	}
}
