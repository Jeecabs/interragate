package rules

import (
	"fmt"
	"strings"

	"github.com/Jeecabs/interragate/internal/kb"
	"github.com/Jeecabs/interragate/internal/model"
)

// dataRules flag destroying or recreating what holds data, and turning off
// what would save it.
func dataRules() []*Rule {
	return []*Rule{
		{
			ID: "stateful-destroy", Title: "Destroys a resource that holds data", Severity: model.Critical, Category: DataLoss,
			Doc: "Flags destroying databases, buckets, volumes, keys, queues, DNS zones and other resources whose contents cannot be recreated from configuration. " +
				"The message says what is lost and whether a final snapshot, recovery window or force_destroy changes the picture.",
			Check: checkStatefulDestroy,
		},
		{
			ID: "stateful-replace", Title: "Replaces a resource that holds data", Severity: model.Critical, Category: DataLoss,
			Doc: "Flags replacing a resource that holds data. Replacement destroys the existing object and creates a new, empty one, " +
				"which is easy to miss in a long plan because it reads like an update.",
			Check: checkStatefulReplace,
		},
		{
			ID: "protected-delete", Title: "Apply will fail: deletion protection is on", Severity: model.High, Category: Correctness,
			Doc: "Flags destroying or replacing an object that still has deletion protection turned on. Terraform destroys the old object " +
				"with its current settings, so turning protection off in the same plan does not help: the apply fails partway.",
			Check: checkProtectedDelete,
		},
		{
			ID: "import-replace", Title: "Imports a resource and immediately replaces it", Severity: model.High, Category: Correctness,
			Doc: "Flags an import block whose configuration differs from the real object in an attribute that cannot change in place, " +
				"so the plan destroys the very object being imported.",
			Check: checkImportReplace,
		},
		{
			ID: "final-snapshot-disabled", Title: "Final snapshot disabled", Severity: model.Medium, Category: DataLoss,
			Doc:   "Flags setting skip_final_snapshot = true, which means a future destroy deletes the data with no backup.",
			Check: checkFinalSnapshotDisabled,
		},
		{
			ID: "deletion-protection-disabled", Title: "Deletion protection turned off", Severity: model.Medium, Category: DataLoss,
			Doc:   "Flags turning deletion protection off, which is often the first step before an accidental destroy.",
			Check: checkProtectionDisabled,
		},
		{
			ID: "force-destroy-enabled", Title: "force_destroy turned on", Severity: model.Medium, Category: DataLoss,
			Doc:   "Flags turning on force_destroy (or force_delete), which lets a destroy delete every object or image inside instead of failing.",
			Check: checkForceDestroy,
		},
		{
			ID: "mass-destroy", Title: "Destroys many resources", Severity: model.High, Category: DataLoss,
			Doc: "Flags plans that destroy many resources at once (20 by default, or half of everything managed). " +
				"This is how a wrong workspace, a missing variable file or a refactor without moved blocks usually shows up. " +
				"Configure with rules.mass-destroy.threshold.",
			Check: checkMassDestroy,
		},
	}
}

func checkStatefulDestroy(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Delete) {
		info := c.Info(ch)
		if !Managed(ch) || !info.Stateful {
			continue
		}
		before := c.Before(ch)
		sev := model.Critical
		var notes []string
		if note, safe := snapshotNote(info, before); note != "" {
			notes = append(notes, note)
			if safe {
				sev = model.High
			}
		}
		if note, empties := forceDestroyNote(info, before); note != "" {
			notes = append(notes, note)
			if !empties {
				sev = model.High
			}
		}
		if note, recoverable := recoveryNote(ch, before); note != "" {
			notes = append(notes, note)
			if recoverable {
				sev = model.High
			}
		}
		msg := fmt.Sprintf("Destroying %s (%s) deletes %s.", Code(ch.Address), Article(info.Noun), info.Loss)
		if ch.Deposed != "" {
			msg = fmt.Sprintf("Destroying %s deletes %s. It is the previous %s, left behind when a create-before-destroy replacement did not finish, so it may still hold the only copy of the data.", Code(ch.Address), info.Loss, info.Noun)
		} else if ch.Why != "" {
			msg += " " + ch.Why
		}
		if len(notes) > 0 {
			msg += " " + strings.Join(notes, " ")
		}
		f := finding(ch, "Destroys "+Article(info.Noun), msg)
		f.Severity = sev
		f.Short = "destroys " + Article(info.Noun)
		f.Remediation = "If you only want Terraform to stop managing it, use a removed block with destroy = false instead. " +
			"If it should be destroyed, take a backup first. Add lifecycle { prevent_destroy = true } to guard against accidents."
		if ch.Reason == "delete_because_no_resource_config" || ch.Reason == "delete_because_no_module" {
			f.Snippet = fmt.Sprintf("removed {\n  from = %s\n\n  lifecycle {\n    destroy = false\n  }\n}", ch.Resource)
		}
		out = append(out, f)
	}
	return out
}

func checkStatefulReplace(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Replace) {
		info := c.Info(ch)
		if !Managed(ch) || !info.Stateful || ch.Importing {
			continue
		}
		msg := fmt.Sprintf("Replacing %s (%s) destroys it, along with %s, and creates a new, empty one.", Code(ch.Address), Article(info.Noun), info.Loss)
		switch {
		case ch.Indirect:
			msg += " " + ch.Why
		case len(ch.ForcedBy) > 0 && ch.Reason == "replace_because_cannot_update":
			msg += " It is forced by the change to " + codeList(ch.ForcedBy) + "."
		case ch.Why != "":
			msg += " " + firstSentence(ch.Why)
		}
		if note, _ := snapshotNote(info, c.Before(ch)); note != "" {
			msg += " " + note
		}
		if ch.Order == "create-first" {
			msg += " The new one is created before the old one is destroyed."
		}
		f := finding(ch, "Replaces "+Article(info.Noun), msg)
		f.Short = "replaces " + Article(info.Noun)
		f.Paths = ch.ForcedBy
		switch ch.Reason {
		case "replace_because_tainted":
			f.Remediation = fmt.Sprintf("If the object is healthy, run `%s untaint %s` to keep it.", c.Report.Plan.Command(), ch.Address)
		case "replace_by_request":
			f.Remediation = fmt.Sprintf("Drop `-replace=%s` unless you meant to recreate it.", ch.Address)
		case "replace_by_triggers":
			f.Remediation = "Check replace_triggered_by on this resource: any change to what it lists recreates it."
		default:
			// The plan does not always say what forces a replacement, as
			// when it follows from an upstream change.
			what := "the change that forces it"
			if len(ch.ForcedBy) > 0 {
				what = "the change to " + codeList(ch.ForcedBy)
			}
			f.Remediation = "If you did not mean to recreate it, revert " + what +
				". If you did, back it up first and plan for the new object to start empty (for a database, restore it from a snapshot)."
		}
		out = append(out, f)
	}
	return out
}

func checkProtectedDelete(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Delete, model.Replace) {
		if !Managed(ch) {
			continue
		}
		info := c.Info(ch)
		before, after := c.Before(ch), c.After(ch)
		for _, attr := range info.Protection {
			v, ok := Attr(before, attr)
			if !ok || !kb.IsProtected(v) {
				continue
			}
			verb := "destroy"
			if ch.Action == model.Replace {
				verb = "replace"
			}
			msg := fmt.Sprintf("The apply will fail when it tries to %s %s: %s is still on for the existing %s.", verb, Code(ch.Address), Code(attr), info.Noun)
			if av, ok := Attr(after, attr); ok && !kb.IsProtected(av) {
				msg += fmt.Sprintf(" Turning it off in this same plan does not help, because %s destroys the old object with its current settings.", c.Report.Plan.ToolName())
			}
			f := finding(ch, "Apply will fail: deletion protection is on", msg)
			f.Short = "will fail on deletion protection"
			f.Fails = "destroy"
			f.Paths = []string{attr}
			f.Remediation = fmt.Sprintf("Apply the change that turns %s off on its own first, then apply this plan.", attr)
			out = append(out, f)
			break
		}
	}
	return out
}

func checkImportReplace(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Replace) {
		if !ch.Importing {
			continue
		}
		msg := fmt.Sprintf("Importing %s and then replacing it destroys the object you are importing.", Code(ch.Address))
		if len(ch.ForcedBy) > 0 {
			msg += " The configuration differs from it in " + codeList(ch.ForcedBy) + ", which cannot change in place."
		}
		f := finding(ch, "Import is immediately replaced", msg)
		f.Short = "destroys the resource it imports"
		f.Paths = ch.ForcedBy
		f.Remediation = "Make the configuration match the existing object so the import plans no changes, then change it deliberately in a later apply."
		out = append(out, f)
	}
	return out
}

func checkFinalSnapshotDisabled(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Create, model.Update, model.Replace) {
		info := c.Info(ch)
		if info.Snapshot == nil || info.Snapshot.Skip == "" || !Managed(ch) {
			continue
		}
		skip, ok := Bool(c.After(ch), info.Snapshot.Skip)
		if !ok || !skip {
			continue
		}
		if was, ok := Bool(c.Before(ch), info.Snapshot.Skip); ok && was && ch.Action != model.Create {
			continue
		}
		f := finding(ch, "Final snapshot disabled", fmt.Sprintf("Sets %s on %s: if it is ever destroyed, no final snapshot is taken.", Code(info.Snapshot.Skip+" = true"), Code(ch.Address)))
		if ch.Action == model.Create {
			f.Severity = model.Low
		}
		f.Short = "disables the final snapshot"
		f.Paths = []string{info.Snapshot.Skip}
		f.Remediation = fmt.Sprintf("Set %s = false and give %s a name, unless the data is disposable.", info.Snapshot.Skip, info.Snapshot.Name)
		out = append(out, f)
	}
	return out
}

func checkProtectionDisabled(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Update) {
		info := c.Info(ch)
		for _, attr := range info.Protection {
			bv, ok1 := Attr(c.Before(ch), attr)
			av, ok2 := Attr(c.After(ch), attr)
			if !ok1 || !ok2 || !kb.IsProtected(bv) || kb.IsProtected(av) {
				continue
			}
			f := finding(ch, "Deletion protection turned off", fmt.Sprintf("Turns %s off on %s, so a later apply can destroy it.", Code(attr), Code(ch.Address)))
			f.Short = "turns off deletion protection"
			f.Paths = []string{attr}
			f.Remediation = "Turn protection off only in the change that destroys the resource, and turn it back on if that change is abandoned."
			out = append(out, f)
		}
	}
	return out
}

func checkForceDestroy(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Create, model.Update) {
		info := c.Info(ch)
		if info.ForceDestroy == "" {
			continue
		}
		on, ok := Bool(c.After(ch), info.ForceDestroy)
		if !ok || !on {
			continue
		}
		if was, _ := Bool(c.Before(ch), info.ForceDestroy); was {
			continue
		}
		f := finding(ch, "force_destroy turned on", fmt.Sprintf("%s is on for %s: destroying it later also deletes %s, instead of failing.", Code(info.ForceDestroy), Code(ch.Address), info.Loss))
		if ch.Action == model.Create {
			f.Severity = model.Low
		}
		f.Short = "turns on " + info.ForceDestroy
		f.Paths = []string{info.ForceDestroy}
		f.Remediation = "Leave it off for anything holding data you care about; turn it on only in the change that removes the resource."
		out = append(out, f)
	}
	return out
}

func checkMassDestroy(c *Context) []*model.Finding {
	threshold := c.Setting("mass-destroy", "threshold", 20)
	managed, destroyed := 0, 0
	var addrs []string
	types := map[string]int{}
	for _, ch := range c.Report.Changes {
		if !Managed(ch) || ch.Category == string(kb.Utility) {
			continue
		}
		managed++
		if ch.Action == model.Delete || ch.Action == model.Replace {
			destroyed++
			addrs = append(addrs, ch.Address)
			types[ch.Type]++
		}
	}
	if destroyed < threshold && !(managed >= 6 && destroyed*2 >= managed) {
		return nil
	}
	var parts []string
	for _, t := range sortedByCount(types) {
		parts = append(parts, fmt.Sprintf("%d %s", types[t], t))
	}
	msg := fmt.Sprintf("Destroys %d of %d managed resources (counting replacements): %s.", destroyed, managed, List(parts, 5))
	if c.Report.Plan.Destroy {
		msg += " This is a destroy plan."
	}
	return []*model.Finding{{
		Title:       "Destroys many resources",
		Short:       fmt.Sprintf("destroys %d resources", destroyed),
		Message:     msg,
		Addresses:   addrs,
		Remediation: "Check that this is the right workspace and directory, that every variable file was passed, and that a refactor is not missing moved blocks.",
	}}
}

// snapshotNote describes whether destroying the object takes a final
// snapshot. safe means the data can be restored afterwards.
func snapshotNote(info kb.Info, before map[string]any) (note string, safe bool) {
	s := info.Snapshot
	if s == nil || before == nil {
		return "", false
	}
	name := String(before, s.Name)
	if s.Skip != "" {
		skip, ok := Bool(before, s.Skip)
		if !ok {
			return "", false
		}
		if skip {
			return fmt.Sprintf("No final snapshot is taken (%s = true).", Code(s.Skip)), false
		}
		if s.Name == "" {
			return "A final backup is taken first.", true
		}
		if name == "" {
			return fmt.Sprintf("%s is false but %s is empty, so the destroy will fail.", Code(s.Skip), Code(s.Name)), true
		}
		return fmt.Sprintf("A final snapshot (%s) is taken first, so the data can be restored.", Code(name)), true
	}
	if name != "" {
		return fmt.Sprintf("A final snapshot (%s) is taken first.", Code(name)), true
	}
	return "No final snapshot is configured.", false
}

// forceDestroyNote describes how force_destroy changes a destroy. empties is
// false when the destroy would fail on a non-empty object instead.
func forceDestroyNote(info kb.Info, before map[string]any) (note string, empties bool) {
	if info.ForceDestroy == "" || before == nil {
		return "", true
	}
	on, ok := Bool(before, info.ForceDestroy)
	if !ok {
		return "", true
	}
	if on {
		return fmt.Sprintf("%s is on, so everything in it is deleted too.", Code(info.ForceDestroy)), true
	}
	return fmt.Sprintf("%s is off, so the destroy fails if it is not empty.", Code(info.ForceDestroy)), false
}

// recoveryNote covers objects with a deletion window.
func recoveryNote(ch *model.Change, before map[string]any) (note string, recoverable bool) {
	switch ch.Type {
	case "aws_kms_key", "aws_kms_replica_key":
		if days, ok := Attr(before, "deletion_window_in_days"); ok && days != nil {
			return fmt.Sprintf("AWS schedules the key for deletion in %v days; until then it can be recovered with CancelKeyDeletion.", days), true
		}
		return "AWS schedules the key for deletion after a waiting period (30 days unless configured); until then it can be recovered with CancelKeyDeletion.", true
	case "aws_secretsmanager_secret":
		if days, ok := Attr(before, "recovery_window_in_days"); ok && days != nil {
			if fmt.Sprint(days) == "0" {
				return "recovery_window_in_days is 0, so the secret is deleted immediately with no way back.", false
			}
			return fmt.Sprintf("It can be restored for %v days.", days), true
		}
	}
	return "", false
}

func codeList(items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = Code(s)
	}
	return List(out, 4)
}

func firstSentence(s string) string {
	inCode := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '`':
			inCode = !inCode
		case '.':
			if !inCode && (i+1 == len(s) || s[i+1] == ' ') {
				return s[:i+1]
			}
		}
	}
	return s
}

func sortedByCount(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && (m[keys[j]] > m[keys[j-1]] || (m[keys[j]] == m[keys[j-1]] && keys[j] < keys[j-1])); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
