package rules

import (
	"path/filepath"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/consumers"
	"github.com/Jeecabs/interragate/internal/model"
)

// consumerRules check plans against the services outside Terraform that
// .interragate.yaml declares, such as a site on Vercel or an app on
// phones. Package consumers does the work; these give what it finds rule
// IDs, so disable, severity and ignore work on it. They are medium unless
// the file says otherwise: they ask for attention without failing
// interragate check at its default threshold, since interragate cannot see
// the services, only what it was told about them.
func consumerRules() []*Rule {
	return []*Rule{
		{
			ID: "consumer-loses", Title: "Destroys what a service outside Terraform uses", Severity: model.Medium, Category: Consumers,
			Doc: "Flags a resource, output or host that .interragate.yaml says a service outside Terraform uses, such as a site on Vercel " +
				"or an app on phones, when this plan destroys or removes it, or stops answering for the host.",
			Check: func(c *Context) []*model.Finding { return c.Consumers().Findings(model.EffectLoses) },
		},
		{
			ID: "consumer-value", Title: "Changes a value a service outside Terraform holds", Severity: model.Medium, Category: Consumers,
			Doc: "Flags a value that .interragate.yaml says a service outside Terraform holds, such as an endpoint in an environment variable, " +
				"when this plan changes it or leaves it known only after apply. How soon the service can take the new value depends on where " +
				"it runs: a site takes it on its next deployment, while copies of an app already on phones keep the old one until people update.",
			Check: func(c *Context) []*model.Finding { return c.Consumers().Findings(model.EffectValue) },
		},
		{
			ID: "consumer-reach", Title: "Closes a way in a service outside Terraform uses", Severity: model.Medium, Category: Consumers,
			Doc: "Flags a firewall that .interragate.yaml says a service outside Terraform comes in through, such as a partner's callbacks " +
				"on a port, when this plan stops letting it in. It reads AWS security groups and their rules and Google Cloud firewalls.",
			Check: func(c *Context) []*model.Finding { return c.Consumers().Findings(model.EffectReach) },
		},
		{
			ID: "consumer-outage", Title: "Takes away what a service outside Terraform relies on during the apply", Severity: model.Medium, Category: Consumers,
			Doc: "Flags a stretch of the apply in which something a service outside Terraform relies on is missing: a resource it uses " +
				"while it is replaced, or the targets of the load balancer behind a host it calls. It follows interragate's simulation of the " +
				"apply, including where it is expected to fail.",
			AfterApply: true,
			Check: func(c *Context) []*model.Finding {
				if c.Timeline == nil {
					return nil
				}
				return c.Consumers().OutageFindings(*c.Timeline)
			},
		},
	}
}

// Consumers is what the plan does to the services outside Terraform that
// the configuration file declares, worked out once for every rule.
func (c *Context) Consumers() *consumers.Assessment {
	if c.consumers == nil {
		in := consumers.Input{Report: c.Report, Plan: c.Plan, Graph: c.Graph, View: c, File: config.FileNames[0]}
		if c.Config != nil {
			in.Services = c.Config.Consumers
			if c.Config.Path != "" {
				in.File = filepath.Base(c.Config.Path)
			}
		}
		c.consumers = consumers.Assess(in)
	}
	return c.consumers
}
