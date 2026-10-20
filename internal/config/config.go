// Package config loads .interragate.yaml, which tunes interragate's rules
// for a repository and adds the team's own, as policies.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/scan"
)

// FileNames are the names interragate looks for, nearest directory first.
var FileNames = []string{".interragate.yaml", ".interragate.yml"}

// Config is the parsed configuration file.
type Config struct {
	// FailOn is the lowest severity that makes `interragate check` fail.
	FailOn string `yaml:"fail_on"`
	// Disable turns rules off entirely.
	Disable []string `yaml:"disable"`
	// Severity overrides a rule's severity.
	Severity map[string]string `yaml:"severity"`
	// Ignore suppresses findings for matching addresses.
	Ignore []Ignore `yaml:"ignore"`
	// RequiredTags enables the required-tags rule.
	RequiredTags []string `yaml:"required_tags"`
	// Stateful marks extra resource types as holding data.
	Stateful []string `yaml:"stateful"`
	// Rules holds per-rule settings, such as thresholds.
	Rules map[string]map[string]any `yaml:"rules"`
	// Policies are the team's own rules.
	Policies Policies `yaml:"policies"`
	// Scan names configuration scanners to run with every plan: trivy,
	// checkov or tflint.
	Scan []string `yaml:"scan"`
	// Consumers are the services outside Terraform that use what it
	// manages.
	Consumers Consumers `yaml:"consumers"`

	// Path is where the configuration was loaded from, if anywhere.
	Path string `yaml:"-"`
}

// Ignore suppresses one rule (or every rule, with "*") for addresses
// matching a pattern in which * matches any run of characters.
type Ignore struct {
	Rule    string `yaml:"rule"`
	Address string `yaml:"address"`
	Reason  string `yaml:"reason"`
}

// Find looks for a configuration file in dir and its parents.
func Find(dir string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		for _, name := range FileNames {
			p := filepath.Join(abs, name)
			if _, err := os.Stat(p); err == nil {
				return p, true
			}
		}
		if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			return "", false
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", false
		}
		abs = parent
	}
}

// Option changes how Load reads a configuration file.
type Option func(*loading)

type loading struct {
	// reserved are the IDs of interragate's own rules, which no policy
	// may take.
	reserved map[string]bool
}

// ReserveRuleIDs keeps the team's policies off the IDs of interragate's own
// rules, which package rules lists.
func ReserveRuleIDs(ids ...string) Option {
	return func(l *loading) {
		for _, id := range ids {
			l.reserved[id] = true
		}
	}
}

// Load reads and validates a configuration file.
func Load(path string, opts ...Option) (*Config, error) {
	l := loading{reserved: map[string]bool{}}
	for _, opt := range opts {
		opt(&l)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the configuration: %w", err)
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Path = path
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i, p := range c.Policies {
		if l.reserved[p.ID] {
			return nil, fmt.Errorf("%s: %s: id is taken by a built-in rule; choose another (to change the built-in rule, use severity or disable)", path, policyName(i, p.ID))
		}
	}
	return &c, nil
}

// Validate checks severities, ignore entries, policies, scanners and the
// services outside Terraform.
func (c *Config) Validate() error {
	for _, s := range c.Scan {
		if !slices.Contains(scan.Tools, s) {
			return fmt.Errorf("scan: unknown scanner %q; use %s", s, either(scan.Tools))
		}
	}
	if c.FailOn != "" {
		if _, err := model.ParseSeverity(c.FailOn); err != nil {
			return fmt.Errorf("fail_on: %w", err)
		}
	}
	for rule, sev := range c.Severity {
		if _, err := model.ParseSeverity(sev); err != nil {
			return fmt.Errorf("severity.%s: %w", rule, err)
		}
	}
	for i, ig := range c.Ignore {
		if ig.Rule == "" {
			return fmt.Errorf("ignore[%d]: rule is required (use \"*\" for every rule)", i)
		}
		if ig.Address == "" {
			return fmt.Errorf("ignore[%d]: address is required (use \"*\" for every address)", i)
		}
	}
	first := map[string]int{}
	for i, p := range c.Policies {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("%s: %w", policyName(i, p.ID), err)
		}
		if j, taken := first[p.ID]; taken {
			return fmt.Errorf("%s: id is already used by policies[%d]", policyName(i, p.ID), j)
		}
		first[p.ID] = i
	}
	// Each service was checked as it was read; names must also differ.
	named := make(map[string]int, len(c.Consumers))
	for i, svc := range c.Consumers {
		key := strings.ToLower(string(svc.Name))
		if j, taken := named[key]; taken {
			return fmt.Errorf("%s: name is already used by consumers[%d]", consumerName(i, string(svc.Name)), j)
		}
		named[key] = i
	}
	return nil
}

// Disabled reports whether a rule is turned off.
func (c *Config) Disabled(rule string) bool {
	if c == nil {
		return false
	}
	for _, d := range c.Disable {
		if d == rule {
			return true
		}
	}
	return false
}

// SeverityFor returns the configured severity for a rule, if overridden.
func (c *Config) SeverityFor(rule string) (model.Severity, bool) {
	if c == nil {
		return model.None, false
	}
	s, ok := c.Severity[rule]
	if !ok {
		return model.None, false
	}
	sev, err := model.ParseSeverity(s)
	return sev, err == nil
}

// Ignored reports whether every address of a finding is ignored for rule,
// returning the reason given.
func (c *Config) Ignored(rule string, addresses []string) (string, bool) {
	if c == nil || len(addresses) == 0 {
		return "", false
	}
	reason := ""
	for _, addr := range addresses {
		matched := false
		for _, ig := range c.Ignore {
			if (ig.Rule == "*" || ig.Rule == rule) && Match(ig.Address, addr) {
				matched = true
				if reason == "" {
					reason = ig.Reason
				}
				break
			}
		}
		if !matched {
			return "", false
		}
	}
	if reason == "" {
		reason = "ignored in " + filepath.Base(c.Path)
	}
	return reason, true
}

// Setting returns a per-rule setting.
func (c *Config) Setting(rule, key string) (any, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Rules[rule][key]
	return v, ok
}

// Match reports whether s matches pattern, where * matches any run of
// characters and everything else is literal.
func Match(pattern, s string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i, p := range parts[1:] {
		if i == len(parts)-2 {
			return strings.HasSuffix(s, p)
		}
		j := strings.Index(s, p)
		if j < 0 {
			return false
		}
		s = s[j+len(p):]
	}
	return true
}
