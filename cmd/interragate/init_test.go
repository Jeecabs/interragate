package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/config"
)

func TestInit(t *testing.T) {
	dir := t.TempDir()
	tf := `resource "aws_db_instance" "orders" {}
resource "aws_instance" "web" {}
resource "aws_s3_bucket" "logs" {}
`
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(tf), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "init", "-C", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "keeps aws_db_instance and aws_s3_bucket from being destroyed") {
		t.Errorf("init output:\n%s", out)
	}
	cfg, err := config.Load(filepath.Join(dir, ".interragate.yaml"))
	if err != nil {
		t.Fatalf("the starter settings do not load: %v", err)
	}
	if cfg.FailOn != "high" || len(cfg.Policies) != 1 || cfg.Policies[0].ID != "keep-data" {
		t.Errorf("config = %+v", cfg)
	}
	// Never over an existing file.
	if _, err := runCLI(t, "init", "-C", dir); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second init: %v", err)
	}
}

// Without anything that holds data, the policy is an example to fill in,
// and the file still loads.
func TestInitNothingStateful(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`resource "null_resource" "x" {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "init", "-C", dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, ".interragate.yaml"))
	if err != nil || len(cfg.Policies) != 0 {
		t.Errorf("config = %+v, %v", cfg, err)
	}
	if _, err := runCLI(t, "init", "-C", t.TempDir()); err == nil {
		t.Error("a directory without configuration should be refused")
	}
}
