package diff

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// Equal reports whether two decoded JSON values are deeply equal.
func Equal(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !Equal(v, w) {
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
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case json.Number:
		switch y := b.(type) {
		case json.Number:
			return x == y || numEqual(x, y)
		case float64:
			f, err := x.Float64()
			return err == nil && f == y
		}
		return false
	case float64:
		if y, ok := b.(json.Number); ok {
			return Equal(y, x)
		}
		y, ok := b.(float64)
		return ok && x == y
	default:
		return a == b
	}
}

// numEqual compares numbers exactly, so large integers that differ in the
// last digit are not mistaken for equal.
func numEqual(a, b json.Number) bool {
	x, ok1 := new(big.Rat).SetString(a.String())
	y, ok2 := new(big.Rat).SetString(b.String())
	return ok1 && ok2 && x.Cmp(y) == 0
}

// IsEmpty reports whether v is null or an empty string, list or map.
// Terraform providers often flip between these, which is rarely meaningful.
func IsEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// TypeOf names the JSON type of a decoded value.
func TypeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "list"
	case string:
		return "string"
	case json.Number, float64, int:
		return "number"
	case bool:
		return "bool"
	}
	return fmt.Sprintf("%T", v)
}

// SortedKeys returns the keys of m in lexical order.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Get walks a decoded value along a path of keys and indexes.
func Get(v any, path ...any) (any, bool) {
	for _, step := range path {
		switch s := step.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			v, ok = m[s]
			if !ok {
				return nil, false
			}
		case int:
			l, ok := v.([]any)
			if !ok || s < 0 || s >= len(l) {
				return nil, false
			}
			v = l[s]
		default:
			return nil, false
		}
	}
	return v, true
}

// Flag reports whether a sensitivity or unknown mask marks path as true.
// Masks mirror the value's shape with true at marked positions; a true at
// any prefix marks everything beneath it.
func Flag(mask any, path ...any) bool {
	for _, step := range path {
		if b, ok := mask.(bool); ok {
			return b
		}
		next, ok := Get(mask, step)
		if !ok {
			return false
		}
		mask = next
	}
	b, ok := mask.(bool)
	return ok && b
}

// Child returns the part of a mask that applies beneath key.
func Child(mask any, key any) any {
	if b, ok := mask.(bool); ok {
		return b
	}
	v, _ := Get(mask, key)
	return v
}

// String renders a scalar compactly, in HCL-like syntax.
func String(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return quote(x)
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	case []any, map[string]any:
		return marshal(x)
	}
	return fmt.Sprint(v)
}

func quote(s string) string { return marshal(s) }

// marshal encodes v as compact JSON without HTML escaping.
func marshal(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(b.String(), "\n")
}
