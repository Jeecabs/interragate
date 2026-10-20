// Package gitdiff finds how a Terraform configuration differs from a git
// base: the commits on a branch since it left the default branch, and the
// work not yet committed. Reading git's output is kept apart from running
// git, so the parsing can be tested on canned diffs.
package gitdiff

import (
	"strconv"
	"strings"
)

// Diff is how the configuration in a directory differs from a base commit.
type Diff struct {
	// Base names what was compared with as the user would: "origin/main"
	// for where the branch left it, "HEAD" for uncommitted work only, or
	// the ref given with --base.
	Base   string
	Top    string // the top of the repository, absolute
	Prefix string // the Terraform directory, relative to Top; "." at the top
	Commit string // the commit compared with, abbreviated
	Branch string // the current branch; empty when HEAD is detached
	Dirty  bool   // uncommitted changes to the configuration are included
	Files  []*File
	// Providers are the providers whose locked version differs from the
	// base's dependency lock file.
	Providers []Provider
}

// Provider is a provider whose locked version moved; From or To is empty
// when the base or the work tree does not lock it.
type Provider struct{ Source, From, To string }

// File is one configuration file that differs from the base.
type File struct {
	// Path is where the file is now, or where it was if it was deleted.
	// Parse leaves it relative to the top of the repository; Load makes it
	// relative to the Terraform directory, as source locations are.
	Path string
	// OldPath is where the file was in the base, when it was renamed.
	OldPath        string
	Added, Deleted bool
	Hunks          []*Hunk
	// Old and New are the file's lines in the base and now, when known
	// (see Rebuild). The hunks only hold the lines that changed.
	Old, New []string
}

// Hunk is one run of changed lines, as git diff --unified=0 prints it.
// When a side has no lines, its start is the line the change comes after.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Lines              []Line // the removed lines, then the added ones
}

// Line is one changed line. Num is its line number in the base for "-" and
// in the file now for "+".
type Line struct {
	Op   string
	Num  int
	Text string
}

// Parse reads the output of git diff --unified=0 --src-prefix=a/
// --dst-prefix=b/. A file with no lines to show, such as a change of mode or
// a binary file, is left out unless it was renamed.
func Parse(out []byte) []*File {
	var files []*File
	var f *File
	var h *Hunk
	oldLeft, newLeft, oldNum, newNum := 0, 0, 0, 0
	for _, line := range strings.Split(string(out), "\n") {
		if h != nil && (oldLeft > 0 || newLeft > 0) {
			// The counts in the hunk header say which lines belong to it, so
			// a removed line that starts with "--" is not taken for a header.
			switch {
			case strings.HasPrefix(line, "-") && oldLeft > 0:
				h.Lines = append(h.Lines, Line{Op: "-", Num: oldNum, Text: text(line[1:])})
				oldNum++
				oldLeft--
				continue
			case strings.HasPrefix(line, "+") && newLeft > 0:
				h.Lines = append(h.Lines, Line{Op: "+", Num: newNum, Text: text(line[1:])})
				newNum++
				newLeft--
				continue
			case strings.HasPrefix(line, " ") && oldLeft > 0 && newLeft > 0:
				// Context, should diff.interHunkContext have asked for it.
				oldNum++
				newNum++
				oldLeft--
				newLeft--
				continue
			case strings.HasPrefix(line, `\`):
				continue
			}
			oldLeft, newLeft = 0, 0
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			f, h = &File{}, nil
			files = append(files, f)
		case f == nil, strings.HasPrefix(line, `\`):
		case strings.HasPrefix(line, "new file mode "):
			f.Added = true
		case strings.HasPrefix(line, "deleted file mode "):
			f.Deleted = true
		case strings.HasPrefix(line, "rename from "):
			f.OldPath = unquote(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			f.Path = unquote(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "--- ") && h == nil:
			if p, ok := name(line[4:], "a/"); ok {
				f.OldPath = p
			}
		case strings.HasPrefix(line, "+++ ") && h == nil:
			if p, ok := name(line[4:], "b/"); ok {
				f.Path = p
			}
		case strings.HasPrefix(line, "@@ "):
			nh, ok := parseHunk(line)
			if !ok {
				continue
			}
			f.Hunks = append(f.Hunks, nh)
			h = nh
			oldLeft, newLeft = nh.OldLines, nh.NewLines
			oldNum, newNum = nh.OldStart, nh.NewStart
		}
	}
	kept := files[:0]
	for _, f := range files {
		if f.Path == "" {
			f.Path = f.OldPath
		}
		if f.OldPath == f.Path {
			f.OldPath = ""
		}
		if f.Path != "" && (len(f.Hunks) > 0 || f.OldPath != "") {
			kept = append(kept, f)
		}
	}
	return kept
}

// parseHunk reads a header such as "@@ -12,3 +12 @@ resource ...".
func parseHunk(line string) (*Hunk, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 || fields[3] != "@@" {
		return nil, false
	}
	oldStart, oldLines, ok1 := parseRange(fields[1], "-")
	newStart, newLines, ok2 := parseRange(fields[2], "+")
	if !ok1 || !ok2 {
		return nil, false
	}
	return &Hunk{OldStart: oldStart, OldLines: oldLines, NewStart: newStart, NewLines: newLines}, true
}

// parseRange reads "-12,3" or "+12"; a missing count means one line.
func parseRange(s, sign string) (start, n int, ok bool) {
	s, ok = strings.CutPrefix(s, sign)
	if !ok {
		return 0, 0, false
	}
	first, count, hasCount := strings.Cut(s, ",")
	start, err := strconv.Atoi(first)
	if err != nil {
		return 0, 0, false
	}
	n = 1
	if hasCount {
		if n, err = strconv.Atoi(count); err != nil {
			return 0, 0, false
		}
	}
	return start, n, start >= 0 && n >= 0
}

// name reads the path from a "---" or "+++" line, which is /dev/null for a
// file that is added or deleted. Git ends a name that has a space with a
// tab, and quotes one with unusual characters.
func name(s, prefix string) (string, bool) {
	s = strings.TrimSuffix(s, "\t")
	if s == "/dev/null" {
		return "", false
	}
	return strings.CutPrefix(unquote(s), prefix)
}

func unquote(s string) string {
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

// text drops the carriage return a file with Windows line endings leaves.
func text(s string) string { return strings.TrimSuffix(s, "\r") }

func splitLines(data []byte) []string {
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = text(l)
	}
	return lines
}

// Untracked describes a file git does not know about yet: wholly added.
func Untracked(path string, content []byte) *File {
	lines := splitLines(content)
	f := &File{Path: path, Added: true, New: lines, Old: []string{}}
	if len(lines) > 0 {
		h := &Hunk{NewStart: 1, NewLines: len(lines)}
		for i, l := range lines {
			h.Lines = append(h.Lines, Line{Op: "+", Num: i + 1, Text: l})
		}
		f.Hunks = []*Hunk{h}
	}
	return f
}

// Rebuild fills in Old and New from the file's content now (nothing, for a
// file that was deleted): the hunks run backwards turn one into the other.
// It reports false, and leaves both unset, when the hunks do not fit the
// content, as when the file was saved again after git diff ran.
func (f *File) Rebuild(now []byte) bool {
	f.Old, f.New = nil, nil
	var cur []string
	if !f.Deleted {
		cur = splitLines(now)
	}
	old := []string{}
	copied := 0
	for _, h := range f.Hunks {
		// Lines before the hunk read the same in both versions.
		before := h.NewStart
		if h.NewLines > 0 {
			before--
		}
		if before < copied || before > len(cur) {
			return false
		}
		old = append(old, cur[copied:before]...)
		copied = before
		want := h.OldStart
		if h.OldLines > 0 {
			want--
		}
		if len(old) != want {
			return false
		}
		removed, added := 0, 0
		for _, l := range h.Lines {
			switch l.Op {
			case "-":
				old = append(old, l.Text)
				removed++
			case "+":
				if l.Num < 1 || l.Num > len(cur) || cur[l.Num-1] != l.Text {
					return false
				}
				added++
			}
		}
		if removed != h.OldLines || added != h.NewLines {
			return false
		}
		copied += h.NewLines
	}
	if copied > len(cur) {
		return false
	}
	f.Old, f.New = append(old, cur[copied:]...), cur
	if f.New == nil {
		f.New = []string{}
	}
	return true
}

// OldLine maps a line of the file now that git left unchanged to its line
// number in the base. It reports false for a line the hunks added.
func (f *File) OldLine(n int) (int, bool) {
	if n < 1 {
		return 0, false
	}
	shift := 0
	for _, h := range f.Hunks {
		last := h.NewStart
		if h.NewLines > 0 {
			last = h.NewStart + h.NewLines - 1
			if n >= h.NewStart && n <= last {
				return 0, false
			}
		}
		if last < n {
			shift += h.OldLines - h.NewLines
		}
	}
	return n + shift, true
}
