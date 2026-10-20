package analysis

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/rules"
	"github.com/Jeecabs/interragate/internal/scan"
)

// scanned turns what scanners reported into findings on the changes they
// concern: resources the plan creates or replaces, whose every issue is new,
// and resources whose edited lines an issue points at. An issue on what
// the plan leaves as it was is counted, not shown.
func (a *analyzer) scanned(r *model.Report, results []ScanResult) (map[string][]*model.Finding, []model.Scan) {
	if len(results) == 0 {
		return nil, nil
	}
	out := map[string][]*model.Finding{}
	var scans []model.Scan
	for _, res := range results {
		s := model.Scan{Tool: res.Tool, Issues: len(res.Issues)}
		if res.Err != nil {
			s.Error = res.Err.Error()
			scans = append(scans, s)
			continue
		}
		groups := map[string]*model.Finding{}
		first := map[string]scan.Issue{} // the issue each finding was made from
		var order []string
		for _, is := range res.Issues {
			hit := false
			for _, b := range a.issueBlocks(is) {
				for _, ch := range r.Changes {
					module, key := blockKey(ch)
					if module != b.module || key != b.key {
						continue
					}
					why, ok := scanReason(ch, is)
					if !ok {
						continue
					}
					hit = true
					id := is.ID + "|" + b.module + "|" + b.key
					f := groups[id]
					if f == nil {
						f = &model.Finding{Title: is.Title, Severity: severityOf(is.Severity), Short: "flagged by " + is.Tool, Remediation: scanFix(is)}
						f.Message = why
						groups[id] = f
						first[id] = is
						order = append(order, id)
					}
					if !contains(f.Addresses, ch.Address) {
						f.Addresses = append(f.Addresses, ch.Address)
					}
				}
			}
			if hit {
				s.Shown++
			}
		}
		for _, id := range order {
			f := groups[id]
			f.Message = scanMessage(f, res.Tool, first[id])
			out[res.Tool] = append(out[res.Tool], f)
		}
		scans = append(scans, s)
	}
	return out, scans
}

// scanReason says why an issue concerns a change, or false when it does
// not: the change creates or replaces the object, or edits the lines the
// issue points at.
func scanReason(ch *model.Change, is scan.Issue) (string, bool) {
	switch {
	case ch.Action == model.Create && !ch.Importing:
		return "new", true
	case ch.Action == model.Replace:
		return "replaced", true
	case ch.Edit != nil && ch.Edit.File == is.File:
		for _, l := range ch.Edit.Lines {
			if l.Op == "+" && l.Line >= is.Start && l.Line <= max(is.End, is.Start) {
				return "edited", true
			}
		}
	}
	return "", false
}

// scanMessage words a scanner's finding from why it concerns the change.
func scanMessage(f *model.Finding, tool string, is scan.Issue) string {
	codes := make([]string, len(f.Addresses))
	for i, a := range f.Addresses {
		codes[i] = rules.Code(a)
	}
	what := strings.TrimSuffix(strings.TrimSpace(is.Message), ".")
	if what == "" {
		what = strings.TrimSuffix(is.Title, ".")
	}
	if len(what) > 1 && what[1] >= 'a' && what[1] <= 'z' {
		what = strings.ToLower(what[:1]) + what[1:]
	}
	subject := rules.List(codes, 3)
	switch f.Message {
	case "new":
		return fmt.Sprintf("%s %s new, and %s flags it: %s (%s).", subject, isAre(len(codes)), tool, what, is.ID)
	case "replaced":
		return fmt.Sprintf("%s %s replaced, and %s flags the new one: %s (%s).", subject, isAre(len(codes)), tool, what, is.ID)
	}
	return fmt.Sprintf("%s flags the lines you edited in %s: %s (%s).", tool, subject, what, is.ID)
}

func scanFix(is scan.Issue) string {
	fix := strings.TrimSpace(is.Fix)
	if fix != "" && !strings.HasSuffix(fix, ".") {
		fix += "."
	}
	if is.URL != "" {
		fix = strings.TrimSpace(fix + " Details: " + is.URL)
	}
	return fix
}

func severityOf(s string) model.Severity {
	sev, err := model.ParseSeverity(s)
	if err != nil {
		return model.Low
	}
	return sev
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

type blockRef struct{ module, key string }

// issueBlocks finds the blocks an issue is in: by the resource the scanner
// names, or the block around its first line, in every module whose source
// is the issue's directory.
func (a *analyzer) issueBlocks(is scan.Issue) []blockRef {
	if a.src == nil || is.File == "" {
		return nil
	}
	dir := path.Dir(is.File)
	var out []blockRef
	modules := make([]string, 0, len(a.src.Modules))
	for p := range a.src.Modules {
		modules = append(modules, p)
	}
	sort.Strings(modules)
	for _, p := range modules {
		m := a.src.Modules[p]
		if slashDir(m.Dir) != dir {
			continue
		}
		key := ""
		if b := m.Blocks[unkeyed(is.Resource)]; b != nil && b.File == is.File {
			key = unkeyed(is.Resource)
		} else {
			for k, b := range m.Blocks {
				if b.File == is.File && !strings.HasPrefix(k, "module.") && b.Line <= is.Start && is.Start <= max(b.EndLine, b.Line) {
					key = k
					break
				}
			}
		}
		if key != "" {
			out = append(out, blockRef{p, key})
		}
	}
	return out
}
