package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/kb"
)

// initCmd writes a starter .interragate.yaml: every setting explained, and a
// first policy drawn from the configuration itself, keeping the resource
// types there that hold data from being destroyed or replaced without a
// second look. It never overwrites a file that exists.
func initCmd(o *options, stdout io.Writer) error {
	if len(o.args) > 0 {
		return fmt.Errorf("interragate init takes no arguments; to set up another directory use interragate init -C %s", o.args[0])
	}
	dir := o.dir
	if dir == "" {
		dir = "."
	}
	path := filepath.Join(dir, config.FileNames[0])
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; edit it, or remove it to start again", path)
	}
	if !hasConfigFiles(dir) {
		return noConfigError(dir)
	}
	stateful := statefulTypes(dir)
	body := starterConfig(displayDir(dir), stateful)
	// What interragate writes, interragate must be able to read.
	tmp, err := os.CreateTemp("", "interragate-init-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if _, err := readConfig(tmp.Name()); err != nil {
		return fmt.Errorf("internal error: the starter settings do not load: %w", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Wrote %s.\n", path)
	if len(stateful) > 0 {
		fmt.Fprintf(stdout, "Its first policy keeps %s from being destroyed or replaced without review.\n", joinTypes(stateful))
	}
	fmt.Fprintln(stdout, "Every other setting is there, commented, to switch on. interragate rules lists what runs; interragate doctor checks the file.")
	return nil
}

var resourceBlock = regexp.MustCompile(`(?m)^\s*resource\s+"([A-Za-z0-9_]+)"\s+"`)

// statefulTypes lists the resource types the configuration declares (in
// the directory and the modules below it) that hold data.
func statefulTypes(dir string) []string {
	seen := map[string]bool{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".tf") && !strings.HasSuffix(p, ".tofu") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range resourceBlock.FindAllSubmatch(b, -1) {
			if t := string(m[1]); kb.Lookup(t).Stateful {
				seen[t] = true
			}
		}
		return nil
	})
	var out []string
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func joinTypes(types []string) string {
	q := make([]string, len(types))
	for i, t := range types {
		q[i] = t
	}
	if len(q) <= 2 {
		return strings.Join(q, " and ")
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}

func starterConfig(name string, stateful []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# interragate settings for %s. interragate uses the nearest
# .interragate.yaml, looking up to the root of the git repository. Every
# setting is optional.

# The lowest severity that makes interragate check fail (exit 2), and
# that interragate apply --auto-approve refuses: critical, high, medium,
# low or none.
fail_on: high

# Turn rules off entirely. interragate rules lists them.
# disable:
#   - provider-unpinned

# Change how severe a rule is.
# severity:
#   dns-change: high

# Every created or updated resource must carry these tags (directly or
# through the provider's default_tags).
# required_tags: [Owner, CostCenter]

# Resource types, beyond the ones interragate knows, that hold data.
# stateful:
#   - mycorp_database

# Accepted risks. A finding is suppressed only when every address it names
# matches; * matches any run of characters.
# ignore:
#   - rule: stateful-replace
#     address: aws_db_instance.scratch*
#     reason: throwaway database for load tests

# Your own rules. Each one flags the resource changes it matches; fail_on,
# disable, severity and ignore work on it by its id. The README's "Your own
# rules" lists every key and condition.
`, name)
	if len(stateful) == 0 {
		b.WriteString(`# policies:
#   - id: keep-data
#     title: Resources that hold data are not destroyed or replaced without review
#     severity: high
#     match:
#       type: [aws_db_instance, aws_s3_bucket]
#       action: [delete, replace]
`)
		b.WriteString(starterConsumers)
		return b.String()
	}
	b.WriteString(`policies:
  - id: keep-data
    title: Resources that hold data are not destroyed or replaced without review
    severity: high
    remediation: Take a backup first, or keep the object with lifecycle { prevent_destroy = true } until the change is agreed.
    match:
      # The types in this configuration that hold data.
      type: [`)
	b.WriteString(strings.Join(stateful, ", "))
	b.WriteString(`]
      action: [delete, replace]
`)
	b.WriteString(starterConsumers)
	return b.String()
}

// starterConsumers shows how to declare the services outside Terraform that
// use what it manages.
const starterConsumers = `
# Services outside Terraform that use what it manages, such as a site on
# Vercel or an app on phones. interragate checks every plan against what
# each one uses; docs/configuration.md lists every key.
# consumers:
#   - name: storefront-web
#     platform: vercel
#     owners: ["@acme/web"]
#     uses:
#       - resource: aws_db_instance.main
#         attribute: endpoint
#         as: DATABASE_URL
#       - host: api.example.com
`
