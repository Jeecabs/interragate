package tfexec

import (
	"fmt"
	"regexp"
	"strings"
)

// hint is advice for an error Terraform prints often, when what to do
// about it is not in the error itself.
type hint struct {
	match  *regexp.Regexp
	advice func(m []string, out, tool string) string
}

var (
	lockID      = regexp.MustCompile(`(?m)^\s*ID:\s*(\S+)`)
	lockWho     = regexp.MustCompile(`(?m)^\s*Who:\s*(.+?)\s*$`)
	lockCreated = regexp.MustCompile(`(?m)^\s*Created:\s*(\S+ \S+)`)
	required    = regexp.MustCompile(`required_version\s*=\s*"([^"]+)"`)
)

var hints = []hint{
	{regexp.MustCompile(`Error acquiring the state lock`), func(_ []string, out, tool string) string {
		who, since, id := "someone", "", "LOCK_ID"
		if m := lockWho.FindStringSubmatch(out); m != nil && m[1] != "" {
			who = m[1]
		}
		if m := lockCreated.FindStringSubmatch(out); m != nil {
			since = ", since " + m[1]
		}
		if m := lockID.FindStringSubmatch(out); m != nil {
			id = m[1]
		}
		return fmt.Sprintf("Another run holds the state lock (%s%s). Wait for it to finish; if it has died, `%s force-unlock %s` releases the lock.", who, since, tool, id)
	}},
	{regexp.MustCompile(`(?i)no valid credential sources|ExpiredToken|security token included in the request is (expired|invalid)|SSO session has expired|refresh cached SSO token failed|InvalidClientTokenId`), func(_ []string, _, _ string) string {
		return "Your AWS credentials are missing or have expired. With SSO, `aws sso login` (with --profile if you use one) renews them; `aws sts get-caller-identity` shows who you are signed in as."
	}},
	{regexp.MustCompile(`could not find default credentials|invalid_rapt|reauth related error`), func(_ []string, _, _ string) string {
		return "Your Google Cloud credentials are missing or have expired: `gcloud auth application-default login` renews them."
	}},
	{regexp.MustCompile(`Please run 'az login'|az login`), func(_ []string, _, _ string) string {
		return "Your Azure CLI session is missing or has expired: `az login` renews it."
	}},
	{regexp.MustCompile(`Unsupported Terraform Core version|Unsupported OpenTofu version`), func(_ []string, out, tool string) string {
		want := "another version"
		if m := required.FindStringSubmatch(out); m != nil {
			want = "`" + m[1] + "`"
		}
		return fmt.Sprintf("The configuration's required_version asks for %s. Switch with your version manager (with mise, `mise use %s@VERSION`), or point interragate at another binary with --bin.", want, tool)
	}},
	{regexp.MustCompile(`Failed to query available provider packages|could not connect to registry|lookup registry\.(terraform|opentofu)\.io[^\n]*no such host`), func(_ []string, _, _ string) string {
		return "interragate could not reach the provider registry: check your connection. A provider mirror in the CLI configuration (provider_installation) lets init work offline."
	}},
	{regexp.MustCompile(`Kubernetes cluster unreachable|dial tcp (\[::1\]|127\.0\.0\.1):80: connect: connection refused`), func(_ []string, _, _ string) string {
		return "The Kubernetes or Helm provider cannot reach a cluster: check the provider's host or config_path, and the kubectl context (`kubectl config current-context`)."
	}},
}

// Hint returns advice for a failed command's output, or "" when interragate
// has none. tool is the binary's name, as the user would type it.
func Hint(output, tool string) string {
	for _, h := range hints {
		if m := h.match.FindStringSubmatch(output); m != nil {
			return h.advice(m, output, tool)
		}
	}
	return ""
}

// Hint is advice for the error, or "".
func (e *CommandError) Hint() string {
	tool, _, _ := strings.Cut(e.Command, " ")
	return Hint(e.Output, tool)
}
