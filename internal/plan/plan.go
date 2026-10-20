// Package plan decodes the machine-readable plan that `terraform show -json`
// and `tofu show -json` print for a saved plan file.
//
// The types mirror the documented JSON format (format_version 1.x). Values are
// decoded as plain Go values (map[string]any, []any, string, json.Number,
// bool, nil) so that nothing is lost for providers we know nothing about.
package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Plan is the top-level JSON plan document.
type Plan struct {
	FormatVersion      string              `json:"format_version"`
	TerraformVersion   string              `json:"terraform_version"`
	Variables          map[string]Variable `json:"variables"`
	PlannedValues      *Values             `json:"planned_values"`
	ResourceDrift      []*ResourceChange   `json:"resource_drift"`
	ResourceChanges    []*ResourceChange   `json:"resource_changes"`
	DeferredChanges    []*DeferredChange   `json:"deferred_changes"`
	OutputChanges      map[string]*Change  `json:"output_changes"`
	PriorState         *State              `json:"prior_state"`
	Configuration      *Configuration      `json:"configuration"`
	RelevantAttributes []RelevantAttribute `json:"relevant_attributes"`
	Checks             []*Check            `json:"checks"`
	Timestamp          string              `json:"timestamp"`
	Applyable          *bool               `json:"applyable"`
	Complete           *bool               `json:"complete"`
	Errored            bool                `json:"errored"`

	// StateValues is only present when the document is a state, not a plan.
	StateValues json.RawMessage `json:"values,omitempty"`
}

// Variable is a root module input variable value.
type Variable struct {
	Value any `json:"value"`
}

// ResourceChange describes the planned change to one resource instance.
type ResourceChange struct {
	Address         string          `json:"address"`
	PreviousAddress string          `json:"previous_address,omitempty"`
	ModuleAddress   string          `json:"module_address,omitempty"`
	Mode            string          `json:"mode"`
	Type            string          `json:"type"`
	Name            string          `json:"name"`
	Index           json.RawMessage `json:"index,omitempty"`
	ProviderName    string          `json:"provider_name"`
	Deposed         string          `json:"deposed,omitempty"`
	Change          Change          `json:"change"`
	ActionReason    string          `json:"action_reason,omitempty"`
}

// DeferredChange is a change Terraform could not plan yet (deferred actions).
type DeferredChange struct {
	Reason         string          `json:"reason"`
	ResourceChange *ResourceChange `json:"resource_change"`
}

// Change is the before/after description shared by resources and outputs.
type Change struct {
	Actions         []string   `json:"actions"`
	Before          any        `json:"before"`
	After           any        `json:"after"`
	AfterUnknown    any        `json:"after_unknown"`
	BeforeSensitive any        `json:"before_sensitive"`
	AfterSensitive  any        `json:"after_sensitive"`
	ReplacePaths    [][]any    `json:"replace_paths,omitempty"`
	Importing       *Importing `json:"importing,omitempty"`
	GeneratedConfig string     `json:"generated_config,omitempty"`
}

// Importing is set when the change imports an existing object.
type Importing struct {
	ID       string `json:"id,omitempty"`
	Identity any    `json:"identity,omitempty"`
	Unknown  bool   `json:"unknown,omitempty"`
}

// RelevantAttribute names a resource attribute that contributed to the plan.
type RelevantAttribute struct {
	Resource  string `json:"resource"`
	Attribute []any  `json:"attribute"`
}

// Check is the status of a check block, precondition or postcondition.
type Check struct {
	Address   CheckAddress     `json:"address"`
	Status    string           `json:"status"`
	Instances []*CheckInstance `json:"instances"`
}

// CheckAddress identifies a checkable object.
type CheckAddress struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Module    string `json:"module,omitempty"`
	ToDisplay string `json:"to_display"`
}

// CheckInstance is one instance of a checkable object.
type CheckInstance struct {
	Address  map[string]any  `json:"address"`
	Status   string          `json:"status"`
	Problems []*CheckProblem `json:"problems,omitempty"`
}

// CheckProblem is a failed assertion message.
type CheckProblem struct {
	Message string `json:"message"`
}

// Values is the "values representation" used by planned_values and states.
type Values struct {
	Outputs    map[string]*StateOutput `json:"outputs"`
	RootModule *StateModule            `json:"root_module"`
}

// State is a state snapshot as embedded in the plan (prior_state).
type State struct {
	FormatVersion    string  `json:"format_version"`
	TerraformVersion string  `json:"terraform_version"`
	Values           *Values `json:"values"`
}

// StateOutput is an output value in a values representation.
type StateOutput struct {
	Value     any  `json:"value"`
	Sensitive bool `json:"sensitive"`
}

// StateModule is a module in a values representation.
type StateModule struct {
	Address      string           `json:"address,omitempty"`
	Resources    []*StateResource `json:"resources"`
	ChildModules []*StateModule   `json:"child_modules"`
}

// StateResource is one resource instance in a values representation.
type StateResource struct {
	Address         string          `json:"address"`
	Mode            string          `json:"mode"`
	Type            string          `json:"type"`
	Name            string          `json:"name"`
	Index           json.RawMessage `json:"index,omitempty"`
	ProviderName    string          `json:"provider_name"`
	SchemaVersion   int             `json:"schema_version"`
	Values          map[string]any  `json:"values"`
	SensitiveValues any             `json:"sensitive_values"`
	DependsOn       []string        `json:"depends_on"`
	Tainted         bool            `json:"tainted"`
	DeposedKey      string          `json:"deposed_key"`
}

// Configuration is the static configuration the plan was made from.
type Configuration struct {
	ProviderConfig map[string]*ProviderConfig `json:"provider_config"`
	RootModule     *ConfigModule              `json:"root_module"`
}

// ProviderConfig is a provider block or required_providers entry.
type ProviderConfig struct {
	Name              string         `json:"name"`
	FullName          string         `json:"full_name"`
	Alias             string         `json:"alias"`
	ModuleAddress     string         `json:"module_address"`
	VersionConstraint string         `json:"version_constraint"`
	Expressions       map[string]any `json:"expressions"`
}

// ConfigModule is one module's configuration.
type ConfigModule struct {
	Outputs     map[string]*ConfigOutput   `json:"outputs"`
	Resources   []*ConfigResource          `json:"resources"`
	ModuleCalls map[string]*ModuleCall     `json:"module_calls"`
	Variables   map[string]*ConfigVariable `json:"variables"`
}

// ConfigResource is a resource or data block.
type ConfigResource struct {
	Address           string         `json:"address"`
	Mode              string         `json:"mode"`
	Type              string         `json:"type"`
	Name              string         `json:"name"`
	ProviderConfigKey string         `json:"provider_config_key"`
	Expressions       map[string]any `json:"expressions"`
	SchemaVersion     int            `json:"schema_version"`
	CountExpression   *Expression    `json:"count_expression"`
	ForEachExpression *Expression    `json:"for_each_expression"`
	DependsOn         []string       `json:"depends_on"`
}

// ModuleCall is a module block.
type ModuleCall struct {
	Source            string         `json:"source"`
	VersionConstraint string         `json:"version_constraint"`
	Expressions       map[string]any `json:"expressions"`
	CountExpression   *Expression    `json:"count_expression"`
	ForEachExpression *Expression    `json:"for_each_expression"`
	DependsOn         []string       `json:"depends_on"`
	Module            *ConfigModule  `json:"module"`
}

// ConfigOutput is an output block.
type ConfigOutput struct {
	Expression  *Expression `json:"expression"`
	Sensitive   bool        `json:"sensitive"`
	Description string      `json:"description"`
	DependsOn   []string    `json:"depends_on"`
}

// ConfigVariable is a variable block. Terraform does not include the type.
type ConfigVariable struct {
	Default     any    `json:"default"`
	Description string `json:"description"`
	Sensitive   bool   `json:"sensitive"`
}

// Expression is a configuration expression: a constant or a set of references.
type Expression struct {
	ConstantValue any      `json:"constant_value"`
	References    []string `json:"references"`
}

// Decode reads a JSON plan. It rejects documents that are not plans, such as
// state files or the streaming `-json` UI output, with a helpful message.
func Decode(r io.Reader) (*Plan, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes a JSON plan from memory.
func Parse(data []byte) (*Plan, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, fmt.Errorf("plan is empty")
	}
	if data[0] != '{' {
		return nil, fmt.Errorf("not a JSON plan: expected the output of `terraform show -json <planfile>`")
	}
	if bytes.Contains(data[:min(len(data), 512)], []byte(`"@level"`)) {
		return nil, fmt.Errorf("this looks like the streaming output of `terraform plan -json`; save the plan with `-out` and pass the plan file or `terraform show -json <planfile>` instead")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var p Plan
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("decoding plan JSON: %w", err)
	}
	if p.FormatVersion == "" {
		return nil, fmt.Errorf("not a plan: missing format_version")
	}
	if major, _, _ := strings.Cut(p.FormatVersion, "."); major != "1" && major != "0" {
		return nil, fmt.Errorf("unsupported plan format_version %q", p.FormatVersion)
	}
	if p.FormatVersion == "0.1" {
		// Terraform 0.12 to 0.14 did not mark sensitive values in plans,
		// so a report could expose them.
		return nil, fmt.Errorf("this plan comes from Terraform 0.14 or earlier, which does not mark sensitive values; interragate needs Terraform 0.15+ or OpenTofu")
	}
	if p.StateValues != nil && p.PlannedValues == nil {
		return nil, fmt.Errorf("this is a state, not a plan; run `terraform plan -out tfplan` and pass tfplan")
	}
	p.ResourceChanges = compact(p.ResourceChanges)
	p.ResourceDrift = compact(p.ResourceDrift)
	p.Checks = compact(p.Checks)
	p.DeferredChanges = compact(p.DeferredChanges)
	for k, v := range p.OutputChanges {
		if v == nil {
			delete(p.OutputChanges, k)
		}
	}
	return &p, nil
}

// compact drops null entries, which Terraform never writes but hand-made
// or truncated JSON can contain.
func compact[T any](in []*T) []*T {
	out := in[:0]
	for _, v := range in {
		if v != nil {
			out = append(out, v)
		}
	}
	return out
}
