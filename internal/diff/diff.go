// Package diff computes structural before/after diffs of Terraform values.
//
// It understands the masks Terraform attaches to a change (unknown and
// sensitive values), marks the attributes that force replacement, aligns list
// elements so an insertion does not look like every later element changing,
// decodes JSON held in strings (IAM policies, task definitions) and line-diffs
// multi-line strings (user data, templates).
package diff

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Kind classifies a node in a diff.
type Kind string

// Node kinds.
const (
	Unchanged Kind = "unchanged"
	Added     Kind = "added"
	Removed   Kind = "removed"
	Changed   Kind = "changed"
)

// Node is one attribute, map entry or list element in a diff. Leaves carry
// Before/After values; composites carry Children.
type Node struct {
	Key       string  `json:"key,omitempty"`
	Kind      Kind    `json:"kind"`
	Type      string  `json:"type,omitempty"`
	Before    any     `json:"before,omitempty"`
	After     any     `json:"after,omitempty"`
	Unknown   bool    `json:"unknown,omitempty"`
	Sensitive bool    `json:"sensitive,omitempty"`
	Forces    bool    `json:"forces,omitempty"`
	Encoding  string  `json:"encoding,omitempty"`
	Trivial   bool    `json:"trivial,omitempty"`
	Lines     []Line  `json:"lines,omitempty"`
	Children  []*Node `json:"children,omitempty"`
	Hidden    int     `json:"hidden,omitempty"`

	path      []any
	beforeIdx int
	afterIdx  int
	parent    *Node
}

// Line is one line of a multi-line string diff. Op is " ", "-", "+" or
// "skip" for a run of Skip unchanged lines that were elided.
type Line struct {
	Op   string `json:"op"`
	Text string `json:"text,omitempty"`
	Skip int    `json:"skip,omitempty"`
}

// Input is everything Terraform records about one change.
type Input struct {
	Before, After                   any
	AfterUnknown                    any
	BeforeSensitive, AfterSensitive any
	ReplacePaths                    [][]any
	// HasBefore and HasAfter distinguish "null" from "absent" at the root;
	// a create has no before and a delete has no after.
	HasBefore, HasAfter bool
}

// Compute builds the diff tree for one change.
func Compute(in Input) *Node {
	root := build(nil, "", in.Before, in.After, in.HasBefore && in.Before != nil, in.HasAfter, in.AfterUnknown, in.BeforeSensitive, in.AfterSensitive)
	for _, p := range in.ReplacePaths {
		markForces(root, p)
	}
	setParents(root)
	return root
}

func setParents(n *Node) {
	for _, c := range n.Children {
		c.parent = n
		setParents(c)
	}
}

// Parent returns the node's parent, or nil at the root.
func (n *Node) Parent() *Node { return n.parent }

// Element returns the list element a node sits in (the outermost one), or
// nil when the node is not inside a list.
func (n *Node) Element() *Node {
	var found *Node
	for c := n; c != nil && c.parent != nil; c = c.parent {
		if c.parent.Type == "list" {
			found = c
		}
	}
	return found
}

// Path returns the node's path from the root of its diff.
func (n *Node) Path() []any { return n.path }

// PathString renders the node's path, e.g. ingress[1].cidr_blocks.
func (n *Node) PathString() string { return PathString(n.path) }

// PathString renders a path the way Terraform users write it.
func PathString(path []any) string {
	var b strings.Builder
	for _, step := range path {
		switch s := step.(type) {
		case int:
			b.WriteString("[" + strconv.Itoa(s) + "]")
		case string:
			if isIdent(s) {
				if b.Len() > 0 {
					b.WriteByte('.')
				}
				b.WriteString(s)
			} else {
				b.WriteString("[" + quote(s) + "]")
			}
		}
	}
	return b.String()
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		ok := r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func build(path []any, key string, before, after any, hasBefore, hasAfter bool, unk, bsens, asens any) *Node {
	n := &Node{Key: key, path: path, beforeIdx: -1, afterIdx: -1}
	unknown := isTrue(unk)
	if unknown {
		hasAfter = true
		after = nil
	}
	if hasAfter && after == nil && !unknown {
		// An explicit null is the same as absent for display purposes.
		hasAfter = false
	}
	if hasBefore && before == nil {
		hasBefore = false
	}

	if isTrue(bsens) || isTrue(asens) {
		n.Sensitive = true
		n.Type = "sensitive"
		n.Unknown = unknown
		switch {
		case !hasBefore && !hasAfter:
			n.Kind = Unchanged
		case !hasBefore:
			n.Kind = Added
		case !hasAfter:
			n.Kind = Removed
		case unknown || !Equal(before, after):
			n.Kind = Changed
		default:
			n.Kind = Unchanged
		}
		return n
	}

	switch {
	case unknown:
		n.Unknown = true
		if hasBefore {
			n.Kind = Changed
			oneSide(n, before, true, nil, bsens)
		} else {
			n.Kind = Added
			n.Type = "unknown"
		}
		return n
	case !hasBefore && !hasAfter:
		n.Kind = Unchanged
		n.Type = "null"
		return n
	case !hasBefore:
		n.Kind = Added
		n.Trivial = IsEmpty(after)
		oneSide(n, after, false, unk, asens)
		return n
	case !hasAfter:
		n.Kind = Removed
		n.Trivial = IsEmpty(before)
		oneSide(n, before, true, nil, bsens)
		return n
	}

	bm, bIsMap := before.(map[string]any)
	am, aIsMap := after.(map[string]any)
	if bIsMap && aIsMap {
		n.Type = "object"
		diffObject(n, bm, am, unk, bsens, asens)
		return n
	}
	bl, bIsList := before.([]any)
	al, aIsList := after.([]any)
	if bIsList && aIsList {
		n.Type = "list"
		diffList(n, bl, al, unk, bsens, asens)
		return n
	}
	bs, bIsStr := before.(string)
	as, aIsStr := after.(string)
	if bIsStr && aIsStr {
		diffString(n, bs, as)
		return n
	}
	// The two sides have different types (a map becoming a list, a number
	// becoming a string). Nested parts may still be sensitive or unknown, so
	// mask them before copying the values.
	n.Type = TypeOf(after)
	if Equal(before, after) {
		n.Kind = Unchanged
	} else {
		n.Kind = Changed
		n.Trivial = IsEmpty(before) && IsEmpty(after)
	}
	n.Before = Masked(before, bsens, SensitivePlaceholder)
	n.After = Masked(Masked(after, asens, SensitivePlaceholder), unk, UnknownPlaceholder)
	return n
}

// Placeholders that stand in for masked values.
const (
	SensitivePlaceholder = "(sensitive value)"
	UnknownPlaceholder   = "(known after apply)"
)

// Masked returns v with every position the mask marks replaced by
// placeholder. Masks mirror the value's shape, as Terraform writes them.
func Masked(v, mask any, placeholder string) any {
	if isTrue(mask) {
		if v == nil {
			return nil
		}
		return placeholder
	}
	if !Marked(mask) {
		return v
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, c := range x {
			out[k] = Masked(c, Child(mask, k), placeholder)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, c := range x {
			out[i] = Masked(c, Child(mask, i), placeholder)
		}
		return out
	}
	return v
}

// oneSide populates n with a value that exists on only one side of the
// change: every descendant is removed (removed=true) or added. Masks still
// apply, so sensitive values stay hidden and unknown values are listed.
func oneSide(n *Node, v any, removed bool, unk, sens any) {
	child := func(path []any, key string, cv any, has bool, cunk, csens any) *Node {
		if removed {
			return build(path, key, cv, nil, has, false, nil, csens, nil)
		}
		return build(path, key, nil, cv, false, has, cunk, nil, csens)
	}
	set := func(x any) {
		if removed {
			n.Before = x
		} else {
			n.After = x
		}
	}
	switch x := v.(type) {
	case map[string]any:
		if n.Type == "" {
			n.Type = "object"
		}
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		if um, ok := unk.(map[string]any); ok {
			for k, uv := range um {
				if isTrue(uv) {
					keys[k] = true
				}
			}
		}
		for _, k := range SortedKeys(keys) {
			cv, has := x[k]
			if cv == nil && !isTrue(Child(unk, k)) {
				continue
			}
			n.Children = append(n.Children, child(appendPath(n.path, k), k, cv, has, Child(unk, k), Child(sens, k)))
		}
		if len(x) == 0 {
			set(x)
		}
	case []any:
		if n.Type == "" {
			n.Type = "list"
		}
		for i, e := range x {
			c := child(appendPath(n.path, i), "", e, true, Child(unk, i), Child(sens, i))
			if removed {
				c.beforeIdx = i
			} else {
				c.afterIdx = i
			}
			n.Children = append(n.Children, c)
		}
		if len(x) == 0 {
			set(x)
		}
	case string:
		if n.Type == "" {
			n.Type = "string"
		}
		if decoded, ok := DecodeJSON(x); ok {
			n.Encoding = "json"
			n.Type = TypeOf(decoded)
			oneSide(n, decoded, removed, nil, nil)
			return
		}
		if strings.Contains(x, "\n") {
			op := "+"
			if removed {
				op = "-"
			}
			n.Lines = wholeLines(x, op)
			return
		}
		set(x)
	default:
		if n.Type == "" {
			n.Type = TypeOf(x)
		}
		set(x)
	}
}

func diffObject(n *Node, bm, am map[string]any, unk, bsens, asens any) {
	keys := map[string]bool{}
	for k := range bm {
		keys[k] = true
	}
	for k := range am {
		keys[k] = true
	}
	if um, ok := unk.(map[string]any); ok {
		for k, v := range um {
			if isTrue(v) {
				keys[k] = true
			}
		}
	}
	changed := false
	for _, k := range SortedKeys(keys) {
		bv, hasB := bm[k]
		av, hasA := am[k]
		c := build(appendPath(n.path, k), k, bv, av, hasB, hasA, Child(unk, k), Child(bsens, k), Child(asens, k))
		if c.Kind != Unchanged {
			changed = true
		}
		n.Children = append(n.Children, c)
	}
	n.Kind = Unchanged
	if changed {
		n.Kind = Changed
	}
}

func diffList(n *Node, bl, al []any, unk, bsens, asens any) {
	known := make([]bool, len(al))
	for j := range al {
		known[j] = !Marked(Child(unk, j))
	}
	pairs := align(bl, al, known)
	changed := false
	add := func(c *Node) {
		if c.Kind != Unchanged {
			changed = true
		}
		n.Children = append(n.Children, c)
	}
	var hunkB, hunkA []int
	flush := func() {
		for _, c := range pairHunk(n, bl, al, hunkB, hunkA, unk, bsens, asens) {
			add(c)
		}
		hunkB, hunkA = hunkB[:0], hunkA[:0]
	}
	for _, p := range pairs {
		switch {
		case p.b >= 0 && p.a >= 0:
			flush()
			c := build(appendPath(n.path, p.a), "", bl[p.b], al[p.a], true, true, Child(unk, p.a), Child(bsens, p.b), Child(asens, p.a))
			c.beforeIdx, c.afterIdx = p.b, p.a
			add(c)
		case p.b >= 0:
			hunkB = append(hunkB, p.b)
		default:
			hunkA = append(hunkA, p.a)
		}
	}
	flush()
	n.Kind = Unchanged
	if changed {
		n.Kind = Changed
	}
}

// pairHunk turns a run of removed and added list elements into nodes,
// pairing removed and added objects that are similar enough to read as an
// in-place modification.
func pairHunk(n *Node, bl, al []any, hb, ha []int, unk, bsens, asens any) []*Node {
	pairedB := map[int]int{}
	usedA := map[int]bool{}
	for _, i := range hb {
		bo, ok := bl[i].(map[string]any)
		if !ok {
			continue
		}
		best, bestScore := -1, 0.5
		for _, j := range ha {
			if usedA[j] {
				continue
			}
			ao, ok := al[j].(map[string]any)
			if !ok {
				continue
			}
			if s := similarity(bo, ao); s >= bestScore {
				best, bestScore = j, s
			}
		}
		if best >= 0 {
			pairedB[i] = best
			usedA[best] = true
		}
	}
	var out []*Node
	for _, i := range hb {
		if j, ok := pairedB[i]; ok {
			c := build(appendPath(n.path, j), "", bl[i], al[j], true, true, Child(unk, j), Child(bsens, i), Child(asens, j))
			c.beforeIdx, c.afterIdx = i, j
			out = append(out, c)
			continue
		}
		c := build(appendPath(n.path, i), "", bl[i], nil, true, false, nil, Child(bsens, i), nil)
		c.beforeIdx = i
		out = append(out, c)
	}
	for _, j := range ha {
		if usedA[j] {
			continue
		}
		c := build(appendPath(n.path, j), "", nil, al[j], false, true, Child(unk, j), nil, Child(asens, j))
		c.afterIdx = j
		out = append(out, c)
	}
	return out
}

// similarity is the fraction of keys two objects agree on.
func similarity(a, b map[string]any) float64 {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	if len(keys) == 0 {
		return 1
	}
	same := 0
	for k := range keys {
		if Equal(a[k], b[k]) {
			same++
		}
	}
	return float64(same) / float64(len(keys))
}

type pair struct{ b, a int }

// align matches equal elements of two lists along a shortest edit script.
// Unknown after-elements never match.
func align(bl, al []any, known []bool) []pair {
	a := make([]string, len(bl))
	for i, v := range bl {
		a[i] = canonical(v)
	}
	b := make([]string, len(al))
	for j, v := range al {
		if known[j] {
			b[j] = canonical(v)
		} else {
			b[j] = "\x00unknown\x00" + strconv.Itoa(j)
		}
	}
	ops, ok := myers(a, b)
	if !ok {
		return alignByIndex(len(bl), len(al), bl, al, known)
	}
	out := make([]pair, 0, len(ops))
	i, j := 0, 0
	for _, op := range ops {
		switch op.Op {
		case " ":
			out = append(out, pair{i, j})
			i++
			j++
		case "-":
			out = append(out, pair{i, -1})
			i++
		case "+":
			out = append(out, pair{-1, j})
			j++
		}
	}
	return out
}

// canonical renders a decoded value so that equal values give equal
// strings (map keys are sorted by encoding/json).
func canonical(v any) string {
	if n, ok := v.(json.Number); ok {
		if f, err := n.Float64(); err == nil && !strings.ContainsAny(n.String(), "eE.") {
			return n.String()
		} else if err == nil {
			return strconv.FormatFloat(f, 'g', -1, 64)
		}
	}
	return marshal(v)
}

func alignByIndex(nb, na int, bl, al []any, known []bool) []pair {
	var out []pair
	for i := 0; i < max(nb, na); i++ {
		switch {
		case i < nb && i < na && known[i] && Equal(bl[i], al[i]):
			out = append(out, pair{i, i})
		default:
			if i < nb {
				out = append(out, pair{i, -1})
			}
			if i < na {
				out = append(out, pair{-1, i})
			}
		}
	}
	return out
}

func diffString(n *Node, bs, as string) {
	n.Type = "string"
	if bs == as {
		n.Kind = Unchanged
		n.Before, n.After = bs, as
		return
	}
	n.Kind = Changed
	bj, bok := DecodeJSON(bs)
	aj, aok := DecodeJSON(as)
	if bok && aok {
		n.Encoding = "json"
		inner := build(n.path, "", bj, aj, true, true, nil, nil, nil)
		if inner.Kind == Unchanged {
			// Same document, different formatting or key order: a leaf, so
			// it counts as a (trivial) change.
			n.Trivial = true
			n.Before, n.After = bs, as
			n.Encoding = ""
			return
		}
		n.Type = TypeOf(aj)
		if TypeOf(bj) != TypeOf(aj) {
			n.Type = "string"
			n.Encoding = ""
			n.Before, n.After = bs, as
			return
		}
		n.Children = inner.Children
		return
	}
	if strings.Contains(bs, "\n") || strings.Contains(as, "\n") {
		n.Lines = lineDiff(bs, as)
		return
	}
	n.Before, n.After = bs, as
}

// DecodeJSON decodes s when it holds a JSON object or array.
func DecodeJSON(s string) (any, bool) {
	t := strings.TrimSpace(s)
	if len(t) < 2 || (t[0] != '{' && t[0] != '[') {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(t))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if strings.TrimSpace(t[dec.InputOffset():]) != "" {
		return nil, false
	}
	return v, true
}

func markForces(root *Node, path []any) {
	n := root
	for _, step := range path {
		next := childAt(n, step)
		if next == nil {
			break
		}
		n = next
	}
	if n != root {
		n.Forces = true
	}
}

func childAt(n *Node, step any) *Node {
	switch s := step.(type) {
	case string:
		for _, c := range n.Children {
			if c.Key == s {
				return c
			}
		}
	case json.Number:
		i, err := strconv.Atoi(s.String())
		if err != nil {
			return nil
		}
		return childAt(n, i)
	case float64:
		return childAt(n, int(s))
	case int:
		for _, c := range n.Children {
			if c.afterIdx == s {
				return c
			}
		}
		for _, c := range n.Children {
			if c.afterIdx < 0 && c.beforeIdx == s {
				return c
			}
		}
	}
	return nil
}

func isTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// Marked reports whether a mask marks anything at or beneath it.
func Marked(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case map[string]any:
		for _, c := range x {
			if Marked(c) {
				return true
			}
		}
	case []any:
		for _, c := range x {
			if Marked(c) {
				return true
			}
		}
	}
	return false
}

func appendPath(path []any, step any) []any {
	out := make([]any, len(path), len(path)+1)
	copy(out, path)
	return append(out, step)
}
