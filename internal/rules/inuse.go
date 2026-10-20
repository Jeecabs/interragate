package rules

import (
	"fmt"

	"github.com/Jeecabs/interragate/internal/model"
)

// inUseRules flag deleting what the plan still uses.
func inUseRules() []*Rule {
	return []*Rule{
		{
			ID: "replace-in-use", Title: "Apply will fail: it is deleted while still in use", Severity: model.High, Category: Correctness,
			Doc: "Flags replacing a security group, target group, certificate or database subnet or parameter group by deleting it first, while resources the plan keeps still use it. " +
				"They only switch to the new one once it exists, and AWS refuses to delete one in use.",
			Check: checkReplaceInUse,
		},
	}
}

// inUse says how AWS stops a resource being deleted while something uses it.
type inUse struct {
	noun    string
	id      string   // the attribute that identifies it to what uses it
	holders []string // types whose use blocks the delete; none means any type not in notHolders
	err     string   // the error AWS returns
	note    string   // what the provider does about it, if worth saying
}

var (
	listeners = []string{"aws_lb_listener", "aws_alb_listener", "aws_lb_listener_rule", "aws_alb_listener_rule"}
	inUseKB   = map[string]inUse{
		"aws_security_group": {noun: "security group", id: "id", err: "DependencyViolation",
			note: "the provider keeps trying for up to 15 minutes, its delete timeout, before giving up"},
		"aws_lb_target_group":  {noun: "target group", id: "arn", holders: listeners, err: "ResourceInUse"},
		"aws_alb_target_group": {noun: "target group", id: "arn", holders: listeners, err: "ResourceInUse"},
		"aws_acm_certificate": {noun: "certificate", id: "arn", err: "ResourceInUseException", holders: append([]string{"aws_lb_listener_certificate", "aws_cloudfront_distribution",
			"aws_api_gateway_domain_name", "aws_apigatewayv2_domain_name"}, listeners...)},
		"aws_db_subnet_group":             {noun: "DB subnet group", id: "name", holders: []string{"aws_db_instance", "aws_rds_cluster"}, err: "InvalidDBSubnetGroupStateFault"},
		"aws_db_parameter_group":          {noun: "DB parameter group", id: "name", holders: []string{"aws_db_instance"}, err: "InvalidDBParameterGroupState"},
		"aws_rds_cluster_parameter_group": {noun: "cluster parameter group", id: "name", holders: []string{"aws_rds_cluster"}, err: "InvalidDBParameterGroupState"},
		"aws_elasticache_subnet_group":    {noun: "cache subnet group", id: "name", holders: []string{"aws_elasticache_cluster", "aws_elasticache_replication_group"}, err: "CacheSubnetGroupInUse"},
	}
	// notHolders name a security group without putting anything in it, so
	// they do not stop it being deleted.
	notHolders = map[string]bool{"aws_launch_template": true, "aws_launch_configuration": true, "aws_ssm_parameter": true}
)

func checkReplaceInUse(c *Context) []*model.Finding {
	type replaced struct {
		ch    *model.Change
		use   inUse
		ident string
	}
	var deleted []replaced
	idents := map[string]bool{}
	for _, ch := range c.Changes(model.Replace) {
		use, ok := inUseKB[ch.Type]
		if !ok || !Managed(ch) || ch.Deposed != "" || ch.Order == "create-first" {
			continue
		}
		if ident := String(c.Before(ch), use.id); ident != "" {
			deleted = append(deleted, replaced{ch, use, ident})
			idents[ident] = true
		}
	}
	if len(deleted) == 0 {
		return nil
	}
	// What still uses each of them, read once from every resource rather
	// than once for each one replaced: a plan can replace thousands.
	usedBy := map[string][]*model.Change{}
	for _, o := range c.Report.Changes {
		if !Managed(o) || !stillThere(o) {
			continue
		}
		seen := map[string]bool{}
		eachString(c.Before(o), func(s string) {
			if idents[s] && !seen[s] {
				seen[s] = true
				usedBy[s] = append(usedBy[s], o)
			}
		})
	}
	var out []*model.Finding
	for _, d := range deleted {
		ch, use := d.ch, d.use
		var users []string
		for _, o := range usedBy[d.ident] {
			if o != ch && holds(use, o.Type) {
				users = append(users, o.Address)
			}
		}
		if len(users) == 0 {
			continue
		}
		msg := fmt.Sprintf("Replacing %s deletes the %s before creating the new one, but %s still %s it, and only %s to the new one once it exists. "+
			"AWS refuses to delete a %s in use (%s), so the apply fails here",
			Code(ch.Address), use.noun, codeList(users), map[bool]string{true: "uses", false: "use"}[len(users) == 1],
			map[bool]string{true: "switches", false: "switch"}[len(users) == 1], use.noun, use.err)
		if use.note != "" {
			msg += "; " + use.note
		}
		f := finding(ch, "Apply will fail: it is deleted while still in use", msg+".")
		f.Addresses = append(f.Addresses, users...)
		f.Short = "will fail on a resource still in use"
		f.Fails = "destroy"
		f.Remediation = fmt.Sprintf("Add lifecycle { create_before_destroy = true } to %s, so the new %s exists and takes over before the old one goes.", ch.Resource, use.noun)
		if !c.Unknown(ch, "name") && String(c.After(ch), "name") != "" {
			f.Remediation += " Use name_prefix rather than name, since both exist for a moment and names must be unique."
		}
		out = append(out, f)
	}
	return out
}

// stillThere reports whether a resource exists, unchanged or updated, when
// the replaced one is deleted. Terraform destroys a dependent it destroys
// or replaces first, before what it depends on.
func stillThere(o *model.Change) bool {
	switch o.Action {
	case model.NoOp, model.Update:
		return true
	case model.Replace:
		return o.Order == "create-first"
	}
	return false
}

func holds(use inUse, typ string) bool {
	if len(use.holders) == 0 {
		return !notHolders[typ]
	}
	for _, h := range use.holders {
		if h == typ {
			return true
		}
	}
	return false
}

// eachString calls fn with every string in v.
func eachString(v any, fn func(string)) {
	switch x := v.(type) {
	case string:
		fn(x)
	case map[string]any:
		for _, e := range x {
			eachString(e, fn)
		}
	case []any:
		for _, e := range x {
			eachString(e, fn)
		}
	}
}
