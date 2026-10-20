// Package demo carries sample plans inside the binary, with the
// configuration each was planned from, so interragate can be tried without
// Terraform, a cloud account or a plan of your own.
//
// The samples are copies of the test fixtures, made by
// scripts/gen-demo-data.sh; the tests fail if the two drift apart.
package demo

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// all: keeps the files whose names start with a dot, such as a sample's
// .interragate.yaml, which a plain directory pattern leaves out.
//
//go:embed all:data
var data embed.FS

// Sample is one plan that ships with interragate.
type Sample struct {
	// Name is what `interragate demo NAME` takes.
	Name string
	// About says what the plan shows, in a phrase.
	About string
	// Dir is the sample's directory name, which the report shows as the
	// working directory.
	Dir string
	// Branch names the change, as the branch it would be made on.
	Branch string
}

// Samples in the order interragate offers them; the first is the default.
var Samples = []Sample{
	{
		Name:   "aws",
		About:  "a web app on AWS that replaces its database, destroys a bucket, opens SSH to the internet and undoes a hotfix made by hand",
		Dir:    "aws-webapp",
		Branch: "encrypt-orders-db",
	},
	{
		Name:   "mechanics",
		About:  "Terraform's trickier mechanics: moved and removed blocks, an import that replaces, a count shift, a tainted resource and -replace",
		Dir:    "mechanics",
		Branch: "tidy-up",
	},
}

// Find returns the sample called name, or the default when name is empty.
func Find(name string) (Sample, error) {
	if name == "" {
		return Samples[0], nil
	}
	var names []string
	for _, s := range Samples {
		if s.Name == name || s.Dir == name {
			return s, nil
		}
		names = append(names, s.Name)
	}
	return Sample{}, fmt.Errorf("no sample called %q; try %s", name, strings.Join(names, " or "))
}

// Plan returns the sample's plan JSON, as terraform show -json wrote it.
func (s Sample) Plan() ([]byte, error) {
	f, err := data.Open(path.Join("data", s.Dir, "plan.json.gz"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, zr); err != nil {
		return nil, err
	}
	return buf.Bytes(), zr.Close()
}

// Config returns the configuration the plan was made from.
func (s Sample) Config() (fs.FS, error) {
	return fs.Sub(data, path.Join("data", s.Dir, "config"))
}

// Extract writes the sample's configuration into a new directory named
// after the sample, inside parent, and returns its path. The analysis reads
// locations and lifecycle settings from real files, as it would in a
// Terraform directory.
//
// When git is installed, the directory is also a repository in which main
// holds the configuration before the change and the sample's branch holds
// the change, so the report shows the edit behind each planned change. The
// configuration is the same either way.
func (s Sample) Extract(parent string) (string, error) {
	root := filepath.Join(parent, s.Dir)
	if before, err := fs.Sub(data, path.Join("data", s.Dir, "before")); err == nil && s.history(root, before) {
		return root, nil
	}
	cfg, err := s.Config()
	if err != nil {
		return "", err
	}
	return root, write(cfg, root)
}

// history makes root a repository with the configuration before the
// change on main and the change committed on the sample's branch. It
// reports whether that worked; a failure leaves root for Extract to fill.
func (s Sample) history(root string, before fs.FS) bool {
	if _, err := exec.LookPath("git"); err != nil || s.Branch == "" {
		return false
	}
	cfg, err := s.Config()
	if err != nil {
		return false
	}
	git := func(args ...string) error {
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=interragate", "-c", "user.email=demo@interragate.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		// The user's own settings and hooks have no business in a sample.
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		return cmd.Run()
	}
	ok := write(before, root) == nil &&
		git("init", "-q") == nil &&
		git("symbolic-ref", "HEAD", "refs/heads/main") == nil &&
		git("add", "-A") == nil &&
		git("commit", "-q", "-m", "The configuration before the change") == nil &&
		git("switch", "-q", "-c", s.Branch) == nil &&
		clear(root) == nil &&
		write(cfg, root) == nil &&
		git("add", "-A") == nil &&
		git("commit", "-q", "-m", "The change the sample plan was made from") == nil
	if !ok {
		os.RemoveAll(root)
	}
	return ok
}

// clear removes everything in root but the repository itself.
func clear(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// write copies a configuration into root.
func write(cfg fs.FS, root string) error {
	return fs.WalkDir(cfg, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := fs.ReadFile(cfg, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}
