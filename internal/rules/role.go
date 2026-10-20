package rules

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
)

// roleRules flag deleting an IAM role that still has policies attached, and
// recreating one that others refer to.
func roleRules() []*Rule {
	return []*Rule{
		{
			ID: "role-delete-conflict", Title: "Apply will fail: the role still has policies attached", Severity: model.High, Category: Correctness,
			Doc: "Flags replacing an IAM role under the same name while policy attachments or inline policies the plan leaves alone still name it. " +
				"The replacement deletes the role first, and AWS refuses to delete a role with policies attached unless force_detach_policies is set.",
			Check: checkRoleDeleteConflict,
		},
		{
			ID: "role-recreated", Title: "Recreating a role breaks what refers to it", Severity: model.High, Category: Availability,
			Doc: "Flags replacing an IAM role while resources the plan leaves alone keep referring to the old one: instance profiles, which AWS empties when it deletes the role; " +
				"policies the provider detaches and nothing attaches again; and policies that trust the role by ARN, which AWS ties to the old role's unique ID.",
			Check: checkRoleRecreated,
		},
	}
}

// recreated is an IAM role the plan deletes and creates again.
type recreated struct {
	ch          *model.Change
	name, arn   string
	sameName    bool // the new role has the old one's name, so its ARN
	forceDetach bool // the provider detaches policies before deleting the role
}

// recreatedRoles lists the roles the plan replaces by deleting them first.
// One created first cannot keep its name, which name-taken reports.
func recreatedRoles(c *Context) []recreated {
	var out []recreated
	for _, ch := range c.Changes(model.Replace) {
		if ch.Type != "aws_iam_role" || !Managed(ch) || ch.Deposed != "" || ch.Order == "create-first" {
			continue
		}
		before, after := c.Before(ch), c.After(ch)
		name := String(before, "name")
		if name == "" {
			continue
		}
		forceDetach, _ := Bool(before, "force_detach_policies")
		out = append(out, recreated{
			ch: ch, name: name, arn: String(before, "arn"), forceDetach: forceDetach,
			sameName: !c.Unknown(ch, "name") && String(after, "name") == name && String(after, "path") == String(before, "path"),
		})
	}
	return out
}

// kept reports whether the plan leaves attr of ch as it is, so the apply
// makes no call that would set it again.
func (c *Context) kept(ch *model.Change, attr string) bool {
	switch ch.Action {
	case model.NoOp:
		return true
	case model.Update:
		return !c.Unknown(ch, attr) && diff.Equal(c.Before(ch)[attr], c.After(ch)[attr])
	}
	return false
}

// attachedTo lists the policy attachments and inline policies the plan
// leaves naming the role.
func attachedTo(c *Context, r recreated) []*model.Change {
	var out []*model.Change
	for _, o := range c.Changes(model.NoOp, model.Update) {
		switch o.Type {
		case "aws_iam_role_policy_attachment", "aws_iam_role_policy":
			if String(c.Before(o), "role") == r.name && c.kept(o, "role") {
				out = append(out, o)
			}
		case "aws_iam_policy_attachment":
			if slices.Contains(Strings(c.Before(o)["roles"]), r.name) && c.kept(o, "roles") {
				out = append(out, o)
			}
		}
	}
	return out
}

func checkRoleDeleteConflict(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, r := range recreatedRoles(c) {
		if !r.sameName || r.forceDetach {
			continue
		}
		attached := attachedTo(c, r)
		if len(attached) == 0 {
			continue
		}
		f := finding(r.ch, "Apply will fail: the role still has policies attached", fmt.Sprintf(
			"Replacing %s deletes the role first, but %s %s attached to it, and this plan leaves %s in place. "+
				"AWS refuses to delete a role that has policies attached (DeleteConflict), so the apply stops there.",
			Code(r.ch.Address), codeList(addresses(attached)), isAre(len(attached)), itThem(len(attached))))
		f.Addresses = append(f.Addresses, addresses(attached)...)
		f.Short = "will fail on a role with policies attached"
		f.Fails = "destroy"
		f.Remediation = "If the role must be recreated, set force_detach_policies = true on it and add " +
			"lifecycle { replace_triggered_by = [" + r.ch.Resource + "] } to the attachments, so they are attached again to the new role. " +
			"Otherwise, find what forces the replacement and avoid it."
		out = append(out, f)
	}
	return out
}

func checkRoleRecreated(c *Context) []*model.Finding {
	var out []*model.Finding
	var quiet []string // roles nothing here refers to; said once for all of them
	roles := recreatedRoles(c)
	kept := keptPolicies(c, roles)
	for _, r := range roles {
		var broken []string
		var addrs []string
		if r.sameName {
			var profiles []*model.Change
			for _, o := range c.Changes(model.NoOp, model.Update) {
				if o.Type == "aws_iam_instance_profile" && String(c.Before(o), "role") == r.name && c.kept(o, "role") {
					profiles = append(profiles, o)
				}
			}
			if len(profiles) > 0 {
				broken = append(broken, fmt.Sprintf("Deleting the role removes it from %s %s, and this plan does not add it back, so whatever runs with %s has no role and no credentials until the next apply.",
					countedNoun(len(profiles), "instance profile", "instance profiles"), codeList(addresses(profiles)), itThem(len(profiles))))
				addrs = append(addrs, addresses(profiles)...)
			}
			if attached := attachedTo(c, r); r.forceDetach && len(attached) > 0 {
				broken = append(broken, fmt.Sprintf("force_detach_policies detaches %s before the role is deleted, and this plan does not attach %s again, so the new role starts without %s permissions.",
					codeList(addresses(attached)), itThem(len(attached)), itsTheir(len(attached))))
				addrs = append(addrs, addresses(attached)...)
			}
		}
		if trusting := trustsByARN(kept, r); len(trusting) > 0 {
			n := len(trusting)
			policies := map[bool]string{true: "The policy of %s names", false: "The policies of %s name"}[n == 1]
			if r.sameName {
				broken = append(broken, fmt.Sprintf(policies+" the role as a principal, and AWS saved the old role's unique ID there rather than its ARN, so %s will not trust the new role, though its ARN is the same, until %s saved again; this plan leaves %s as %s.",
					codeList(addresses(trusting)), itThey(n), itIsTheyAre(n), itThem(n), itIsTheyAre(n)))
			} else {
				broken = append(broken, fmt.Sprintf(policies+" the old role's ARN as a principal, and this plan leaves %s as %s, so %s a role that no longer exists.",
					codeList(addresses(trusting)), itThem(n), itIsTheyAre(n), map[bool]string{true: "it trusts", false: "they trust"}[n == 1]))
			}
			addrs = append(addrs, addresses(trusting)...)
		}
		if len(broken) == 0 {
			quiet = append(quiet, r.ch.Address)
			continue
		}
		f := finding(r.ch, "Recreating a role breaks what refers to it", fmt.Sprintf("Replacing %s deletes the role and creates another. %s", Code(r.ch.Address), strings.Join(broken, " ")))
		f.Addresses = append(f.Addresses, addrs...)
		f.Short = "breaks what refers to a recreated role"
		f.Remediation = "Point those resources at the role's id or arn attribute rather than its name or a written-out ARN: neither is known until the new role exists, " +
			"so this apply updates them after creating it. Or add lifecycle { replace_triggered_by = [" + r.ch.Resource + "] } to them. " +
			"Applying twice also repairs them, after an outage in between."
		out = append(out, f)
	}
	if len(quiet) > 0 {
		// Nothing in this configuration refers to them, but policies
		// elsewhere may trust them, and those cannot be seen from here.
		what := fmt.Sprintf("Replacing %s deletes the role and creates another. A policy outside this configuration that trusts it", Code(quiet[0]))
		if len(quiet) > 1 {
			what = fmt.Sprintf("Replacing %s deletes %d roles and creates others. A policy outside this configuration that trusts one of them", codeList(quiet), len(quiet))
		}
		f := &model.Finding{Title: "Recreating a role breaks what refers to it", Addresses: quiet, Severity: model.Low,
			Message:     what + " by ARN (another account's role, a bucket or key policy) holds the old role's unique ID, and will not trust the new role until it is saved again.",
			Short:       "recreates " + countedNoun(len(quiet), "an IAM role", "IAM roles"),
			Remediation: "After the apply, save again any policy outside this configuration that names a recreated role, or avoid the replacement."}
		out = append(out, f)
	}
	return out
}

// keptPolicy is a resource's policy statements that the plan leaves as
// they are, with the principals they name.
type keptPolicy struct {
	ch         *model.Change
	principals map[string]bool
}

// keptPolicies reads, once, the policies the plan leaves unchanged, for
// every recreated role to look itself up in.
func keptPolicies(c *Context, roles []recreated) []keptPolicy {
	if len(roles) == 0 {
		return nil
	}
	var out []keptPolicy
	for _, o := range c.Changes(model.NoOp, model.Update) {
		if !Managed(o) || o.Type == "aws_iam_role" && slices.ContainsFunc(roles, func(r recreated) bool { return r.ch == o }) {
			continue
		}
		before := policyStatements(c.Before(o))
		if len(before) == 0 {
			continue
		}
		after := policyStatements(c.After(o))
		k := keptPolicy{ch: o, principals: map[string]bool{}}
		for path, stmts := range before {
			if !sameStatements(stmts, after[path]) {
				continue // saved again by this apply, which ties it to the new role
			}
			for _, s := range stmts {
				if p, ok := s.raw["Principal"].(map[string]any); ok {
					for _, arn := range Strings(p["AWS"]) {
						k.principals[arn] = true
					}
				}
			}
		}
		if len(k.principals) > 0 {
			out = append(out, k)
		}
	}
	return out
}

// trustsByARN lists the resources whose kept policies name the role's ARN
// as a principal.
func trustsByARN(kept []keptPolicy, r recreated) []*model.Change {
	if r.arn == "" {
		return nil
	}
	var out []*model.Change
	for _, k := range kept {
		if k.principals[r.arn] {
			out = append(out, k.ch)
		}
	}
	return out
}

func sameStatements(a, b []statement) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !diff.Equal(a[i].raw, b[i].raw) {
			return false
		}
	}
	return true
}

func addresses(chs []*model.Change) []string {
	out := make([]string, len(chs))
	for i, ch := range chs {
		out[i] = ch.Address
	}
	return out
}

func itThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func itThey(n int) string {
	if n == 1 {
		return "it"
	}
	return "they"
}

func itIsTheyAre(n int) string {
	if n == 1 {
		return "it is"
	}
	return "they are"
}

func itsTheir(n int) string {
	if n == 1 {
		return "its"
	}
	return "their"
}

// countedNoun is one alone, or many with the count: "instance profile",
// "2 instance profiles".
func countedNoun(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return fmt.Sprintf("%d %s", n, many)
}
