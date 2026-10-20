// Package sarif writes findings as SARIF 2.1.0, the format code scanning
// tools such as GitHub's read, each anchored to the block of the resource
// it is about.
package sarif

import (
	"encoding/json"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/rules"
)

type log struct {
	Schema  string `json:"$schema"`
	Version string `json:"version"`
	Runs    []run  `json:"runs"`
}

type run struct {
	Tool    tool     `json:"tool"`
	Results []result `json:"results"`
}

type tool struct {
	Driver driver `json:"driver"`
}

type driver struct {
	Name           string `json:"name"`
	InformationURI string `json:"informationUri,omitempty"`
	Version        string `json:"version,omitempty"`
	Rules          []rule `json:"rules"`
}

type rule struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	ShortDescription text   `json:"shortDescription"`
	FullDescription  *text  `json:"fullDescription,omitempty"`
	DefaultConfig    config `json:"defaultConfiguration"`
}

type config struct {
	Level string `json:"level"`
}

type text struct {
	Text string `json:"text"`
}

type result struct {
	RuleID    string     `json:"ruleId"`
	Level     string     `json:"level"`
	Message   text       `json:"message"`
	Locations []location `json:"locations,omitempty"`
	// Fingerprints let code scanning follow a finding across runs.
	Fingerprints map[string]string `json:"partialFingerprints,omitempty"`
}

type location struct {
	Physical physical `json:"physicalLocation"`
}

type physical struct {
	Artifact artifact `json:"artifactLocation"`
	Region   *region  `json:"region,omitempty"`
}

type artifact struct {
	URI string `json:"uri"`
}

type region struct {
	StartLine int `json:"startLine"`
	EndLine   int `json:"endLine,omitempty"`
}

// Options adjust the log.
type Options struct {
	// Root is where the Terraform directory sits in the repository, such
	// as infra/prod, so locations resolve from the repository's top.
	Root string
	// Version is interragate's version.
	Version string
}

// Render writes the report's findings as a SARIF log.
func Render(w io.Writer, r *model.Report, opts Options) error {
	byAddr := map[string]*model.Change{}
	// Code scanning needs a place for every result. One about a resource
	// with no block left in the configuration goes where its block was
	// removed, or else at the top of the configuration's first file.
	fallback := ""
	for _, ch := range r.Changes {
		byAddr[ch.Address] = ch
		if fallback == "" && ch.Location != nil {
			fallback = ch.Location.File
		}
	}
	seen := map[string]bool{}
	var rs []rule
	var results []result
	for _, f := range r.Findings {
		if !seen[f.Rule] {
			seen[f.Rule] = true
			rs = append(rs, ruleFor(f))
		}
		res := result{RuleID: f.Rule, Level: level(f.Severity), Message: text{plain(f.Message)},
			Fingerprints: map[string]string{"interragate/v1": f.ID}}
		if f.Remediation != "" {
			res.Message.Text += " Fix: " + plain(f.Remediation)
		}
		for _, a := range f.Addresses {
			ch := byAddr[a]
			if ch == nil || ch.Location == nil {
				continue
			}
			loc := location{Physical: physical{Artifact: artifact{URI: path.Join(opts.Root, ch.Location.File)}, Region: &region{StartLine: ch.Location.Line}}}
			if ch.Location.EndLine > ch.Location.Line {
				loc.Physical.Region.EndLine = ch.Location.EndLine
			}
			res.Locations = append(res.Locations, loc)
			break // one place to show it; the message names the rest
		}
		if len(res.Locations) == 0 {
			if loc, ok := removedAt(f, byAddr, opts.Root); ok {
				res.Locations = append(res.Locations, loc)
			} else if fallback != "" {
				res.Locations = append(res.Locations, location{Physical: physical{Artifact: artifact{URI: path.Join(opts.Root, fallback)}, Region: &region{StartLine: 1}}})
			}
		}
		results = append(results, res)
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
	if results == nil {
		results = []result{}
	}
	if rs == nil {
		rs = []rule{}
	}
	out := log{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs: []run{{
			Tool:    tool{Driver: driver{Name: "interragate", InformationURI: "https://github.com/Jeecabs/interragate", Version: opts.Version, Rules: rs}},
			Results: results,
		}},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func ruleFor(f *model.Finding) rule {
	r := rule{ID: f.Rule, ShortDescription: text{f.Title}, DefaultConfig: config{level(f.Severity)}}
	if def := rules.Lookup(f.Rule); def != nil {
		r.Name = def.Title
		r.ShortDescription = text{def.Title}
		r.FullDescription = &text{def.Doc}
		r.DefaultConfig = config{level(def.Severity)}
	}
	return r
}

// level maps a severity to SARIF's three levels.
func level(s model.Severity) string {
	switch {
	case s >= model.High:
		return "error"
	case s >= model.Medium:
		return "warning"
	}
	return "note"
}

// plain drops the backticks interragate puts around addresses.
func plain(s string) string { return strings.ReplaceAll(s, "`", "") }

// removedAt is the file a finding's resource had its block removed from,
// when git shows the edit that removed it. The lines it had are numbered as
// in the old file and say nothing about the new one, so the location is
// the file itself, at its first line.
func removedAt(f *model.Finding, byAddr map[string]*model.Change, root string) (location, bool) {
	for _, a := range f.Addresses {
		ch := byAddr[a]
		if ch == nil || ch.Edit == nil || ch.Edit.File == "" {
			continue
		}
		return location{Physical: physical{Artifact: artifact{URI: path.Join(root, ch.Edit.File)}, Region: &region{StartLine: 1}}}, true
	}
	return location{}, false
}
