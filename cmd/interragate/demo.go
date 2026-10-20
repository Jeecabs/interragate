package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/demo"
	"github.com/Jeecabs/interragate/internal/plan"
)

// demoCmd explains a plan that ships with interragate, so it can be tried
// before it is pointed at real infrastructure: the summary in the terminal,
// and the full report in a browser.
func demoCmd(o *options, stdout, stderr io.Writer) error {
	if len(o.args) > 1 {
		return fmt.Errorf("interragate demo takes one sample at most: %s", sampleNames())
	}
	name := ""
	if len(o.args) == 1 {
		name = o.args[0]
	}
	s, err := demo.Find(name)
	if err != nil {
		return err
	}
	data, err := s.Plan()
	if err != nil {
		return err
	}
	p, err := plan.Parse(data)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "interragate-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dir, err := s.Extract(tmp)
	if err != nil {
		return err
	}
	// The sample stands alone: a .interragate.yaml that happens to sit above
	// the temporary directory has nothing to do with it, but the sample's
	// own declares the services outside Terraform it shows.
	cfg, err := readConfig(filepath.Join(dir, config.FileNames[0]))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	r := analyzeWith(p, o, cfg, dir, "", "sample plan", nil)

	browse := !o.noOpen && o.format == "text" && o.out == ""
	if !browse && o.format == "text" {
		o.hint = "Map and diffs: interragate demo " + s.Name
	}
	if browse {
		// demo opens the report below; --open must not open it again.
		o.open = false
	}
	if err := emit("demo", r, o, stdout, stderr); err != nil || !browse {
		return err
	}
	path, err := writeHTML(r, "")
	if err != nil {
		return err
	}
	if err := openBrowser(path); err != nil {
		fmt.Fprintf(stderr, "\nThe full report is at %s (could not open a browser: %v).\n", path, err)
	} else {
		fmt.Fprintf(stderr, "\nOpened the full report in your browser: %s\n", path)
	}
	var other []string
	for _, x := range demo.Samples {
		if x.Name != s.Name {
			other = append(other, "interragate demo "+x.Name)
		}
	}
	fmt.Fprintf(stderr, `
Next, in a Terraform or OpenTofu directory:
  interragate           plan and explain (runs init first when the directory needs it)
  interragate --open    the same, with this report
  interragate watch     keep this report open; it plans again every time you save
  interragate doctor    check that interragate can plan there
Another sample: %s.
`, strings.Join(other, ", "))
	return nil
}

func sampleNames() string {
	var names []string
	for _, s := range demo.Samples {
		names = append(names, s.Name)
	}
	return strings.Join(names, " or ")
}

// noConfigError explains that dir holds no Terraform configuration, and
// points at directories below it that do.
func noConfigError(dir string) error {
	found := configDirs(dir, 3, 6)
	var b strings.Builder
	fmt.Fprintf(&b, "no Terraform configuration in %s", displayDir(dir))
	if len(found) == 0 {
		b.WriteString("; run interragate in a Terraform directory (or pass -C DIR), explain a saved plan with interragate show tfplan, or try interragate demo")
		return errors.New(b.String())
	}
	b.WriteString(", but these directories have some:\n")
	for _, d := range found {
		fmt.Fprintf(&b, "  %s\n", d)
	}
	fmt.Fprintf(&b, "Run interragate -C %s, or try interragate demo to see a sample plan", found[0])
	return errors.New(b.String())
}

// configDirs lists directories below root, at most depth levels down, that
// hold Terraform configuration: nearest first, then by name. It skips
// provider caches, version control and dependencies, and directories named
// modules, which by convention hold child modules rather than ones you plan.
func configDirs(root string, depth, limit int) []string {
	var found []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "modules" {
			return filepath.SkipDir
		}
		if strings.Count(rel, string(filepath.Separator)) >= depth {
			return filepath.SkipDir
		}
		if hasConfigFiles(p) {
			found = append(found, rel)
		}
		return nil
	})
	sort.SliceStable(found, func(i, j int) bool {
		di, dj := strings.Count(found[i], string(filepath.Separator)), strings.Count(found[j], string(filepath.Separator))
		if di != dj {
			return di < dj
		}
		return found[i] < found[j]
	})
	if len(found) > limit {
		found = found[:limit]
	}
	return found
}
