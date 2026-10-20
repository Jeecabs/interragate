package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/kb"
	"github.com/Jeecabs/interragate/internal/model"
)

// Policies are a team's own rules, listed under policies: in
// .interragate.yaml.
type Policies []Policy

// Policy is one of a team's own rules. It flags every resource change that
// Match selects and that meets every condition in When.
type Policy struct {
	// ID names the policy wherever a rule ID goes: findings, disable,
	// severity and ignore. It is kebab-case and no built-in rule has it.
	ID    string
	Title string
	// Severity is critical, high, medium, low or info.
	Severity string
	// Message and Remediation are optional. The message follows
	// interragate's own sentence about the change, so it can say why the
	// policy exists.
	Message     string
	Remediation string
	Match       Selector
	When        []Condition
}

// Selector picks the changes a policy applies to. Every field that is set
// must match, and a field that lists several values matches when one does.
type Selector struct {
	// Type, Address and Module are patterns in which * matches any run of
	// characters. Address is the instance address. Module is a module path,
	// such as module.network, with or without instance keys; it covers the
	// modules that module calls too.
	Type, Address, Module []string
	// Provider is the provider's short name, such as aws.
	Provider []string
	// Action is create, update, replace, delete (or destroy), read, forget
	// or no-op. Without it, a policy covers what the plan creates, updates,
	// replaces or imports: the objects its conditions describe. Destroys,
	// forgets, data source reads and resources left alone must be named.
	Action []string
	// Category is interragate's kind of resource, such as database or
	// network.
	Category []string
	// Stateful selects resources that hold data or, when false, those that
	// do not.
	Stateful *bool
	// Origin selects changes by where they come from when interragate
	// compared the configuration with git: edit (an edit to their block or
	// module call), follows (a change that follows from one) or untied
	// (neither). Without a git comparison it matches nothing.
	Origin []string
}

// Origins are the values match.origin takes.
var Origins = []string{"edit", "follows", "untied"}

// Condition tests one attribute of the planned object.
type Condition struct {
	// Attribute is a path such as tags.Owner or ingress.*.cidr_blocks.
	Attribute string
	// Operator is equals, not_equals, in, matches, contains, present or
	// changes.
	Operator string
	// Value is what the operator takes, as read from YAML: a string, a
	// bool, a json.Number that keeps a number as it was written (8.0 stays
	// 8.0), a []any or a map[string]any.
	Value any
}

var (
	policyKeys    = []string{"id", "title", "severity", "message", "remediation", "match", "when"}
	matchKeys     = []string{"type", "address", "module", "provider", "action", "category", "stateful", "origin"}
	operators     = []string{"equals", "not_equals", "in", "matches", "contains", "present", "changes"}
	actionNames   = []string{"create", "update", "replace", "delete", "read", "forget", "no-op"}
	severityNames = []string{"critical", "high", "medium", "low", "info"}
	kebabCase     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// ParseAction returns the action a policy names. destroy is accepted for
// delete, since that is what terraform plan calls it.
func ParseAction(name string) (model.Action, bool) {
	name = strings.ToLower(name)
	if name == "destroy" {
		return model.Delete, true
	}
	if slices.Contains(actionNames, name) {
		return model.Action(name), true
	}
	return "", false
}

// ParsePath reads an attribute path: names joined by dots, * for every
// list element or map key, and brackets for list indexes and for keys a
// dot would split, as in ingress[0].from_port or
// tags["kubernetes.io/role/elb"].
func ParsePath(path string) ([]diff.Step, error) {
	bad := func(problem string) ([]diff.Step, error) {
		return nil, fmt.Errorf("attribute %q %s", path, problem)
	}
	switch {
	case path == "":
		return nil, errors.New("attribute is required")
	case path[0] == '.':
		return bad("starts with a dot")
	case path[0] == '[':
		return bad("must start with an attribute name")
	}
	var steps []diff.Step
	rest, wantName := path, true
	for rest != "" {
		if wantName {
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			name := rest[:end]
			switch {
			case name == "" && rest[0] == '[':
				return bad("has a dot right before [")
			case name == "":
				return bad("has an empty step between dots")
			case strings.ContainsAny(name, "]\" \t\n"):
				return bad(`has a space, quote or ] in a name; put keys like that in brackets, as in tags["Cost Center"]`)
			case name != "*" && strings.Contains(name, "*"):
				return bad("mixes * with other characters; * stands for a whole step")
			}
			if name == "*" {
				steps = append(steps, diff.Step{Any: true})
			} else {
				steps = append(steps, diff.Step{Key: name})
			}
			rest, wantName = rest[end:], false
			continue
		}
		switch rest[0] {
		case '.':
			rest, wantName = rest[1:], true
			if rest == "" {
				return bad("ends with a dot")
			}
		case '[':
			step, n, problem := bracketStep(rest)
			if problem != "" {
				return bad(problem)
			}
			steps = append(steps, step)
			rest = rest[n:]
		default:
			return bad("needs a dot or [ after ]")
		}
	}
	return steps, nil
}

// bracketStep reads [0], [*] or ["key"] at the start of s, returning the
// step and how much of s it took.
func bracketStep(s string) (diff.Step, int, string) {
	if strings.HasPrefix(s, `["`) {
		quoted, err := strconv.QuotedPrefix(s[1:])
		if err != nil {
			return diff.Step{}, 0, "has a quoted key that is not closed"
		}
		end := 1 + len(quoted)
		if end >= len(s) || s[end] != ']' {
			return diff.Step{}, 0, "needs ] after the quoted key"
		}
		key, _ := strconv.Unquote(quoted)
		return diff.Step{Key: key}, end + 1, ""
	}
	end := strings.IndexByte(s, ']')
	if end < 0 {
		return diff.Step{}, 0, "has a [ that is not closed"
	}
	inner := s[1:end]
	switch {
	case inner == "*":
		return diff.Step{Any: true}, end + 1, ""
	case inner == "" || strings.Trim(inner, "0123456789") != "":
		return diff.Step{}, 0, fmt.Sprintf(`has [%s]; brackets hold a number, * or a quoted key such as ["Cost Center"]`, inner)
	}
	return diff.Step{Key: inner}, end + 1, ""
}

// UnmarshalYAML reads the policies list. Every error names the policy it
// is about, by position and ID, and the field at fault.
func (ps *Policies) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.SequenceNode {
		return errors.New("policies must be a list, with each policy starting with -")
	}
	for i, item := range n.Content {
		node := item
		for node.Kind == yaml.AliasNode {
			node = node.Alias
		}
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: a policy must be a mapping with id, title, severity and match", policyName(i, ""))
		}
		v, err := value(node)
		if err != nil {
			return fmt.Errorf("%s: %w", policyName(i, ""), err)
		}
		raw := v.(map[string]any)
		p, err := parsePolicy(raw)
		if err != nil {
			id, _ := text(raw["id"], "id")
			return fmt.Errorf("%s: %w", policyName(i, id), err)
		}
		*ps = append(*ps, p)
	}
	return nil
}

// value converts YAML to Go the way yaml.v3 decodes it into any, except
// that a number keeps the text it was written with: a json.Number when
// JSON reads it the same way, and a string otherwise. An unquoted 8.0 or
// 0755 then still matches the strings providers use for versions and file
// modes.
func value(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.AliasNode:
		return value(n.Alias)
	case yaml.SequenceNode:
		out := make([]any, len(n.Content))
		for i, c := range n.Content {
			v, err := value(c)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case yaml.MappingNode:
		// yaml.v3 resolves merge keys and rejects duplicate keys.
		var fields map[string]yaml.Node
		if err := n.Decode(&fields); err != nil {
			var te *yaml.TypeError
			if errors.As(err, &te) {
				err = errors.New(strings.Join(te.Errors, "; "))
			}
			return nil, err
		}
		out := make(map[string]any, len(fields))
		for _, k := range slices.Sorted(maps.Keys(fields)) {
			f := fields[k]
			v, err := value(&f)
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	}
	switch n.ShortTag() {
	case "!!null":
		return nil, nil
	case "!!bool":
		var b bool
		err := n.Decode(&b)
		return b, err
	case "!!int", "!!float":
		if json.Valid([]byte(n.Value)) {
			return json.Number(n.Value), nil
		}
	}
	return n.Value, nil
}

// policyName says which policy an error is about: policies[2] (prod-db-kept).
func policyName(i int, id string) string {
	if id == "" {
		return fmt.Sprintf("policies[%d]", i)
	}
	return fmt.Sprintf("policies[%d] (%s)", i, id)
}

func parsePolicy(raw map[string]any) (Policy, error) {
	if err := onlyKeys(raw, "key", policyKeys); err != nil {
		return Policy{}, err
	}
	var err error
	str := func(key string) string {
		s, e := text(raw[key], key)
		if err == nil {
			err = e
		}
		return s
	}
	p := Policy{ID: str("id"), Title: str("title"), Severity: str("severity"), Message: str("message"), Remediation: str("remediation")}
	if err != nil {
		return p, err
	}
	if p.Match, err = parseMatch(raw["match"]); err != nil {
		return p, err
	}
	p.When, err = parseWhen(raw["when"])
	return p, err
}

func parseMatch(v any) (Selector, error) {
	if v == nil {
		return Selector{}, nil
	}
	raw, ok := v.(map[string]any)
	if !ok {
		return Selector{}, errors.New("match must be a mapping, such as match: {type: aws_db_instance}")
	}
	if err := onlyKeys(raw, "match key", matchKeys); err != nil {
		return Selector{}, err
	}
	var err error
	list := func(key string) []string {
		l, e := strs(raw[key], "match."+key)
		if err == nil {
			err = e
		}
		return l
	}
	m := Selector{Type: list("type"), Address: list("address"), Module: list("module"), Provider: list("provider"), Action: list("action"), Category: list("category"), Origin: list("origin")}
	if err != nil {
		return m, err
	}
	if s := raw["stateful"]; s != nil {
		b, ok := s.(bool)
		if !ok {
			return m, errors.New("match.stateful must be true or false")
		}
		m.Stateful = &b
	}
	return m, nil
}

func parseWhen(v any) ([]Condition, error) {
	if v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, errors.New("when must be a list of conditions, each starting with -")
	}
	out := make([]Condition, len(items))
	for i, item := range items {
		c, err := parseCondition(item)
		if err != nil {
			return nil, fmt.Errorf("when[%d]: %w", i, err)
		}
		out[i] = c
	}
	return out, nil
}

func parseCondition(v any) (Condition, error) {
	raw, ok := v.(map[string]any)
	if !ok {
		return Condition{}, errors.New("a condition must be a mapping, such as {attribute: publicly_accessible, equals: true}")
	}
	for _, k := range slices.Sorted(maps.Keys(raw)) {
		if k != "attribute" && !slices.Contains(operators, k) {
			return Condition{}, fmt.Errorf("unknown key %q; use attribute and one of %s", k, either(operators))
		}
	}
	attr, err := text(raw["attribute"], "attribute")
	if err != nil {
		return Condition{}, err
	}
	c := Condition{Attribute: attr}
	var ops []string
	for _, op := range operators {
		if _, ok := raw[op]; ok {
			ops = append(ops, op)
		}
	}
	switch len(ops) {
	case 0:
		return c, fmt.Errorf("has no operator; use one of %s", either(operators))
	case 1:
		c.Operator, c.Value = ops[0], raw[ops[0]]
		return c, nil
	case 2:
		return c, fmt.Errorf("has both %s and %s; use one operator per condition", ops[0], ops[1])
	}
	return c, fmt.Errorf("has %s; use one operator per condition", strings.Join(ops[:len(ops)-1], ", ")+" and "+ops[len(ops)-1])
}

// Validate checks one policy on its own. Config.Validate also checks that
// no two policies share an ID.
func (p Policy) Validate() error {
	switch {
	case p.ID == "":
		return errors.New("id is required")
	case !kebabCase.MatchString(p.ID):
		return errors.New("id must be kebab-case: lowercase letters and digits joined by hyphens, such as prod-db-kept")
	case strings.TrimSpace(p.Title) == "":
		return errors.New("title is required")
	case p.Severity == "":
		return fmt.Errorf("severity is required; use %s", either(severityNames))
	}
	if sev, err := model.ParseSeverity(p.Severity); err != nil || sev == model.None {
		return fmt.Errorf("unknown severity %q; use %s", p.Severity, either(severityNames))
	}
	if err := p.Match.validate(); err != nil {
		return err
	}
	for i, c := range p.When {
		if err := c.validate(); err != nil {
			return fmt.Errorf("when[%d]: %w", i, err)
		}
	}
	return nil
}

func (m Selector) validate() error {
	if len(m.Type)+len(m.Address)+len(m.Module)+len(m.Provider)+len(m.Action)+len(m.Category)+len(m.Origin) == 0 && m.Stateful == nil {
		return fmt.Errorf(`match is empty; give at least one of %s (type: "*" matches every resource)`, either(matchKeys))
	}
	for _, f := range []struct {
		key    string
		values []string
	}{{"type", m.Type}, {"address", m.Address}, {"module", m.Module}, {"provider", m.Provider}} {
		if slices.Contains(f.values, "") {
			return fmt.Errorf("match.%s has an empty value", f.key)
		}
	}
	for _, a := range m.Action {
		if _, ok := ParseAction(a); !ok {
			return fmt.Errorf("unknown action %q; use %s", a, either(actionNames))
		}
	}
	var categories []string
	for _, c := range kb.Categories {
		categories = append(categories, string(c))
	}
	for _, c := range m.Category {
		if !slices.Contains(categories, strings.ToLower(c)) {
			return fmt.Errorf("unknown category %q; use %s", c, either(categories))
		}
	}
	for _, o := range m.Origin {
		if !slices.Contains(Origins, o) {
			return fmt.Errorf("unknown origin %q; use %s", o, either(Origins))
		}
	}
	return nil
}

func (c Condition) validate() error {
	if _, err := ParsePath(c.Attribute); err != nil {
		return err
	}
	switch c.Operator {
	case "":
		return fmt.Errorf("has no operator; use one of %s", either(operators))
	case "equals", "not_equals", "contains":
		if c.Value == nil {
			return fmt.Errorf("%s needs a value; to check whether %s is set at all, use present", c.Operator, c.Attribute)
		}
	case "in":
		l, ok := c.Value.([]any)
		if !ok || len(l) == 0 {
			return errors.New("in takes a list of values, such as in: [db.t4g.small, db.t4g.medium]")
		}
		if slices.Contains(l, nil) {
			return fmt.Errorf("in cannot hold null; to check whether %s is set at all, use present", c.Attribute)
		}
	case "matches":
		s, ok := c.Value.(string)
		if !ok {
			return errors.New("matches takes a regular expression, written as a string")
		}
		if _, err := regexp.Compile(s); err != nil {
			reason := err.Error()
			var se *syntax.Error
			if errors.As(err, &se) {
				reason = se.Code.String()
			}
			return fmt.Errorf("matches %q is not a valid regular expression: %s", s, reason)
		}
	case "present", "changes":
		if _, ok := c.Value.(bool); !ok {
			return fmt.Errorf("%s takes true or false", c.Operator)
		}
	default:
		return fmt.Errorf("unknown operator %q; use %s", c.Operator, either(operators))
	}
	return nil
}

// onlyKeys rejects keys of a mapping that are not in known.
func onlyKeys(raw map[string]any, what string, known []string) error {
	for _, k := range slices.Sorted(maps.Keys(raw)) {
		if !slices.Contains(known, k) {
			return fmt.Errorf("unknown %s %q; use %s", what, k, either(known))
		}
	}
	return nil
}

// text reads a scalar as text. Numbers and booleans are taken as written,
// so a title such as 1.10 needs no quotes.
func text(v any, field string) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case json.Number:
		return string(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	}
	return "", fmt.Errorf("%s must be text, not a list or mapping", field)
}

// strs reads one string, or a list of them.
func strs(v any, field string) ([]string, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{x}, nil
	case []any:
		if len(x) == 0 {
			return nil, fmt.Errorf("%s is an empty list", field)
		}
		out := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a string or a list of strings", field)
			}
			out[i] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s must be a string or a list of strings", field)
}

// either joins words as "a, b or c".
func either(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}
