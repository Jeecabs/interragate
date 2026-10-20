package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Jeecabs/interragate/internal/delta"
	"github.com/Jeecabs/interragate/internal/render/text"
)

// diffCmd compares two plans of the same configuration, such as before and
// after an edit, or the plan a pull request was reviewed with and the one
// about to be applied, and says what differs. With --fail-on it exits 2
// when the later plan brings a new finding at or above that severity.
func diffCmd(ctx context.Context, o *options, stdout io.Writer) error {
	if len(o.args) != 2 {
		return errors.New("interragate diff needs two plans: the earlier one, then the later one")
	}
	before, err := loadReport(ctx, o, o.args[0])
	if err != nil {
		return err
	}
	after, err := loadReport(ctx, o, o.args[1])
	if err != nil {
		return err
	}
	d := delta.Compare(before, after)
	switch o.format {
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(d)
	case "markdown":
		var b strings.Builder
		fmt.Fprintf(&b, "### What changed between `%s` and `%s`\n\n", o.args[0], o.args[1])
		for _, s := range d.Sentences {
			fmt.Fprintf(&b, "- %s\n", s.Text)
		}
		_, err = io.WriteString(stdout, b.String())
	case "text":
		color := !o.noColor && os.Getenv("NO_COLOR") == "" && isTerminal(stdout)
		lead := fmt.Sprintf("%s → %s", o.args[0], o.args[1])
		err = text.RenderDelta(stdout, after, d, lead, text.Options{Color: color, Width: termWidth(stdout)})
	default:
		return fmt.Errorf("interragate diff writes text, markdown or json, not %s", o.format)
	}
	if err != nil {
		return err
	}
	if o.failOn != "" {
		threshold, err := failThreshold(o)
		if err != nil {
			return err
		}
		for _, f := range d.Found {
			if f.Severity >= threshold {
				return exitError(2)
			}
		}
	}
	return nil
}
