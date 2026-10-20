// Package model defines the report interragate produces for a plan. Every
// renderer (terminal, Markdown, JSON, HTML) reads this one structure, and the
// JSON form is the contract for tools built on top of interragate.
package model

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
)

// SchemaVersion is bumped whenever the JSON report changes incompatibly.
const SchemaVersion = 1

// Severity orders findings and changes by how much attention they need.
type Severity int

// Severities, least to most severe.
const (
	None Severity = iota
	Info
	Low
	Medium
	High
	Critical
)

var severityNames = []string{"none", "info", "low", "medium", "high", "critical"}

func (s Severity) String() string {
	if s < None || s > Critical {
		return "unknown"
	}
	return severityNames[s]
}

// ParseSeverity parses a severity name.
func ParseSeverity(s string) (Severity, error) {
	for i, name := range severityNames {
		if strings.EqualFold(s, name) {
			return Severity(i), nil
		}
	}
	return None, fmt.Errorf("unknown severity %q (want one of %s)", s, strings.Join(severityNames[1:], ", "))
}

// MarshalJSON encodes a severity as its name.
func (s Severity) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// UnmarshalJSON decodes a severity name.
func (s *Severity) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err != nil {
		return err
	}
	v, err := ParseSeverity(name)
	*s = v
	return err
}

// Action is what the plan does to a resource instance.
type Action string

// Actions. Moves and imports are flags on a Change, since they combine with
// other actions.
const (
	Create  Action = "create"
	Update  Action = "update"
	Replace Action = "replace"
	Delete  Action = "delete"
	Read    Action = "read"
	Forget  Action = "forget"
	NoOp    Action = "no-op"
)

// Rank orders actions by how disruptive they are, for picking the headline
// action of a group of instances.
func (a Action) Rank() int {
	switch a {
	case Delete:
		return 7
	case Replace:
		return 6
	case Forget:
		return 5
	case Create:
		return 4
	case Update:
		return 3
	case Read:
		return 2
	case NoOp:
		return 1
	}
	return 0
}

// Report is everything interragate concluded about one plan.
type Report struct {
	Schema      int        `json:"schema"`
	Generator   string     `json:"generator"`
	GeneratedAt string     `json:"generated_at"`
	Plan        PlanInfo   `json:"plan"`
	Summary     Summary    `json:"summary"`
	Risk        Risk       `json:"risk"`
	Narrative   []Sentence `json:"narrative"`
	Changes     []*Change  `json:"changes"`
	Outputs     []*Output  `json:"outputs"`
	Drift       []*Drift   `json:"drift"`
	Findings    []*Finding `json:"findings"`
	// RuleDocs says what each rule with a finding looks for and why.
	RuleDocs   map[string]string `json:"rule_docs,omitempty"`
	Suppressed []*Finding        `json:"suppressed,omitempty"`
	Patterns   []*Pattern        `json:"patterns,omitempty"`
	Checks     []*Check          `json:"checks,omitempty"`
	Graph      *Graph            `json:"graph"`
	Apply      *Apply            `json:"apply,omitempty"`
	Git        *GitInfo          `json:"git,omitempty"`
	// Scans says what each scanner interragate ran reported, and how much of
	// it concerns this plan's changes.
	Scans []Scan `json:"scans,omitempty"`
	// Consumers are the services outside Terraform that .interragate.yaml
	// declares, each with what this plan does to what it uses.
	Consumers []*Consumer `json:"consumers,omitempty"`
}

// Scan is one scanner's report on the configuration.
type Scan struct {
	Tool   string `json:"tool"`
	Issues int    `json:"issues"` // everything it reported
	Shown  int    `json:"shown"`  // issues on what this plan creates, replaces or edits
	Error  string `json:"error,omitempty"`
}

// GitInfo says what the configuration was compared with to find the edit
// behind each change.
type GitInfo struct {
	// Base names what was compared with as the user would: "origin/main"
	// for where the branch left it, "HEAD" for uncommitted work only.
	Base   string `json:"base"`
	Commit string `json:"commit"` // the commit compared with, abbreviated
	Branch string `json:"branch,omitempty"`
	// Files are the configuration files that differ, relative to the
	// Terraform directory.
	Files []string `json:"files"`
	// Dirty is set when uncommitted changes to the configuration are
	// included.
	Dirty bool `json:"dirty,omitempty"`
	// Providers lists providers whose locked version differs from the
	// base's dependency lock file.
	Providers []ProviderChange `json:"providers,omitempty"`
	// Codeowners is the repository's CODEOWNERS file, when it has one,
	// relative to the top of the repository.
	Codeowners string `json:"codeowners,omitempty"`
}

// ProviderChange is a provider whose locked version moved. From is empty
// for a provider the base did not lock, To for one no longer locked.
type ProviderChange struct {
	Source string `json:"source"` // registry.terraform.io/hashicorp/aws
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

// PlanInfo describes the plan that was analysed.
type PlanInfo struct {
	Tool          string `json:"tool,omitempty"`
	Version       string `json:"version"`
	FormatVersion string `json:"format_version"`
	Timestamp     string `json:"timestamp,omitempty"`
	Source        string `json:"source,omitempty"`
	Dir           string `json:"dir,omitempty"`
	Applyable     *bool  `json:"applyable,omitempty"`
	Complete      *bool  `json:"complete,omitempty"`
	Errored       bool   `json:"errored,omitempty"`
	Destroy       bool   `json:"destroy,omitempty"`
	SourceRead    bool   `json:"source_read"`
	// Workspace is the Terraform workspace the plan was made in, when
	// interragate ran the plan itself and so knows it.
	Workspace string `json:"workspace,omitempty"`
	// Providers maps the short provider names changes use ("aws") to
	// their source addresses ("registry.terraform.io/hashicorp/aws"), so
	// renderers can link to the documentation.
	Providers map[string]string `json:"providers,omitempty"`
}

// ToolName is the product that made the plan, for prose.
func (p PlanInfo) ToolName() string {
	if p.Tool == "opentofu" {
		return "OpenTofu"
	}
	return "Terraform"
}

// Command is the binary users type for the tool.
func (p PlanInfo) Command() string {
	if p.Tool == "opentofu" {
		return "tofu"
	}
	return "terraform"
}

// Summary counts changes by kind.
type Summary struct {
	Create   int `json:"create"`
	Update   int `json:"update"`
	Replace  int `json:"replace"`
	Delete   int `json:"delete"`
	Read     int `json:"read"`
	Forget   int `json:"forget"`
	Move     int `json:"move"`
	Import   int `json:"import"`
	NoOp     int `json:"no_op"`
	Cosmetic int `json:"cosmetic"`
	Indirect int `json:"indirect"`
	Drifted  int `json:"drifted"`
	Outputs  int `json:"outputs"`
	Deferred int `json:"deferred,omitempty"`
}

// Total is the number of resource instances the plan does anything to.
func (s Summary) Total() int {
	return s.Create + s.Update + s.Replace + s.Delete + s.Read + s.Forget + s.Move + s.Import
}

// Risk is the overall verdict.
type Risk struct {
	Level    Severity       `json:"level"`
	Headline string         `json:"headline"`
	Counts   map[string]int `json:"counts"`
}

// Sentence is one line of the plain-English summary. Text uses backticks
// around resource addresses so renderers can style or link them.
type Sentence struct {
	Severity Severity `json:"severity"`
	Text     string   `json:"text"`
	Refs     []string `json:"refs,omitempty"`
}

// Change is the analysed change to one resource instance.
type Change struct {
	Address     string     `json:"address"`
	Resource    string     `json:"resource"`
	PrevAddress string     `json:"prev_address,omitempty"`
	Module      string     `json:"module,omitempty"`
	Mode        string     `json:"mode"`
	Type        string     `json:"type"`
	Name        string     `json:"name"`
	Key         string     `json:"key,omitempty"`
	Provider    string     `json:"provider"`
	Action      Action     `json:"action"`
	Order       string     `json:"order,omitempty"`
	Moved       bool       `json:"moved,omitempty"`
	Importing   bool       `json:"importing,omitempty"`
	ImportID    string     `json:"import_id,omitempty"`
	Deposed     string     `json:"deposed,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	Why         string     `json:"why,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	ForcedBy    []string   `json:"forced_by,omitempty"`
	Noun        string     `json:"noun"`
	Category    string     `json:"category"`
	Stateful    bool       `json:"stateful,omitempty"`
	Severity    Severity   `json:"severity"`
	Cosmetic    string     `json:"cosmetic,omitempty"`
	Indirect    bool       `json:"indirect,omitempty"`
	Causes      []Cause    `json:"causes,omitempty"`
	Changed     int        `json:"changed"`
	Unknown     int        `json:"unknown"`
	Diff        *diff.Node `json:"diff,omitempty"`
	Findings    []string   `json:"findings,omitempty"`
	Location    *Location  `json:"location,omitempty"`
	Edit        *Edit      `json:"edit,omitempty"`
	// Origin says where the change comes from when the configuration was
	// compared with git: "edit" when its block (or the module call it sits
	// under) was edited, "follows" when it only changes because of such a
	// change, and empty when it is not tied to an edit.
	Origin string `json:"origin,omitempty"`
	// Access is what the change allows that was not allowed before, and
	// what it stops allowing: IAM permissions and network paths, in words.
	Access []AccessLine `json:"access,omitempty"`
	// Deploys are new versions of what runs that the change ships: an
	// image, a chart, a function's code.
	Deploys []Deploy `json:"deploys,omitempty"`
	// Owners are who CODEOWNERS says owns the file the resource's block is
	// in, when the repository has one.
	Owners     []string `json:"owners,omitempty"`
	Drifted    bool     `json:"drifted,omitempty"`
	Dependents int      `json:"dependents,omitempty"`
}

// Label is the change's action as a short verb phrase.
func (c *Change) Label() string {
	switch c.Action {
	case Create:
		if c.Importing {
			return "import"
		}
		return "create"
	case Update:
		if c.Importing {
			return "import + update"
		}
		return "update"
	case Replace:
		if c.Importing {
			return "import + replace"
		}
		return "replace"
	case Delete:
		return "destroy"
	case Read:
		return "read"
	case Forget:
		return "forget"
	case NoOp:
		if c.Importing {
			return "import"
		}
		if c.Moved {
			return "move"
		}
		return "no change"
	}
	return string(c.Action)
}

// Cause explains that an attribute changes because of an upstream change.
type Cause struct {
	Attribute string `json:"attribute"`
	Resource  string `json:"resource"`
	Address   string `json:"address,omitempty"`
	Action    Action `json:"action"`
	Via       string `json:"via,omitempty"`
}

// Location is where a resource is defined.
type Location struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"`
}

// Edit is how the configuration behind a change differs from the git base:
// the lines of its block that were changed, or of the block that was
// deleted, or of the module call it sits under.
// Deploy is a new version of something that runs.
type Deploy struct {
	What    string `json:"what"`              // the image's repository, a container, "chart x" or "code"
	From    string `json:"from,omitempty"`    // the version before: a tag, a digest, a chart version
	To      string `json:"to,omitempty"`      // the version after
	Unknown bool   `json:"unknown,omitempty"` // the new version is known after apply
}

// AccessLine is a permission or network path a change adds or removes.
type AccessLine struct {
	Op   string `json:"op"`             // "+" when the change allows it, "-" when it stops allowing it
	Kind string `json:"kind"`           // "iam" or "network"
	Text string `json:"text"`           // in words, with code spans in backticks
	Wide bool   `json:"wide,omitempty"` // allows anyone, every action, or the whole internet in
}

type Edit struct {
	File string `json:"file"` // relative to the Terraform directory
	// Kind is "edited", "added" (the whole block is new), "removed" (the
	// block was deleted), "module" (the call to the module that holds it
	// was edited or removed, and its own block was not), "input" (it
	// reads a variable or local that was edited) or "provider" (only its
	// tags change, after an edit to its provider's block).
	Kind string `json:"kind"`
	// Module is the call that was edited, such as "module.network", for
	// kind "module".
	Module string `json:"module,omitempty"`
	// Input is the variable or local that was edited, such as "var.azs",
	// for kind "input": the change reads it.
	Input string     `json:"input,omitempty"`
	Lines []EditLine `json:"lines"`
	// Omitted counts the changed lines left out after the first few dozen.
	Omitted int `json:"omitted,omitempty"`
}

// EditLine is one line that differs from the base.
type EditLine struct {
	Op string `json:"op"` // "+" or "-"
	// Line is the line number in the file now for "+", and in the base
	// for "-".
	Line int    `json:"line,omitempty"`
	Text string `json:"text"`
}

// Output is a change to a root module output.
type Output struct {
	Name      string     `json:"name"`
	Action    Action     `json:"action"`
	Sensitive bool       `json:"sensitive,omitempty"`
	Diff      *diff.Node `json:"diff,omitempty"`
}

// Drift is a change Terraform found between state and the real object.
type Drift struct {
	Address  string     `json:"address"`
	Action   Action     `json:"action"`
	Diff     *diff.Node `json:"diff,omitempty"`
	Reverted []string   `json:"reverted,omitempty"`
	Kept     []string   `json:"kept,omitempty"`
	Noise    bool       `json:"noise,omitempty"`
	Summary  string     `json:"summary"`
}

// Finding is something a rule flagged.
type Finding struct {
	ID          string   `json:"id"`
	Rule        string   `json:"rule"`
	Severity    Severity `json:"severity"`
	Category    string   `json:"category"`
	Title       string   `json:"title"`
	Short       string   `json:"short,omitempty"`
	Message     string   `json:"message"`
	Addresses   []string `json:"addresses"`
	Paths       []string `json:"paths,omitempty"`
	Remediation string   `json:"remediation,omitempty"`
	Snippet     string   `json:"snippet,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	// Fails is the operation on the first address (create, update or
	// destroy) at which the apply is expected to stop with an error.
	Fails string `json:"fails,omitempty"`
	// Consumers names the services outside Terraform the finding is about.
	Consumers []ServiceName `json:"consumers,omitempty"`
	// About is what the rule looks for and why; the report keeps it once
	// per rule, in RuleDocs.
	About string `json:"-"`
}

// Pattern is the same attribute change repeated across many resources.
type Pattern struct {
	Path      string   `json:"path"`
	Before    any      `json:"before"`
	After     any      `json:"after"`
	Count     int      `json:"count"`
	Addresses []string `json:"addresses"`
	Text      string   `json:"text"`
}

// Check is the result of a check block or condition.
type Check struct {
	Address  string   `json:"address"`
	Kind     string   `json:"kind"`
	Status   string   `json:"status"`
	Problems []string `json:"problems,omitempty"`
}

// Graph is the resource dependency graph, one node per configuration
// resource, for the visual map.
type Graph struct {
	Nodes []*GraphNode `json:"nodes"`
	Edges [][2]string  `json:"edges"`
}

// GraphNode is one configuration resource with the actions planned for its
// instances.
type GraphNode struct {
	ID        string         `json:"id"`
	Module    string         `json:"module,omitempty"`
	Mode      string         `json:"mode"`
	Type      string         `json:"type"`
	Name      string         `json:"name"`
	Noun      string         `json:"noun"`
	Category  string         `json:"category"`
	Action    Action         `json:"action"`
	Actions   map[Action]int `json:"actions"`
	Instances []string       `json:"instances"`
	Severity  Severity       `json:"severity"`
	Location  *Location      `json:"location,omitempty"`
}

// Apply is interragate's simulation of how the apply will run: the order
// Terraform works through the changes in, roughly how long each takes, and
// when a replaced resource does not exist. Times are seconds from the start
// of the apply, built from typical durations, so they sketch the apply rather
// than predict it.
type Apply struct {
	Steps       []*ApplyStep `json:"steps"`               // in the order they start
	Joins       []*ApplyJoin `json:"joins,omitempty"`     // by ID
	Duration    int          `json:"duration"`            // estimated seconds until the last step ends
	Critical    []string     `json:"critical"`            // step IDs on the chain of waits that ends last, in order; joins on it are left out
	Gaps        []*ApplyGap  `json:"gaps,omitempty"`      // in the order they open
	Parallelism int          `json:"parallelism"`         // how many steps run at once; Terraform's default is 10
	Narrative   []Sentence   `json:"narrative,omitempty"` // how long it takes and what goes missing, in plain English
}

// ApplyStep is one operation on a resource instance. A replacement is two
// steps, a destroy and a create, in the order the plan gives.
type ApplyStep struct {
	ID       string   `json:"id"`                // address and operation, e.g. "aws_db_instance.orders:destroy"
	Address  string   `json:"address"`           // the change's address
	Op       string   `json:"op"`                // create, update, destroy or read
	Deposed  bool     `json:"deposed,omitempty"` // destroys the old object once its replacement exists (create_before_destroy)
	After    []string `json:"after,omitempty"`   // IDs of the steps and joins that must finish first, leaving out ones those already wait for
	Start    int      `json:"start"`             // estimated seconds from the start of the apply
	Duration int      `json:"duration"`          // estimated seconds
	Known    bool     `json:"known,omitempty"`   // the duration comes from interragate's table of slow resource types, not the default
	Fails    string   `json:"fails,omitempty"`   // ID of the finding that expects this step to fail
}

// ApplyJoin stands for several steps of one operation on a resource's
// instances, in one module instance or across them. An instance waits for
// every instance of a resource it refers to, so a step that waits for all
// of them lists the join rather than each one, as Terraform's graph joins
// them in one node; the waits then grow with the number of instances
// rather than with their product. A join takes no time: it is done when
// everything in its After is.
type ApplyJoin struct {
	ID       string   `json:"id"`       // the steps' address and operation, with [*] where their addresses differ, e.g. "aws_subnet.private[*]:update"
	Resource string   `json:"resource"` // the configuration address, as in changes
	Op       string   `json:"op"`       // create, update, destroy or read
	After    []string `json:"after"`    // the steps it stands for, or one join per module instance
}

// ApplyGap is the window in which a resource that is destroyed before its
// replacement is created does not exist.
type ApplyGap struct {
	Address    string   `json:"address"`
	From       int      `json:"from"`                 // its destroy starts
	To         int      `json:"to"`                   // its replacement has been created
	Dependents []string `json:"dependents,omitempty"` // resources that read it and may fail or misbehave meanwhile
}
