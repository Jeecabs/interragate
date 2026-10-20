package main

import (
	"os/exec"
	"runtime"
	"strings"

	"github.com/Jeecabs/interragate/internal/delta"
	"github.com/Jeecabs/interragate/internal/model"
)

// riseWorthANotice says, for watch, what to tell someone who is in their
// editor rather than looking at the page: that the save they just made
// took the plan to high or critical risk. Anything quieter waits for them
// to look.
func riseWorthANotice(prev, now *model.Report, d *delta.Delta) (title, body string, ok bool) {
	if prev == nil || d == nil || now.Risk.Level < model.High || now.Risk.Level <= prev.Risk.Level {
		return "", "", false
	}
	body = now.Risk.Headline
	for _, s := range d.Sentences {
		if strings.HasPrefix(s.Text, "New: ") {
			body = strings.TrimPrefix(s.Text, "New: ")
			break
		}
	}
	return "interragate: " + levelWord(now.Risk.Level) + " risk", strings.ReplaceAll(body, "`", ""), true
}

func levelWord(s model.Severity) string {
	name := s.String()
	return strings.ToUpper(name[:1]) + name[1:]
}

// notifier is the command that shows a desktop notification, or nil where
// interragate knows none; tests replace it.
var notifier = func(title, body string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		quote := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
		return exec.Command("osascript", "-e", "display notification "+quote(body)+" with title "+quote(title))
	case "linux", "freebsd", "openbsd", "netbsd":
		if _, err := exec.LookPath("notify-send"); err == nil {
			return exec.Command("notify-send", "--app-name=interragate", title, body)
		}
	}
	return nil
}

// notify shows a desktop notification if it can, and never waits for it
// or minds if it fails.
func notify(title, body string) {
	cmd := notifier(title, body)
	if cmd == nil {
		return
	}
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}
