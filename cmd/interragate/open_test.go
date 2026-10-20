package main

import (
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
)

// interragate open shows the last report interragate planned in a directory,
// without planning again, and says when there is none.
func TestOpenKeptReport(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	if _, err := runCLI(t, "open", dir, "--no-open"); err == nil || !strings.Contains(err.Error(), "no plan of") {
		t.Fatalf("before any plan: %v", err)
	}
	keepReport(dir, &model.Report{Plan: model.PlanInfo{Dir: "infra"}, GeneratedAt: "2026-10-20T01:00:00Z", Risk: model.Risk{Headline: "Creates 1 resource."}})
	out, err := runCLI(t, "open", dir, "--no-open")
	if err != nil || !strings.Contains(out, ".html") {
		t.Fatalf("after a plan: %v\n%s", err, out)
	}
}
