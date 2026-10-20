package owners

import (
	"strings"
	"testing"
)

// The examples from GitHub's documentation for CODEOWNERS.
func TestOwners(t *testing.T) {
	f := Parse([]byte(`# Default owners
*       @global-owner1 @global-owner2

*.js    @js-owner #This is an inline comment.
*.go docs@example.com
*.txt @octo-org/octocats
/build/logs/ @doctocat
docs/*  docs@example.com
apps/ @octocat
/docs/ @doctocat
/scripts/ @doctocat @octocat
**/logs @octocat
/apps/ @octocat
/apps/github
[Section]
/infra/**/*.tf @platform
`))
	for path, want := range map[string]string{
		"README.md":                         "@global-owner1 @global-owner2",
		"src/app.js":                        "@js-owner",
		"main.go":                           "docs@example.com",
		"build/logs/today.log":              "@octocat", // **/logs comes later
		"docs/getting-started.md":           "@doctocat",
		"docs/build-app/troubleshooting.md": "@doctocat",
		"deep/apps/x.md":                    "@octocat",
		"scripts/deploy.sh":                 "@doctocat @octocat",
		"some/where/logs/x":                 "@octocat",
		"apps/github/x.rb":                  "",
		"apps/other/x.rb":                   "@octocat",
		"infra/prod/main.tf":                "@platform",
		"infra/main.tf":                     "@platform",
		"infra/prod/notes.md":               "@global-owner1 @global-owner2",
	} {
		if got := strings.Join(f.Owners(path), " "); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}

// docs/* covers the files directly in docs, not those further down.
func TestWildcardLastSegment(t *testing.T) {
	f := Parse([]byte("docs/* @docs\n"))
	if got := f.Owners("docs/a.md"); len(got) != 1 {
		t.Errorf("docs/a.md: %v", got)
	}
	if got := f.Owners("docs/sub/a.md"); got != nil {
		t.Errorf("docs/sub/a.md: %v", got)
	}
}
