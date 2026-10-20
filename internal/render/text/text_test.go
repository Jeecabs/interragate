package text

import "testing"

// Colour stays; cursor movement, screen clearing and title setting from a
// plan value do not reach the terminal.
func TestSafeForTerminal(t *testing.T) {
	in := "\x1b[1mbold\x1b[0m echo hi\x1b[2K\x1b[1A done \x1b]0;pwned\x07\r\n\tok"
	want := "\x1b[1mbold\x1b[0m echo hi�[2K�[1A done �]0;pwned��\n\tok"
	if got := safeForTerminal(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
