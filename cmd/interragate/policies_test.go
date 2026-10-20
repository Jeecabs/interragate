package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const legacyPolicy = `policies:
  - id: legacy-kept
    title: The legacy module is not torn down yet
    severity: critical
    match:
      address: "module.legacy.*"
      action: destroy
`

func writeConfig(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".interragate.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRulesListsPolicies(t *testing.T) {
	cfg := writeConfig(t, legacyPolicy)

	out, err := runCLI(t, "rules", "--config", cfg)
	if err != nil {
		t.Fatal(err)
	}
	builtin, policies, ok := strings.Cut(out, "\nPolicies from .interragate.yaml:\n")
	if !ok || !strings.Contains(builtin, "required-tags") || strings.Contains(builtin, "legacy-kept") ||
		!strings.HasPrefix(policies, "legacy-kept ") || !strings.Contains(policies, " policy ") {
		t.Errorf("text listing:\n%s", out)
	}

	out, err = runCLI(t, "rules", "--config", cfg, "--format", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	builtin, policies, ok = strings.Cut(out, "\n# Policies\n")
	if !ok || !strings.Contains(builtin, "## required-tags") || !strings.Contains(policies, "## legacy-kept") ||
		!strings.Contains(policies, "Flags destroying resources at `module.legacy.*`.") {
		t.Errorf("markdown listing:\n%s", out)
	}

	out, err = runCLI(t, "rules", "--config", cfg, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var docs []struct{ ID, Category, Source string }
	if err := json.Unmarshal([]byte(out), &docs); err != nil {
		t.Fatal(err)
	}
	last := docs[len(docs)-1]
	if last.ID != "legacy-kept" || last.Category != "policy" || last.Source != ".interragate.yaml" || docs[0].Source != "" {
		t.Errorf("json listing: first %+v, last %+v", docs[0], last)
	}
}

func TestRulesFindsTheNearestConfig(t *testing.T) {
	dir := filepath.Dir(writeConfig(t, legacyPolicy))
	out, err := runCLI(t, "rules", "-C", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\nPolicies from .interragate.yaml:\nlegacy-kept ") {
		t.Errorf("listing lacks the policy:\n%s", out)
	}
}

func TestRulesReportsABadPolicy(t *testing.T) {
	cfg := writeConfig(t, "policies: [{id: a, title: T, severity: high, match: {action: destory}}]\n")
	_, err := runCLI(t, "rules", "--config", cfg)
	if err == nil || !strings.HasSuffix(err.Error(), `policies[0] (a): unknown action "destory"; use create, update, replace, delete, read, forget or no-op`) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckFailsOnAPolicy(t *testing.T) {
	// mechanics has no critical findings of its own.
	_, err := runCLI(t, "check", mechanicsPlan, "--fail-on", "critical", "--config", writeConfig(t, legacyPolicy))
	var code exitError
	if !errors.As(err, &code) || code != 2 {
		t.Fatalf("check with a critical policy finding: err = %v, want exit 2", err)
	}
}
