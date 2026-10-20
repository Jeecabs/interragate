package tfexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Event is one step of a plan's progress, from Terraform's machine-readable
// output.
type Event struct {
	// Type is Terraform's event type: refresh_start, refresh_complete,
	// planned_change, change_summary, diagnostic and so on.
	Type string
	// Address is the resource the event is about, when there is one.
	Address string
}

// uiEvent is the part of a -json line interragate reads.
type uiEvent struct {
	Type    string `json:"type"`
	Message string `json:"@message"`
	Hook    struct {
		Resource struct {
			Addr string `json:"addr"`
		} `json:"resource"`
	} `json:"hook"`
	Change struct {
		Resource struct {
			Addr string `json:"addr"`
		} `json:"resource"`
	} `json:"change"`
	Diagnostic *diagnostic `json:"diagnostic"`
}

type diagnostic struct {
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail"`
	Range    *struct {
		Filename string `json:"filename"`
		Start    struct {
			Line int `json:"line"`
		} `json:"start"`
	} `json:"range"`
	Snippet *struct {
		Context   string `json:"context"`
		Code      string `json:"code"`
		StartLine int    `json:"start_line"`
	} `json:"snippet"`
}

// PlanWithProgress runs plan like Plan, but with -json, and calls progress
// for each event as it arrives so a terminal can show how far the plan has
// got. When the plan fails, the error carries Terraform's diagnostics laid
// out as Terraform prints them; if none can be read, it carries the raw
// output instead.
func (r *Runner) PlanWithProgress(ctx context.Context, planFile string, args []string, progress func(Event)) error {
	full := append([]string{"plan", "-input=false", "-json", "-out=" + planFile}, args...)
	cmd := exec.Command(r.Bin, full...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1")
	var stderr, raw bytes.Buffer
	var diags []*diagnostic
	stdout := &lines{each: func(line []byte) {
		raw.Write(line)
		raw.WriteByte('\n')
		var e uiEvent
		if json.Unmarshal(line, &e) != nil || e.Type == "" {
			return
		}
		if e.Diagnostic != nil {
			diags = append(diags, e.Diagnostic)
		}
		addr := e.Hook.Resource.Addr
		if addr == "" {
			addr = e.Change.Resource.Addr
		}
		if progress != nil {
			progress(Event{Type: e.Type, Address: addr})
		}
	}}
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	err := runCommand(ctx, cmd)
	stdout.flush()
	if err != nil {
		out := formatDiagnostics(diags)
		if out == "" {
			out = plain(raw.String() + "\n" + stderr.String())
		} else if s := plain(stderr.String()); s != "" {
			out += "\n\n" + s
		}
		return &CommandError{Err: err, Output: out, Command: r.Name() + " plan"}
	}
	return nil
}

// lines hands each line written to it to each, without the newline.
type lines struct {
	each func([]byte)
	buf  []byte
}

func (l *lines) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		l.each(l.buf[:i])
		l.buf = l.buf[i+1:]
	}
}

// flush hands on a last line that did not end in a newline.
func (l *lines) flush() {
	if len(l.buf) > 0 {
		l.each(l.buf)
		l.buf = nil
	}
}

// formatDiagnostics lays out errors and warnings as Terraform's human
// output does: the summary, where, the code, then the detail.
func formatDiagnostics(diags []*diagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		if d.Severity != "error" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "Error: %s", d.Summary)
		if d.Range != nil && d.Range.Filename != "" {
			fmt.Fprintf(&b, "\n\n  on %s line %d", d.Range.Filename, d.Range.Start.Line)
			if d.Snippet != nil && d.Snippet.Context != "" {
				fmt.Fprintf(&b, ", in %s", d.Snippet.Context)
			}
			b.WriteString(":")
			if d.Snippet != nil {
				for i, code := range strings.Split(d.Snippet.Code, "\n") {
					fmt.Fprintf(&b, "\n  %d: %s", d.Snippet.StartLine+i, code)
				}
			}
		}
		if d.Detail != "" {
			fmt.Fprintf(&b, "\n\n%s", d.Detail)
		}
	}
	return b.String()
}
