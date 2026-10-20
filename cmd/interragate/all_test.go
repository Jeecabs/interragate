package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRootDirs(t *testing.T) {
	top := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(top, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("envs/prod/main.tf", "module \"app\" {\n  source = \"../../components/app\"\n}\n")
	write("envs/staging/main.tf", "module \"app\" {\n  source = \"../../components/app\"\n}\n")
	write("components/app/main.tf", "resource \"terraform_data\" \"x\" {}\n")
	write("modules/net/main.tf", "resource \"terraform_data\" \"y\" {}\n")
	write("tools/lone/main.tf", "resource \"terraform_data\" \"z\" {}\n")
	got := rootDirs(top)
	want := []string{"envs/prod", "envs/staging", "tools/lone"}
	if !slices.Equal(got, want) {
		t.Errorf("rootDirs = %v, want %v", got, want)
	}
	if slugify("envs/prod") != "envs-prod" || slugify(".") != "root" {
		t.Errorf("slugify: %q %q", slugify("envs/prod"), slugify("."))
	}
}
