package analysis

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/rules"
	"github.com/Jeecabs/interragate/internal/source"
)

type sourceBlock = source.Block

// why explains, in a sentence, why the plan does what it does to a resource.
// Causes are added later, once every change is known.
func (a *analyzer) why(ch *model.Change, destroyPlan bool) string {
	switch ch.Action {
	case model.Create:
		if ch.Importing {
			return "Imported from " + rules.Code(ch.ImportID) + " with no changes."
		}
		if ch.Moved {
			return "Moved from " + rules.Code(ch.PrevAddress) + "."
		}
		return "New in the configuration."
	case model.Update:
		s := "Updated in place."
		if attrs := a.changedRoots(ch); len(attrs) > 0 {
			s = "Updates " + codes(attrs) + " in place."
		}
		if ch.Importing {
			s = "Imported from " + rules.Code(ch.ImportID) + ", then updated in place to match the configuration."
		}
		if ch.Moved {
			s += " Moved from " + rules.Code(ch.PrevAddress) + "."
		}
		if r := resizeSentence(ch, a.full[ch]); r != "" {
			s += " " + r
		}
		return s
	case model.Replace:
		var s string
		switch ch.Reason {
		case "replace_because_tainted":
			s = "Tainted: a previous apply failed partway through creating it, or someone ran `" + a.info.Command() + " taint`."
		case "replace_by_request":
			s = "Replacement was requested with `-replace`."
		case "replace_by_triggers":
			s = "A resource listed in `replace_triggered_by` changed."
			if b := a.block(ch); b != nil && len(b.Lifecycle.ReplaceTriggeredBy) > 0 {
				s = "Triggered by a change to " + codes(b.Lifecycle.ReplaceTriggeredBy) + " (`replace_triggered_by`)."
			}
		default:
			if len(ch.ForcedBy) > 0 {
				s = "Changing " + codes(ch.ForcedBy) + " cannot be done in place."
			} else {
				s = "The provider cannot update it in place."
			}
		}
		switch ch.Order {
		case "create-first":
			s += " The replacement is created before the old one is destroyed."
		case "delete-first":
			s += " The old one is destroyed before the replacement is created."
		}
		if ch.Importing {
			s = "Imported from " + rules.Code(ch.ImportID) + ", then replaced. " + s
		}
		return s
	case model.Delete:
		if ch.Deposed != "" {
			return "A deposed object left behind by an earlier create-before-destroy replacement that did not finish."
		}
		switch ch.Reason {
		case "delete_because_no_resource_config":
			return "Its block was removed from the configuration."
		case "delete_because_no_module":
			return "Its module was removed from the configuration."
		case "delete_because_wrong_repetition":
			return "The resource switched between count and for_each (or stopped using them), so this instance key no longer exists."
		case "delete_because_count_index":
			return "`count` no longer reaches index " + ch.Key + "."
		case "delete_because_each_key":
			return "`for_each` no longer contains the key " + strings.Trim(ch.Key, "[]") + "."
		case "delete_because_no_move_target":
			return "It was moved to an address that does not exist in the configuration."
		}
		if destroyPlan {
			return "This is a destroy plan."
		}
		return "Planned for destruction."
	case model.Read:
		switch ch.Reason {
		case "read_because_config_unknown":
			return "Read during apply: its arguments depend on values that are only known after apply."
		case "read_because_dependency_pending":
			return "Read during apply: it depends on resources with changes pending."
		case "read_because_check_nested":
			return "Read during apply because a check block uses it."
		}
		return "Read during apply."
	case model.Forget:
		return "Removed from state but not destroyed (a `removed` block with `destroy = false`). The real object keeps running, unmanaged."
	case model.NoOp:
		if ch.Importing {
			return "Imported from " + rules.Code(ch.ImportID) + " with no changes."
		}
		if ch.Moved {
			return "Moved from " + rules.Code(ch.PrevAddress) + " with no other changes."
		}
		return "No changes."
	}
	return ""
}

// changedRoots lists the top-level attributes an update changes, ignoring
// attributes that only become unknown.
func (a *analyzer) changedRoots(ch *model.Change) []string {
	seen := map[string]bool{}
	configured := a.configured(ch.Address)
	var out []string
	for _, l := range a.full[ch].Leaves() {
		root := l.Root()
		if root == "" || seen[root] || l.Trivial || (l.Unknown && !configured[root]) {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	if seen["tags"] && seen["tags_all"] {
		out = slices.DeleteFunc(out, func(s string) bool { return s == "tags_all" })
	}
	return out
}

func (a *analyzer) block(ch *model.Change) *sourceBlock {
	if a.src == nil {
		return nil
	}
	local := ch.Type + "." + ch.Name
	if ch.Mode == "data" {
		local = "data." + local
	}
	module := moduleConfig(ch.Module)
	return a.src.Block(module, local)
}

func codes(items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = rules.Code(s)
	}
	return rules.List(out, 4)
}

// moduleConfig strips instance keys from a module path.
func moduleConfig(module string) string {
	if !strings.Contains(module, "[") {
		return module
	}
	var b strings.Builder
	depth := 0
	inString := false
	for i := 0; i < len(module); i++ {
		c := module[i]
		switch {
		case inString:
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
		case c == '"' && depth > 0:
			inString = true
		case c == '[':
			depth++
		case c == ']':
			depth--
		case depth == 0:
			b.WriteByte(c)
		}
	}
	return b.String()
}

var shortReasons = map[string]string{
	"delete_because_no_resource_config": "removed from the configuration",
	"delete_because_no_module":          "module removed",
	"delete_because_wrong_repetition":   "switched between count and for_each",
	"delete_because_count_index":        "count reduced",
	"delete_because_each_key":           "key removed from for_each",
	"delete_because_no_move_target":     "moved to an address that does not exist",
	"replace_because_tainted":           "tainted",
	"replace_by_request":                "replacement requested with -replace",
	"replace_by_triggers":               "replace_triggered_by",
	"read_because_config_unknown":       "arguments known only after apply",
	"read_because_dependency_pending":   "depends on pending changes",
}

// summaryLine describes a change in a few words, for lists.
func summaryLine(ch *model.Change) string {
	switch {
	case len(ch.Deploys) > 0:
		line := "deploys " + deployWords(ch.Deploys[0], false)
		if n := len(ch.Deploys) - 1; n > 0 {
			line += fmt.Sprintf(" and %d more", n)
		}
		return line
	case ch.Indirect:
		var names []string
		seen := map[string]bool{}
		for _, c := range ch.Causes {
			if !seen[c.Resource] {
				seen[c.Resource] = true
				names = append(names, c.Resource)
			}
		}
		return "follows " + strings.Join(names, ", ")
	case ch.Action == model.Replace && len(ch.ForcedBy) > 0 && ch.Reason == "replace_because_cannot_update":
		if len(ch.ForcedBy) == 1 {
			return ch.ForcedBy[0] + " forces replacement"
		}
		return strings.Join(ch.ForcedBy, ", ") + " force replacement"
	case ch.Action == model.Update && ch.Diff != nil:
		var keys []string
		for _, c := range ch.Diff.Children {
			if c.Kind != "unchanged" && !(c.Unknown && c.Kind == "added") && c.Key != "tags_all" {
				keys = append(keys, c.Key)
			}
		}
		if len(keys) > 4 {
			keys = append(keys[:4], fmt.Sprintf("+%d more", len(keys)-4))
		}
		if ch.Cosmetic != "" {
			return ch.Cosmetic + " only"
		}
		return strings.Join(keys, ", ")
	case ch.Action == model.Create && !ch.Importing:
		return ch.Noun
	case ch.Moved && ch.Action == model.NoOp:
		return "moved from " + ch.PrevAddress
	case ch.Importing:
		return "import " + ch.ImportID
	case ch.Action == model.Forget:
		return "removed from state, not destroyed"
	case ch.Deposed != "":
		return "deposed object"
	}
	if r, ok := shortReasons[ch.Reason]; ok {
		return r
	}
	return ""
}

// deployWords says what a deploy ships: "api 1.3.2 → 1.4.0", "new code",
// or with code spans for prose, "`api` `1.3.2` → `1.4.0`".
func deployWords(d model.Deploy, code bool) string {
	q := func(s string) string {
		if code {
			return "`" + s + "`"
		}
		return s
	}
	to := q(d.To)
	if d.Unknown {
		to = "a version known after apply"
	}
	switch {
	case d.What == "code":
		return "new code"
	case d.From == "":
		return q(d.What) + " " + to
	}
	return q(d.What) + " " + q(d.From) + " → " + to
}
