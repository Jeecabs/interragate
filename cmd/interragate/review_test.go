package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReview(t *testing.T) {
	// A note, then Enter, then n, then stop.
	withConfirmation(t, "Snapshot checked with the DBA\n\nn\nq\n", true)
	out, err := runCLI(t, "review", awsPlan, "--no-color")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"need a look before this plan is applied",
		"1 of 15",
		"### Plan review",
		"Reviewed 2 of 15 changes that need a look.",
		"  \n  Snapshot checked with the DBA",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("review output lacks %q", want)
		}
	}
	// Worst first: the first three answers went to critical changes.
	checklist := out[strings.Index(out, "### Plan review"):]
	lines := strings.Split(checklist, "\n")
	var items []string
	for _, l := range lines {
		if strings.HasPrefix(l, "- [") {
			items = append(items, l)
		}
	}
	if len(items) != 15 || !strings.HasPrefix(items[0], "- [x]") || !strings.HasPrefix(items[1], "- [x]") || !strings.HasPrefix(items[2], "- [ ]") || !strings.Contains(items[0], "(critical)") {
		t.Errorf("checklist:\n%s", strings.Join(items, "\n"))
	}
}

func TestReviewWritesNotes(t *testing.T) {
	withConfirmation(t, "\n\n\n", true)
	path := filepath.Join(t.TempDir(), "review.md")
	if _, err := runCLI(t, "review", mechanicsPlan, "--no-color", "-o", path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), "- [x]") {
		t.Errorf("notes file: %v\n%s", err, b)
	}
}

func TestReviewNeedsATerminal(t *testing.T) {
	withConfirmation(t, "", false)
	if _, err := runCLI(t, "review", awsPlan); err == nil || !strings.Contains(err.Error(), "needs a terminal") {
		t.Errorf("err = %v", err)
	}
}
