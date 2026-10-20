package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Step is one module call in a module path, such as module.network["a"].
type Step struct {
	Name string
	Key  string // rendered instance key including brackets, or ""
}

// Address is a parsed resource instance address.
type Address struct {
	Module []Step
	Mode   string // "managed" or "data"
	Type   string
	Name   string
	Key    string // rendered instance key including brackets, or ""
}

// ModuleString renders the module path, keys included.
func (a Address) ModuleString() string { return renderSteps(a.Module, true) }

// ModuleConfigString renders the module path without instance keys.
func (a Address) ModuleConfigString() string { return renderSteps(a.Module, false) }

// Resource renders the resource address without the instance key.
func (a Address) Resource() string {
	return join(a.ModuleString(), a.local())
}

// Config renders the static configuration address: no keys anywhere.
func (a Address) Config() string {
	return join(a.ModuleConfigString(), a.local())
}

// String renders the full instance address.
func (a Address) String() string { return a.Resource() + a.Key }

func (a Address) local() string {
	if a.Mode == "data" {
		return "data." + a.Type + "." + a.Name
	}
	return a.Type + "." + a.Name
}

func join(module, local string) string {
	if module == "" {
		return local
	}
	return module + "." + local
}

func renderSteps(steps []Step, keys bool) string {
	var b strings.Builder
	for i, s := range steps {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString("module.")
		b.WriteString(s.Name)
		if keys {
			b.WriteString(s.Key)
		}
	}
	return b.String()
}

// ParseAddress parses a resource instance address such as
// module.app["blue"].data.aws_ami.ubuntu[0].
func ParseAddress(s string) (Address, error) {
	var a Address
	rest := s
	for strings.HasPrefix(rest, "module.") {
		rest = rest[len("module."):]
		name, after := splitIdent(rest)
		if name == "" {
			return a, fmt.Errorf("invalid address %q: missing module name", s)
		}
		key, after, err := splitKey(after)
		if err != nil {
			return a, fmt.Errorf("invalid address %q: %w", s, err)
		}
		a.Module = append(a.Module, Step{Name: name, Key: key})
		if after == "" {
			return a, fmt.Errorf("invalid address %q: no resource after module path", s)
		}
		if after[0] != '.' {
			return a, fmt.Errorf("invalid address %q: unexpected %q", s, after)
		}
		rest = after[1:]
	}
	a.Mode = "managed"
	if strings.HasPrefix(rest, "data.") {
		a.Mode = "data"
		rest = rest[len("data."):]
	}
	typ, after := splitIdent(rest)
	if typ == "" || !strings.HasPrefix(after, ".") {
		return a, fmt.Errorf("invalid address %q: expected <type>.<name>", s)
	}
	name, after := splitIdent(after[1:])
	if name == "" {
		return a, fmt.Errorf("invalid address %q: missing resource name", s)
	}
	key, after, err := splitKey(after)
	if err != nil {
		return a, fmt.Errorf("invalid address %q: %w", s, err)
	}
	if after != "" {
		return a, fmt.Errorf("invalid address %q: unexpected %q", s, after)
	}
	a.Type, a.Name, a.Key = typ, name, key
	return a, nil
}

// ParseModulePath parses a module address such as module.a["x"].module.b.
func ParseModulePath(s string) ([]Step, error) {
	if s == "" {
		return nil, nil
	}
	a, err := ParseAddress(s + ".x.y")
	if err != nil {
		return nil, fmt.Errorf("invalid module address %q", s)
	}
	return a.Module, nil
}

func splitIdent(s string) (ident, rest string) {
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '.' || c == '[' {
			break
		}
		i++
	}
	return s[:i], s[i:]
}

func splitKey(s string) (key, rest string, err error) {
	if !strings.HasPrefix(s, "[") {
		return "", s, nil
	}
	if len(s) > 1 && s[1] == '"' {
		i := 2
		for i < len(s) {
			switch s[i] {
			case '\\':
				i += 2
				continue
			case '"':
				if i+1 < len(s) && s[i+1] == ']' {
					return s[:i+2], s[i+2:], nil
				}
				return "", "", fmt.Errorf("malformed key")
			}
			i++
		}
		return "", "", fmt.Errorf("unterminated key")
	}
	end := strings.IndexByte(s, ']')
	if end < 0 {
		return "", "", fmt.Errorf("unterminated key")
	}
	if _, err := strconv.Atoi(s[1:end]); err != nil {
		return "", "", fmt.Errorf("invalid index %q", s[1:end])
	}
	return s[:end+1], s[end+1:], nil
}

// RenderKey renders an instance index from the plan (a number or a string)
// the way Terraform prints it: [0] or ["key"].
func RenderKey(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return "[" + strconv.Quote(s) + "]"
		}
	}
	return "[" + string(raw) + "]"
}

// KeyValue returns the index as an int (for count) or string (for for_each).
func KeyValue(raw json.RawMessage) (any, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s, true
		}
		return nil, false
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil {
		return nil, false
	}
	return n, true
}

// AddressOf parses rc.Address, falling back to its structured fields.
func AddressOf(rc *ResourceChange) Address {
	if a, err := ParseAddress(rc.Address); err == nil {
		return a
	}
	steps, _ := ParseModulePath(rc.ModuleAddress)
	mode := rc.Mode
	if mode == "" {
		mode = "managed"
	}
	return Address{Module: steps, Mode: mode, Type: rc.Type, Name: rc.Name, Key: RenderKey(rc.Index)}
}
