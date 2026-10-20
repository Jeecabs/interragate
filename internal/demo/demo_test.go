package demo

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The samples are copies of the fixtures. When a fixture is regenerated,
// these fail until scripts/gen-demo-data.sh copies it again.
func TestSamplesMatchFixtures(t *testing.T) {
	for _, s := range Samples {
		t.Run(s.Name, func(t *testing.T) {
			got, err := s.Plan()
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "plans", s.Dir+".terraform.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("the %s sample differs from its fixture; run scripts/gen-demo-data.sh", s.Name)
			}

			cfg, err := s.Config()
			if err != nil {
				t.Fatal(err)
			}
			scenario := filepath.Join("..", "..", "testdata", "scenarios", s.Dir, "v2")
			seen := map[string]bool{}
			err = fs.WalkDir(cfg, ".", func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				seen[p] = true
				got, err := fs.ReadFile(cfg, p)
				if err != nil {
					return err
				}
				want, err := os.ReadFile(filepath.Join(scenario, filepath.FromSlash(p)))
				if err != nil {
					t.Errorf("%s is in the sample but not in the scenario: %v", p, err)
					return nil
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s differs from the scenario; run scripts/gen-demo-data.sh", p)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			err = filepath.WalkDir(scenario, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || filepath.Ext(p) != ".tf" || d.Name() == "moto_override.tf" {
					return err
				}
				rel, _ := filepath.Rel(scenario, p)
				if !seen[filepath.ToSlash(rel)] {
					t.Errorf("%s is missing from the sample; run scripts/gen-demo-data.sh", rel)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExtract(t *testing.T) {
	s, err := Find("")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := s.Extract(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) != s.Dir {
		t.Errorf("extracted to %s, want a directory named %s", dir, s.Dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.tf")); err != nil {
		t.Errorf("main.tf was not extracted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "modules", "network", "main.tf")); err != nil {
		t.Errorf("the module was not extracted: %v", err)
	}
}

func TestFind(t *testing.T) {
	if s, err := Find("mechanics"); err != nil || s.Dir != "mechanics" {
		t.Errorf("Find(mechanics) = %+v, %v", s, err)
	}
	if s, err := Find("aws-webapp"); err != nil || s.Name != "aws" {
		t.Errorf("Find(aws-webapp) = %+v, %v", s, err)
	}
	if _, err := Find("gcp"); err == nil || err.Error() != `no sample called "gcp"; try aws or mechanics` {
		t.Errorf("Find(gcp) error = %v", err)
	}
}

func TestExtractKeepsTheChangeInGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	s, _ := Find("")
	dir, err := s.Extract(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", dir, "log", "--format=%s", "main.."+s.Branch).Output()
	if err != nil || !strings.Contains(string(out), "The change the sample plan was made from") {
		t.Fatalf("the change is not committed on %s: %s %v", s.Branch, out, err)
	}
	if out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); err != nil || len(out) > 0 {
		t.Errorf("the work tree is not clean: %q %v", out, err)
	}
	// The files are the configuration the plan was made from.
	want, _ := os.ReadFile(filepath.Join("..", "..", "testdata", "scenarios", s.Dir, "v2", "main.tf"))
	if got, _ := os.ReadFile(filepath.Join(dir, "main.tf")); !bytes.Equal(got, want) {
		t.Error("main.tf is not the changed configuration")
	}
}
