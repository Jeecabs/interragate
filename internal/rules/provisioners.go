package rules

import (
	"fmt"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
)

// provisionerRules flag provisioners that run commands during the apply.
func provisionerRules() []*Rule {
	return []*Rule{
		{
			ID: "runs-command", Title: "Runs a command during the apply", Severity: model.Low, Category: Security,
			Doc: "Flags a change that runs a provisioner: local-exec runs a command on the machine that applies the plan, remote-exec on " +
				"the new host, and file copies files to it. A plan shows none of this, so the command is worth reading before the apply. " +
				"Creating a resource runs its creation-time provisioners; destroying it runs its destroy-time ones.",
			Check: checkRunsCommand,
		},
	}
}

func checkRunsCommand(c *Context) []*model.Finding {
	if c.Source == nil {
		return nil
	}
	var out []*model.Finding
	seen := map[string]bool{}
	for _, ch := range c.Changes(model.Create, model.Replace, model.Delete) {
		if !Managed(ch) || seen[ch.Resource] {
			continue
		}
		local := ch.Type + "." + ch.Name
		b := c.Source.Block(moduleConfigOf(ch.Module), local)
		if b == nil {
			continue
		}
		creates := ch.Action == model.Create || ch.Action == model.Replace
		destroys := ch.Action == model.Delete || ch.Action == model.Replace
		for _, p := range b.Provisioners {
			if p.Destroy && !destroys || !p.Destroy && !creates {
				continue
			}
			seen[ch.Resource] = true
			when := "Creating"
			if p.Destroy {
				when = "Destroying"
			}
			var msg string
			switch p.Type {
			case "local-exec":
				msg = fmt.Sprintf("%s %s runs a command on the machine that applies the plan: %s.", when, Code(ch.Address), Code(p.Command))
			case "remote-exec":
				msg = fmt.Sprintf("%s %s runs a command on the host over SSH or WinRM: %s.", when, Code(ch.Address), Code(p.Command))
				if p.More > 0 {
					msg = strings.TrimSuffix(msg, ".") + fmt.Sprintf(", and %d more.", p.More)
				}
			case "file":
				msg = fmt.Sprintf("%s %s copies files to the host over SSH or WinRM.", when, Code(ch.Address))
			default:
				msg = fmt.Sprintf("%s %s runs its %s provisioner.", when, Code(ch.Address), p.Type)
			}
			if p.Command == "" && p.Type != "file" {
				msg = fmt.Sprintf("%s %s runs its %s provisioner.", when, Code(ch.Address), p.Type)
			}
			f := finding(ch, "", msg)
			f.Short = "runs a command during the apply"
			f.Remediation = "Read the command before applying, and check it does what this change needs; a provisioner failing leaves the resource tainted."
			out = append(out, f)
			break
		}
	}
	return out
}

// moduleConfigOf drops instance keys from a module path: module.a["x"]
// becomes module.a.
func moduleConfigOf(module string) string {
	var b strings.Builder
	depth := 0
	for _, r := range module {
		switch {
		case r == '[':
			depth++
		case r == ']':
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}
