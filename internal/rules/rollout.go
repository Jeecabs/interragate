package rules

import (
	"fmt"

	"github.com/Jeecabs/interragate/internal/model"
)

// rolloutRules flag a new launch template that running instances never get.
func rolloutRules() []*Rule {
	return []*Rule{
		{
			ID: "no-rollout", Title: "The new launch template does not reach running instances", Severity: model.Low, Category: Availability,
			Doc: "Flags a launch template change that an Auto Scaling group using it will not roll out. Without an instance_refresh block, " +
				"only instances launched later get the new template; and a group that launches $Default or a fixed version does not get it at all.",
			Check: checkNoRollout,
		},
	}
}

func checkNoRollout(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, lt := range c.Changes(model.Update, model.Replace) {
		if lt.Type != "aws_launch_template" || lt.Cosmetic != "" || c.Graph == nil {
			continue
		}
		// An update that makes no new version (a description, tags, the
		// default version setting) gives the group nothing to roll out.
		if lt.Action == model.Update && !c.Unknown(lt, "latest_version") {
			continue
		}
		node := c.Graph.Nodes[lt.Resource]
		if node == nil {
			continue
		}
		for _, id := range node.Down {
			if n := c.Graph.Nodes[id]; n == nil || n.Type != "aws_autoscaling_group" {
				continue
			}
			for _, asg := range c.Instances(id) {
				// A new group starts with the new template.
				if asg.Action == model.Create || asg.Action == model.Replace || asg.Action == model.Delete {
					continue
				}
				// With count or for_each on both, each group uses one of
				// the templates: the one whose ID it holds.
				if !usesTemplate(c, asg, lt) {
					continue
				}
				if f := rollout(c, lt, asg); f != nil {
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// usesTemplate reports whether a group launches from a template, by the
// ID or name it holds now; when it holds neither, any template it refers
// to counts.
func usesTemplate(c *Context, asg, lt *model.Change) bool {
	id, name := String(c.Before(lt), "id"), String(c.Before(lt), "name")
	if id == "" && name == "" {
		return true
	}
	held := false
	for _, path := range []string{"launch_template.id", "launch_template.name", "mixed_instances_policy.launch_template.launch_template_specification.launch_template_id",
		"mixed_instances_policy.launch_template.launch_template_specification.launch_template_name"} {
		v, ok := Attr(c.Before(asg), path)
		s, _ := v.(string)
		if !ok || s == "" {
			continue
		}
		held = true
		if s == id || s == name {
			return true
		}
	}
	return !held
}

func rollout(c *Context, lt, asg *model.Change) *model.Finding {
	obj := c.After(asg)
	if obj == nil {
		return nil
	}
	if refresh, ok := obj["instance_refresh"].([]any); ok && len(refresh) > 0 {
		return nil
	}
	version, known := "", false
	for _, path := range []string{"launch_template.version", "mixed_instances_policy.launch_template.launch_template_specification.version"} {
		if v, ok := Attr(obj, path); ok {
			version, known = fmt.Sprint(v), v != nil
			if v == nil || v == "" {
				version = "$Default" // what AWS uses when none is given
			}
		}
	}
	moves, _ := c.After(lt)["update_default_version"].(bool)
	// A replaced template is a new one, whose default and only version
	// has the new settings, and the group moves to it.
	stuck := lt.Action != model.Replace &&
		(version == "$Default" && !moves || known && version != "$Latest" && version != "$Default" && !c.Unknown(asg, "launch_template"))
	var f *model.Finding
	switch {
	case stuck:
		which := "version " + version
		why := ""
		if version == "$Default" {
			which, why = "its default version", " (update_default_version is off)"
		}
		f = finding(asg, "", fmt.Sprintf("%s changes, but %s launches %s, which this change does not move%s, so no instance gets the new template, not even new ones.",
			Code(lt.Address), Code(asg.Address), which, why))
		f.Severity = model.Medium
		f.Remediation = "Point the group at the template's latest_version (or \"$Latest\"), or set update_default_version = true on the template, and add an instance_refresh block to roll it out."
	default:
		f = finding(asg, "", fmt.Sprintf("%s changes, but %s has no instance_refresh, so its running instances keep the old template; only instances it launches from now on get the new one.",
			Code(lt.Address), Code(asg.Address)))
		f.Remediation = "Add an instance_refresh block to the group so a template change rolls through its instances, or start an instance refresh after the apply."
	}
	f.Addresses = []string{asg.Address, lt.Address}
	f.Short = "keeps running instances on the old template"
	return f
}
