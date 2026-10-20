package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"aws_db_instance.scratch*", "aws_db_instance.scratch_1", true},
		{"aws_db_instance.scratch*", "aws_db_instance.orders", false},
		{"*", "anything", true},
		{`module.app["*"].aws_instance.web`, `module.app["blue"].aws_instance.web`, true},
		{"module.*.aws_s3_bucket.logs", "module.a.module.b.aws_s3_bucket.logs", true},
		{"exact", "exact", true},
		{"a*b*c", "acb", false},
		{"a*b*c", "a1b2c", true},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.s); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.pattern, c.s, got)
		}
	}
}

func TestLoadAndIgnore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".interragate.yaml")
	os.WriteFile(path, []byte(`
fail_on: medium
disable: [provider-unpinned]
severity:
  dns-change: high
ignore:
  - rule: stateful-replace
    address: aws_db_instance.scratch*
    reason: throwaway database
`), 0o644)
	sub := filepath.Join(dir, "infra")
	os.Mkdir(sub, 0o755)
	found, ok := Find(sub)
	if !ok || found != path {
		t.Fatalf("Find = %q, %v", found, ok)
	}
	c, err := Load(found)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Disabled("provider-unpinned") {
		t.Error("rule should be disabled")
	}
	if sev, ok := c.SeverityFor("dns-change"); !ok || sev.String() != "high" {
		t.Errorf("severity override = %v %v", sev, ok)
	}
	if reason, ok := c.Ignored("stateful-replace", []string{"aws_db_instance.scratch_2"}); !ok || reason != "throwaway database" {
		t.Errorf("Ignored = %q %v", reason, ok)
	}
	if _, ok := c.Ignored("stateful-replace", []string{"aws_db_instance.scratch_2", "aws_db_instance.orders"}); ok {
		t.Error("a finding is only ignored when all its addresses are")
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".interragate.yaml")
	os.WriteFile(path, []byte("fail_onn: high\n"), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for a misspelled key")
	}
}

func TestScanSetting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".interragate.yaml")
	os.WriteFile(path, []byte("scan: [trivy, checkov]\n"), 0o644)
	c, err := Load(path)
	if err != nil || len(c.Scan) != 2 {
		t.Fatalf("scan: %v %+v", err, c)
	}
	os.WriteFile(path, []byte("scan: [tfsec]\n"), 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), `unknown scanner "tfsec"`) {
		t.Errorf("tfsec: %v", err)
	}
}
