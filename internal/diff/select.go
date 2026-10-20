package diff

import (
	"slices"
	"strconv"
	"strings"
)

// Step is one step along an attribute path: a key or list index or, when
// Any is set, every element or key.
type Step struct {
	Key string
	Any bool
}

// Selection is a value an attribute path picks out of an object, with the
// parts of the unknown and sensitive masks that cover it.
type Selection struct {
	Path      []any
	Value     any
	Unknown   any // the part of the after_unknown mask over it
	Sensitive any // the part of the sensitive mask over it
	// Short is set when the path stopped before the attribute it names, at
	// a value known only after apply.
	Short bool
}

// At names where the value was found, or the path as written when it was
// not reached. Inside a sensitive value, the keys and positions that lead
// to it are held back too.
func (s Selection) At(pattern string) string {
	if len(s.Path) == 0 || s.Short || Flag(s.Sensitive) {
		return pattern
	}
	return PathString(s.Path)
}

// IsUnknown reports whether the whole value is known only after apply.
func (s Selection) IsUnknown() bool { return Flag(s.Unknown) }

// Opaque reports whether any part of the value is known only after apply
// or sensitive.
func (s Selection) Opaque() bool { return Marked(s.Unknown) || Marked(s.Sensitive) }

// Select returns the values a path selects from v, which unknown and
// sensitive mask. A name applied to a list looks inside every element, so
// nested blocks such as root_block_device.encrypted need no index, and an
// empty list reads as a missing block. JSON held in a string, such as an
// IAM policy, can be followed into. A missing attribute selects one null
// value; * over an empty list or map selects none, and * over a single
// value selects that value, so Resource.* covers IAM's string and list
// forms.
func Select(v, unknown, sensitive any, steps []Step) []Selection {
	return pick(v, unknown, sensitive, steps, nil)
}

// pick is Select from a path already followed as far as at.
func pick(v, unk, sens any, steps []Step, at []any) []Selection {
	if Flag(unk) {
		return []Selection{{Path: at, Unknown: unk, Sensitive: sens, Short: len(steps) > 0}}
	}
	if len(steps) == 0 {
		return []Selection{{Path: at, Value: v, Unknown: unk, Sensitive: sens}}
	}
	step, rest := steps[0], steps[1:]
	child := func(key, value any, next []Step) []Selection {
		return pick(value, Child(unk, key), Child(sens, key), next, append(slices.Clip(at), key))
	}
	var out []Selection
	switch x := v.(type) {
	case map[string]any:
		if !step.Any {
			return child(step.Key, x[step.Key], rest)
		}
		for _, k := range SortedKeys(x) {
			out = append(out, child(k, x[k], rest)...)
		}
		return out
	case []any:
		if !step.Any && isIndex(step.Key) {
			i, err := strconv.Atoi(step.Key)
			switch {
			case err != nil:
				return child(step.Key, nil, rest)
			case i < len(x):
				return child(i, x[i], rest)
			}
			return child(i, nil, rest)
		}
		if len(x) == 0 && !step.Any {
			return child(step.Key, nil, rest)
		}
		for i, e := range x {
			if step.Any {
				out = append(out, child(i, e, rest)...)
			} else {
				out = append(out, child(i, e, steps)...)
			}
		}
		return out
	case string:
		if doc, ok := DecodeJSON(x); ok {
			return pick(doc, unk, sens, steps, at)
		}
	}
	switch {
	case step.Any && v != nil:
		return pick(v, unk, sens, rest, at)
	case step.Any:
		return nil
	}
	// Null, or a value with nothing inside it: the attribute is missing.
	return child(step.Key, nil, rest)
}

// isIndex reports whether a path step is written as a list index.
func isIndex(key string) bool { return key != "" && strings.Trim(key, "0123456789") == "" }

// Differs reports whether the values a path selects change between the
// object as it is and as planned, and returns one that does. A value known
// only after apply counts as a change, as terraform plan shows it.
func Differs(before, after []Selection) (Selection, bool) {
	was := map[string]any{}
	for _, s := range before {
		was[PathString(s.Path)] = s.Value
	}
	for _, s := range after {
		key := PathString(s.Path)
		if Marked(s.Unknown) || !Equivalent(was[key], s.Value) {
			return s, true
		}
		delete(was, key)
	}
	for _, s := range before {
		if v, left := was[PathString(s.Path)]; left && !IsEmpty(v) {
			return s, true
		}
	}
	return Selection{}, false
}

// Equivalent compares values the way interragate's diffs tell a change from
// noise: null and empty are the same, and so is JSON held in a string that
// was only reformatted.
func Equivalent(a, b any) bool {
	if IsEmpty(a) && IsEmpty(b) {
		return true
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range x {
			if !Equivalent(v, y[k]) {
				return false
			}
		}
		for k, v := range y {
			if _, seen := x[k]; !seen && !IsEmpty(v) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Equivalent(x[i], y[i]) {
				return false
			}
		}
		return true
	case string:
		if y, ok := b.(string); ok && x != y {
			xj, xok := DecodeJSON(x)
			yj, yok := DecodeJSON(y)
			return xok && yok && Equivalent(xj, yj)
		}
	}
	return Equal(a, b)
}
