package main

import (
	"strings"
	"testing"
)

// A flag a command has no use for is refused, naming the command, rather
// than silently ignored.
func TestUnusedFlagsAreRefused(t *testing.T) {
	refused := []struct {
		args []string
		want string
	}{
		{[]string{"diff", mechanicsPlan, awsPlan, "-o", "x.md"}, "interragate diff does not use -o"},
		{[]string{"show", mechanicsPlan, "--save-plan", "x"}, "interragate show does not use --save-plan"},
		{[]string{"plan", "--auto-approve"}, "interragate plan does not use --auto-approve"},
		{[]string{"explain", mechanicsPlan, "random_pet.cluster", "-f", "json"}, "interragate explain does not use -f"},
		{[]string{"check", mechanicsPlan, "--all"}, "interragate check does not use --all"},
		{[]string{"demo", "--bin", "/nonexistent"}, "interragate demo does not use --bin"},
		{[]string{"doctor", "--out", "x"}, "interragate doctor does not use --out"},
		{[]string{"rules", "-o", "x"}, "interragate rules does not use -o"},
		{[]string{"completion", "bash", "--open"}, "interragate completion does not use --open"},
		{[]string{"version", "-f", "json"}, "interragate version does not use -f"},
		{[]string{"watch", "--format=json"}, "interragate watch does not use --format: it keeps its report in the browser"},
		{[]string{"apply", "--out", "x"}, "interragate apply does not use --out: it prints the plan for you to confirm"},
		{[]string{"show", mechanicsPlan, "--", "-var-file=prod.tfvars"}, "interragate show does not run plan, so it takes no plan arguments after --"},
		{[]string{"review", mechanicsPlan, "--no-init"}, "only apply without one"},
	}
	for _, c := range refused {
		withConfirmation(t, "", true)
		if _, err := runCLI(t, c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want %q", c.args, err, c.want)
		}
	}

	// The default format, and --no-color, are fine anywhere.
	for _, args := range [][]string{
		{"explain", mechanicsPlan, "random_pet.cluster", "-f", "text", "--no-color"},
		{"version", "--no-color"},
		{"-f", "json", "show", mechanicsPlan},
	} {
		if _, err := runCLI(t, args...); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
}

// A mistyped command gets the one it most likely meant; a word nowhere
// near one, or a file, is still taken for a plan argument.
func TestNearestCommand(t *testing.T) {
	for word, want := range map[string]string{"shwo": "show", "explian": "explain", "wtach": "watch", "Check": "check", "xyz": "", "tfplan": ""} {
		if got := nearestCommand(word); got != want {
			t.Errorf("nearestCommand(%q) = %q, want %q", word, got, want)
		}
	}
}
