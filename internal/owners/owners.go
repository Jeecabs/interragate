// Package owners reads a repository's CODEOWNERS file and says who owns a
// path, as GitHub and GitLab do: the last pattern that matches decides.
package owners

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// File is a parsed CODEOWNERS file.
type File struct {
	// Path is where it was found, relative to the top of the repository.
	Path  string
	rules []rule
}

type rule struct {
	re     *regexp.Regexp
	owners []string
}

// places are where GitHub looks for the file, in its order, then GitLab's
// own.
var places = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS", ".gitlab/CODEOWNERS"}

// Find reads the CODEOWNERS file of the repository whose top is top, or
// returns nil when it has none.
func Find(top string) *File {
	for _, p := range places {
		data, err := os.ReadFile(filepath.Join(top, filepath.FromSlash(p)))
		if err == nil {
			f := Parse(data)
			f.Path = p
			return f
		}
	}
	return nil
}

// Parse reads CODEOWNERS lines: a pattern, then the owners, which are
// @users, @org/teams or email addresses. A pattern with no owners leaves
// what it matches unowned. GitLab's [Section] headings are skipped.
func Parse(data []byte) *File {
	f := &File{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "^[") {
			continue
		}
		fields := strings.Fields(line)
		var owners []string
		for _, o := range fields[1:] {
			if strings.HasPrefix(o, "#") {
				break // a comment after the owners
			}
			owners = append(owners, o)
		}
		re, err := regexp.Compile(pattern(fields[0]))
		if err != nil {
			continue
		}
		f.rules = append(f.rules, rule{re: re, owners: owners})
	}
	return f
}

// Owners returns who owns a path, slash-separated and relative to the top
// of the repository; nil when nobody does.
func (f *File) Owners(path string) []string {
	if f == nil {
		return nil
	}
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	var owners []string
	for _, r := range f.rules {
		if r.re.MatchString(path) {
			owners = r.owners
		}
	}
	return owners
}

// pattern turns a CODEOWNERS pattern into a regular expression. As in
// .gitignore, a slash at the start or in the middle ties the pattern to
// the top of the repository, and otherwise it matches at any depth; a
// pattern also covers everything below a directory it names. As GitHub
// documents, a last segment with a wildcard (docs/*) covers only the files
// directly inside.
func pattern(p string) string {
	anchored := strings.HasPrefix(p, "/") || strings.Contains(strings.TrimSuffix(p, "/"), "/")
	p = strings.TrimPrefix(p, "/")
	dir := strings.HasSuffix(p, "/")
	p = strings.TrimSuffix(p, "/")
	var b strings.Builder
	if anchored {
		b.WriteString("^")
	} else {
		b.WriteString("^(?:.*/)?")
	}
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '*' && i+1 < len(p) && p[i+1] == '*':
			// "**/" matches any number of directories, "**" anything.
			if i+2 < len(p) && p[i+2] == '/' {
				b.WriteString("(?:.*/)?")
				i += 2
			} else {
				b.WriteString(".*")
				i++
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	last := p[strings.LastIndex(p, "/")+1:]
	switch {
	case p == "*" || p == "**":
		b.WriteString("(?:/.*)?$")
	case dir:
		b.WriteString("/.*$")
	case strings.ContainsAny(last, "*?") && anchored:
		b.WriteString("$")
	default:
		b.WriteString("(?:/.*)?$")
	}
	return b.String()
}
