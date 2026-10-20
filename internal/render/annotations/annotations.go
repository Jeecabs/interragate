// Package annotations renders findings as GitHub Actions workflow commands,
// which GitHub shows inline on the pull request diff at the resource's line.
package annotations

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
)

// Render writes one annotation per finding. prefix is the path from the
// repository root to the Terraform directory, so file paths resolve.
func Render(w io.Writer, r *model.Report, prefix string) error {
	locations := map[string]*model.Location{}
	for _, c := range r.Changes {
		if c.Location != nil {
			locations[c.Address] = c.Location
		}
	}
	for _, f := range r.Findings {
		level := "notice"
		switch {
		case f.Severity >= model.High:
			level = "error"
		case f.Severity == model.Medium:
			level = "warning"
		}
		var params []string
		for _, addr := range f.Addresses {
			if loc := locations[addr]; loc != nil {
				params = append(params, "file="+escapeProperty(filepath.ToSlash(filepath.Join(prefix, loc.File))), fmt.Sprintf("line=%d", loc.Line))
				if loc.EndLine > loc.Line {
					params = append(params, fmt.Sprintf("endLine=%d", loc.EndLine))
				}
				break
			}
		}
		params = append(params, "title="+escapeProperty(fmt.Sprintf("interragate %s: %s", f.Severity, f.Title)))
		msg := strings.ReplaceAll(f.Message, "`", "")
		if f.Remediation != "" {
			msg += "\n\nFix: " + strings.ReplaceAll(f.Remediation, "`", "")
		}
		if _, err := fmt.Fprintf(w, "::%s %s::%s\n", level, strings.Join(params, ","), escapeData(msg)); err != nil {
			return err
		}
	}
	return nil
}

// Escaping follows the workflow command rules.
func escapeData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func escapeProperty(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}
