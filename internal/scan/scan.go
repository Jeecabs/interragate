// Package scan reads what configuration scanners (trivy, checkov and
// tflint) report, so interragate can show the issues a plan touches: those
// on what it creates, replaces or edits. An issue a resource had before the
// change is not news, as with interragate's own rules.
package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strings"
	"time"
)

// Tools are the scanners interragate can run, in the order it runs them.
var Tools = []string{"trivy", "checkov", "tflint"}

// Issue is one problem a scanner reports in the configuration.
type Issue struct {
	Tool     string // trivy, checkov or tflint
	ID       string // the scanner's check, such as AWS-0017 or CKV_AWS_18
	Severity string // critical, high, medium, low or info
	Title    string
	Message  string
	Fix      string // how to resolve it, when the scanner says
	URL      string
	// File is where the issue is, relative to the scanned directory, with
	// forward slashes. Start and End are its lines in File.
	File       string
	Start, End int
	// Resource names the block, such as aws_s3_bucket.logs, when the
	// scanner does; File and Start find it otherwise.
	Resource string
}

// ErrNotInstalled says a scanner's binary is not on PATH.
var ErrNotInstalled = errors.New("not installed")

// Run runs a scanner on dir and reads what it reports. It returns an error
// wrapping ErrNotInstalled when the scanner is not on PATH.
func Run(ctx context.Context, tool, dir string) ([]Issue, error) {
	var args []string
	switch tool {
	case "trivy":
		args = []string{"config", "--format", "json", "--quiet", "."}
	case "checkov":
		args = []string{"-d", ".", "-o", "json", "--quiet", "--compact", "--framework", "terraform"}
	case "tflint":
		args = []string{"--format", "json"}
	default:
		return nil, fmt.Errorf("unknown scanner %q; use %s", tool, strings.Join(Tools, ", "))
	}
	if _, err := exec.LookPath(tool); err != nil {
		return nil, fmt.Errorf("%s: %w", tool, ErrNotInstalled)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir = dir
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	runErr := cmd.Run()
	// checkov and tflint exit non-zero when they find something; what they
	// printed decides.
	issues, err := Parse(tool, out.Bytes())
	if err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("%s: %v: %s", tool, runErr, strings.TrimSpace(errOut.String()))
		}
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	return issues, nil
}

// Parse reads a scanner's JSON report.
func Parse(tool string, data []byte) ([]Issue, error) {
	switch tool {
	case "trivy":
		return parseTrivy(data)
	case "checkov":
		return parseCheckov(data)
	case "tflint":
		return parseTFLint(data)
	}
	return nil, fmt.Errorf("unknown scanner %q", tool)
}

func parseTrivy(data []byte) ([]Issue, error) {
	var doc struct {
		Results []struct {
			Target            string
			Class             string
			Misconfigurations []struct {
				ID, AVDID, Title, Message, Resolution, Severity, PrimaryURL, Status string
				CauseMetadata                                                       struct {
					Resource           string
					StartLine, EndLine int
					Occurrences        []struct {
						Resource, Filename string
						Location           struct{ StartLine, EndLine int }
					}
				}
			}
		}
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("not trivy's JSON report: %w", err)
	}
	var out []Issue
	for _, r := range doc.Results {
		for _, m := range r.Misconfigurations {
			if m.Status != "" && m.Status != "FAIL" {
				continue
			}
			cm := m.CauseMetadata
			is := Issue{Tool: "trivy", ID: m.ID, Severity: severity(m.Severity), Title: m.Title, Message: m.Message, Fix: m.Resolution, URL: m.PrimaryURL,
				File: clean(r.Target), Start: cm.StartLine, End: cm.EndLine, Resource: cm.Resource}
			if is.ID == "" {
				is.ID = m.AVDID
			}
			// Inside a module, trivy names the module call; the occurrence in
			// the scanned file names the resource.
			for _, o := range cm.Occurrences {
				if clean(o.Filename) == is.File && !strings.HasPrefix(o.Resource, "module.") {
					is.Resource = o.Resource
					break
				}
			}
			out = append(out, is)
		}
	}
	return out, nil
}

func parseCheckov(data []byte) ([]Issue, error) {
	type report struct {
		Results struct {
			FailedChecks []struct {
				CheckID       string `json:"check_id"`
				CheckName     string `json:"check_name"`
				FilePath      string `json:"file_path"`
				FileLineRange []int  `json:"file_line_range"`
				Resource      string `json:"resource"`
				Severity      string `json:"severity"`
				Guideline     string `json:"guideline"`
			} `json:"failed_checks"`
		} `json:"results"`
	}
	// One report per framework comes as a list; one alone as an object.
	var reports []report
	if err := json.Unmarshal(data, &reports); err != nil {
		var one report
		if err := json.Unmarshal(data, &one); err != nil {
			return nil, fmt.Errorf("not checkov's JSON report: %w", err)
		}
		reports = []report{one}
	}
	var out []Issue
	for _, rep := range reports {
		for _, c := range rep.Results.FailedChecks {
			is := Issue{Tool: "checkov", ID: c.CheckID, Severity: severity(c.Severity), Title: c.CheckName, Message: c.CheckName, URL: c.Guideline,
				File: clean(c.FilePath), Resource: c.Resource}
			if c.Severity == "" {
				is.Severity = "medium" // checkov leaves severity out without its platform
			}
			if len(c.FileLineRange) == 2 {
				is.Start, is.End = c.FileLineRange[0], c.FileLineRange[1]
			}
			out = append(out, is)
		}
	}
	return out, nil
}

func parseTFLint(data []byte) ([]Issue, error) {
	var doc struct {
		Issues []struct {
			Rule struct {
				Name, Severity, Link string
			}
			Message string
			Range   struct {
				Filename string
				Start    struct{ Line int }
				End      struct{ Line int }
			}
		}
		// Errors are what stopped tflint checking, such as a plugin that
		// was never installed; its issues are then not the whole story.
		Errors []struct{ Message string }
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("not tflint's JSON report: %w", err)
	}
	if len(doc.Errors) > 0 {
		msg := strings.TrimSpace(doc.Errors[0].Message)
		if n := len(doc.Errors) - 1; n > 0 {
			msg += fmt.Sprintf(" (and %d more errors)", n)
		}
		return nil, fmt.Errorf("tflint did not finish: %s", msg)
	}
	var out []Issue
	for _, i := range doc.Issues {
		sev := map[string]string{"error": "medium", "warning": "low", "notice": "info"}[strings.ToLower(i.Rule.Severity)]
		if sev == "" {
			sev = "low"
		}
		out = append(out, Issue{Tool: "tflint", ID: i.Rule.Name, Severity: sev, Title: i.Message, Message: i.Message, URL: i.Rule.Link,
			File: clean(i.Range.Filename), Start: i.Range.Start.Line, End: i.Range.End.Line})
	}
	return out, nil
}

// severity maps a scanner's severity to interragate's words.
func severity(s string) string {
	switch strings.ToLower(s) {
	case "critical":
		return "critical"
	case "high":
		return "high"
	case "medium", "moderate":
		return "medium"
	case "info", "informational":
		return "info"
	}
	return "low"
}

// clean makes a scanner's file name relative and slash-separated.
func clean(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	return p
}
