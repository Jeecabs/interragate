package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/kb"
	"github.com/Jeecabs/interragate/internal/model"
)

// availabilityRules flag changes that restart, disconnect or briefly remove
// what others depend on.
func availabilityRules() []*Rule {
	return []*Rule{
		{
			ID: "network-disruption", Title: "Destroys or replaces core networking", Severity: model.High, Category: Availability,
			Doc: "Flags destroying or replacing VPCs, subnets, route tables, gateways, load balancers, listeners and CDN distributions, " +
				"which interrupts everything that sends traffic through them.",
			Check: checkNetworkDisruption,
		},
		{
			ID: "downtime-replace", Title: "Replacement leaves a gap", Severity: model.Medium, Category: Availability,
			Doc: "Flags replacements that destroy the old object before creating the new one, for resources that serve traffic. " +
				"create_before_destroy avoids the gap.",
			Check: checkDowntimeReplace,
		},
		{
			ID: "disruptive-update", Title: "In-place update still causes downtime", Severity: model.Medium, Category: Availability,
			Doc: "Flags in-place updates that still restart or resize something: instance types, database classes and engine versions, " +
				"cluster versions and similar. They read like harmless updates in a plan.",
			Check: checkDisruptiveUpdate,
		},
		{
			ID: "dns-change", Title: "Changes DNS", Severity: model.Medium, Category: Availability,
			Doc:   "Flags DNS records whose target changes or that are destroyed. Resolvers keep old answers for the TTL, so mistakes linger.",
			Check: checkDNSChange,
		},
		{
			ID: "large-blast-radius", Title: "Many resources depend on what is replaced", Severity: model.Medium, Category: Availability,
			Doc: "Flags destroying or replacing a resource that many others depend on, directly or through modules. " +
				"Configure with rules.large-blast-radius.threshold (default 10).",
			Check: checkBlastRadius,
		},
	}
}

var networkCore = map[string]bool{
	"aws_vpc": true, "aws_subnet": true, "aws_route_table": true, "aws_nat_gateway": true, "aws_internet_gateway": true,
	"aws_egress_only_internet_gateway": true, "aws_ec2_transit_gateway": true, "aws_ec2_transit_gateway_vpc_attachment": true,
	"aws_vpn_connection": true, "aws_vpn_gateway": true, "aws_vpc_peering_connection": true, "aws_dx_connection": true,
	"aws_lb": true, "aws_alb": true, "aws_elb": true, "aws_lb_listener": true, "aws_alb_listener": true, "aws_cloudfront_distribution": true,
	"aws_api_gateway_rest_api": true, "aws_apigatewayv2_api": true, "aws_eip": true, "aws_globalaccelerator_accelerator": true,
	"google_compute_network": true, "google_compute_subnetwork": true, "google_compute_router": true, "google_compute_router_nat": true,
	"google_compute_global_forwarding_rule": true, "google_compute_forwarding_rule": true, "google_compute_url_map": true,
	"google_compute_global_address": true, "google_compute_address": true,
	"azurerm_virtual_network": true, "azurerm_subnet": true, "azurerm_application_gateway": true, "azurerm_lb": true,
	"azurerm_firewall": true, "azurerm_public_ip": true, "azurerm_nat_gateway": true, "azurerm_frontdoor": true,
	"azurerm_cdn_frontdoor_profile": true, "azurerm_virtual_network_gateway": true,
	"cloudflare_load_balancer": true, "digitalocean_vpc": true, "digitalocean_loadbalancer": true, "hcloud_network": true,
}

func checkNetworkDisruption(c *Context) []*model.Finding {
	groups := map[string][]*model.Change{}
	var order []string
	for _, ch := range c.Changes(model.Delete, model.Replace) {
		// A count shift explains these changes better; it is reported there.
		if !networkCore[ch.Type] || ch.Deposed != "" || c.shifted(ch.Resource) {
			continue
		}
		if groups[ch.Resource] == nil {
			order = append(order, ch.Resource)
		}
		groups[ch.Resource] = append(groups[ch.Resource], ch)
	}
	var out []*model.Finding
	for _, res := range order {
		chs := groups[res]
		first := chs[0]
		var addrs, parts []string
		for _, ch := range chs {
			addrs = append(addrs, ch.Address)
			verb := "destroyed"
			if ch.Action == model.Replace {
				verb = "replaced"
			}
			parts = append(parts, Code(ch.Address)+" "+verb)
		}
		var msg string
		them := "it"
		if len(chs) == 1 {
			verb := "Destroying"
			if first.Action == model.Replace {
				verb = "Replacing"
			}
			msg = fmt.Sprintf("%s %s (%s) interrupts traffic through it.", verb, Code(first.Address), Article(first.Noun))
		} else {
			them = "them"
			msg = fmt.Sprintf("%d %s go away or are recreated: %s.", len(chs), pluralNoun(first.Noun), List(parts, 4))
		}
		if down := c.Graph.Downstream(res); len(down) > 0 {
			msg += " " + dependsPhrase(down, them)
		}
		for _, ch := range chs {
			if ch.Action == model.Replace && ch.Order == "delete-first" {
				msg += fmt.Sprintf(" Replacing destroys the old %s before the new one exists.", first.Noun)
				break
			}
		}
		f := &model.Finding{Title: "Destroys or replaces core networking", Message: msg, Addresses: addrs}
		f.Short = "disrupts " + pluralNoun(first.Noun)
		if len(chs) == 1 {
			f.Short = "disrupts " + Article(first.Noun)
		}
		f.Remediation = "Make sure the change is intended and schedule it; where the provider allows, use create_before_destroy so traffic can move to the new object first."
		out = append(out, f)
	}
	return out
}

func dependsPhrase(down []string, them string) string {
	if len(down) == 1 {
		return fmt.Sprintf("%s depends on %s.", Code(down[0]), them)
	}
	codes := make([]string, len(down))
	for i, d := range down {
		codes[i] = Code(d)
	}
	if len(down) <= 3 {
		return fmt.Sprintf("%s depend on %s.", List(codes, 3), them)
	}
	return fmt.Sprintf("%d resources depend on %s, including %s.", len(down), them, List(codes[:3], 3))
}

var downtimeCategories = map[kb.Category]bool{kb.Compute: true, kb.Containers: true, kb.Serverless: true, kb.Edge: true, kb.Firewall: true, kb.DNS: true, kb.Identity: true, kb.Messaging: true}

func checkDowntimeReplace(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Replace) {
		info := c.Info(ch)
		// Indirect replacements follow from another change, which is where
		// the attention belongs.
		if ch.Order != "delete-first" || info.Stateful || networkCore[ch.Type] || !downtimeCategories[info.Category] || ch.Importing || ch.Indirect {
			continue
		}
		msg := fmt.Sprintf("%s is destroyed before its replacement is created, so there is a gap while %s does not exist.", Code(ch.Address), Article(info.Noun))
		rem := "Add lifecycle { create_before_destroy = true }. If the name must be unique, switch to name_prefix so both can exist at once."
		if ch.Type == "aws_security_group" {
			msg += " A security group still attached to network interfaces cannot be deleted, so the apply can hang until it times out."
		}
		f := finding(ch, "Replacement leaves a gap", msg)
		f.Short = "replaces " + Article(info.Noun) + " with a gap"
		f.Paths = ch.ForcedBy
		f.Remediation = rem
		out = append(out, f)
	}
	return out
}

func checkDisruptiveUpdate(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Update) {
		info := c.Info(ch)
		if len(info.Disruptive) == 0 || ch.Diff == nil {
			continue
		}
		var attrs []string
		for _, child := range ch.Diff.Children {
			if child.Kind == diff.Unchanged {
				continue
			}
			if child.Key == "engine_version" && engineChangeFails(c, ch) {
				continue // the apply fails there instead; engine-downgrade or major-upgrade-not-allowed says so
			}
			if _, ok := info.Disruptive[child.Key]; ok && !(child.Unknown && child.Kind == diff.Added) {
				attrs = append(attrs, child.Key)
			}
		}
		if len(attrs) == 0 {
			continue
		}
		sort.Strings(attrs)
		note := info.Disruptive[attrs[0]]
		if ch.Type == "aws_db_instance" || ch.Type == "aws_rds_cluster" || ch.Type == "aws_rds_cluster_instance" {
			if now, _ := Bool(c.After(ch), "apply_immediately"); now {
				note = "the database restarts as soon as this applies (apply_immediately = true)"
			} else {
				note = "the database restarts in its next maintenance window"
			}
		}
		f := finding(ch, "In-place update still causes downtime", fmt.Sprintf("Changing %s on %s happens in place, but %s.", codeList(attrs), Code(ch.Address), note))
		f.Short = "restarts " + Article(info.Noun)
		f.Paths = attrs
		f.Remediation = "Apply it in a maintenance window, or roll it out instance by instance."
		out = append(out, f)
	}
	return out
}

type dnsFields struct{ name, typ, ttl, values string }

var dnsTypes = map[string]dnsFields{
	"aws_route53_record":       {"name", "type", "ttl", "records"},
	"google_dns_record_set":    {"name", "type", "ttl", "rrdatas"},
	"cloudflare_record":        {"name", "type", "ttl", "value"},
	"cloudflare_dns_record":    {"name", "type", "ttl", "content"},
	"digitalocean_record":      {"name", "type", "ttl", "value"},
	"azurerm_dns_a_record":     {"name", "", "ttl", "records"},
	"azurerm_dns_cname_record": {"name", "", "ttl", "record"},
	"azurerm_dns_aaaa_record":  {"name", "", "ttl", "records"},
	"azurerm_dns_txt_record":   {"name", "", "ttl", "record"},
}

func checkDNSChange(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Update, model.Replace, model.Delete) {
		fields, ok := dnsTypes[ch.Type]
		if !ok {
			continue
		}
		before, after := c.Before(ch), c.After(ch)
		name := String(before, fields.name)
		if name == "" {
			name = String(after, fields.name)
		}
		rtype := ""
		if fields.typ != "" {
			rtype = String(before, fields.typ)
		}
		label := Code(name)
		if rtype != "" {
			label += " (" + rtype + ")"
		}
		newName := String(after, fields.name)
		var f *model.Finding
		switch {
		case ch.Action != model.Delete && name != "" && newName != "" && name != newName:
			f = finding(ch, "Renames a DNS record", fmt.Sprintf("Renames %s to %s: the old name stops resolving.", label, Code(newName)))
			f.Severity = model.High
			f.Short = "renames a DNS record"
		case ch.Action == model.Delete:
			f = finding(ch, "Removes a DNS record", fmt.Sprintf("%s stops resolving when %s is destroyed.", label, Code(ch.Address)))
			f.Severity = model.High
			f.Short = "removes a DNS record"
		case !diff.Equal(dnsValue(before, fields.values), dnsValue(after, fields.values)) || !diff.Equal(before["alias"], after["alias"]):
			bv, av := dnsTarget(before, fields.values), dnsTarget(after, fields.values)
			msg := fmt.Sprintf("Changes what %s resolves to", label)
			if bv != "" && av != "" && !c.Unknown(ch, fields.values) {
				msg += fmt.Sprintf(": %s → %s", Code(bv), Code(av))
			} else if c.Unknown(ch, fields.values) {
				msg += " (the new value is known after apply)"
			}
			msg += "."
			// Resolvers cached the old answer under the old TTL, so that is
			// how long it can linger, whatever the new TTL is.
			if old := atoi(before[fields.ttl]); old > 0 {
				msg += fmt.Sprintf(" Resolvers may keep serving the old answer for up to %s, the record's TTL.", ttlWords(old))
				if now := atoi(after[fields.ttl]); now > 0 && now < old {
					msg += fmt.Sprintf(" Lowering the TTL to %s in the same apply does not shorten that: lower it in an earlier apply, wait out the old TTL, then change the record.", ttlWords(now))
				}
			}
			f = finding(ch, "Changes DNS", msg)
			f.Short = "changes DNS for " + name
		case !diff.Equal(before[fields.ttl], after[fields.ttl]):
			f = finding(ch, "Changes a DNS TTL", fmt.Sprintf("Changes the TTL of %s from %s to %s seconds.", label, Value(before[fields.ttl]), Value(after[fields.ttl])))
			f.Severity = model.Low
		default:
			continue
		}
		f.Remediation = "Lower the TTL ahead of planned DNS changes, and double-check the new target before applying."
		out = append(out, f)
	}
	return out
}

// ttlWords says a TTL in seconds as a reader would: "5 minutes", "a day".
func ttlWords(seconds int) string {
	unit := func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	switch {
	case seconds < 60 || seconds < 3600 && seconds%60 != 0:
		return unit(seconds, "a second", "seconds")
	case seconds%86400 == 0:
		return unit(seconds/86400, "a day", "days")
	case seconds%3600 == 0:
		return unit(seconds/3600, "an hour", "hours")
	case seconds < 3600:
		return unit(seconds/60, "a minute", "minutes")
	case seconds < 86400:
		return "about " + unit((seconds+1800)/3600, "an hour", "hours")
	}
	return "about " + unit((seconds+43200)/86400, "a day", "days")
}

func dnsValue(obj map[string]any, attr string) any {
	if obj == nil {
		return nil
	}
	return obj[attr]
}

func dnsTarget(obj map[string]any, attr string) string {
	if obj == nil {
		return ""
	}
	if alias, ok := obj["alias"].([]any); ok && len(alias) > 0 {
		if m, ok := alias[0].(map[string]any); ok {
			return "alias " + String(m, "name")
		}
	}
	return strings.Join(Strings(obj[attr]), ", ")
}

func checkBlastRadius(c *Context) []*model.Finding {
	threshold := c.Setting("large-blast-radius", "threshold", 10)
	// The widest blast radius first: a resource inside one already told
	// adds nothing but a smaller copy of it.
	var candidates []*model.Change
	for _, ch := range c.Changes(model.Delete, model.Replace) {
		if ch.Dependents >= threshold {
			candidates = append(candidates, ch)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Dependents > candidates[j].Dependents })
	covered := map[string]bool{}
	var out []*model.Finding
	for _, ch := range candidates {
		if covered[ch.Resource] {
			continue
		}
		covered[ch.Resource] = true
		down := c.Graph.Downstream(ch.Resource)
		for _, d := range down {
			covered[d] = true
		}
		// Only the addresses the message shows are written out.
		shown := down[:min(len(down), 6)]
		codes := make([]string, len(shown))
		for i, d := range shown {
			codes[i] = Code(d)
		}
		named := List(codes, 6)
		if rest := len(down) - len(shown); rest > 0 {
			named = strings.Join(codes, ", ") + fmt.Sprintf(" and %d more", rest)
		}
		verb := "destroyed"
		if ch.Action == model.Replace {
			verb = "replaced"
		}
		f := finding(ch, "Many resources depend on what is "+verb, fmt.Sprintf("%d resources depend on %s, which is %s: %s.", len(down), Code(ch.Resource), verb, named))
		f.Short = fmt.Sprintf("%s something %d resources depend on", verb, len(down))
		f.Remediation = "Check how the dependents handle the change; split the apply or use create_before_destroy where it helps."
		out = append(out, f)
	}
	return out
}

func pluralNoun(noun string) string {
	switch {
	case strings.HasSuffix(noun, "s"), strings.HasSuffix(noun, "x"), strings.HasSuffix(noun, "ch"), strings.HasSuffix(noun, "sh"):
		return noun + "es"
	case strings.HasSuffix(noun, "y") && len(noun) > 1 && !strings.ContainsRune("aeiou", rune(noun[len(noun)-2])):
		return noun[:len(noun)-1] + "ies"
	}
	return noun + "s"
}
