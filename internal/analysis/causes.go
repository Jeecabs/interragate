package analysis

import (
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/rules"
)

// causes finds, for each change, the upstream changes it follows from: an
// attribute that becomes unknown because the resource it reads from is
// being created or replaced. A change whose every difference is explained
// this way is indirect: it happens only because something else changes.
func (a *analyzer) causes(r *model.Report) {
	instances := map[string][]*model.Change{}
	for _, ch := range r.Changes {
		instances[ch.Resource] = append(instances[ch.Resource], ch)
	}
	for _, ch := range r.Changes {
		if ch.Action != model.Update && ch.Action != model.Replace && ch.Action != model.Read {
			continue
		}
		node := a.graph.Nodes[ch.Resource]
		if node == nil {
			continue
		}
		unknownRoots := map[string]bool{}
		knownChange, tagChange := false, false
		configured := a.configured(ch.Address)
		for _, l := range a.full[ch].Leaves() {
			switch {
			case l.Unknown && !configured[l.Root()] && ch.Action != model.Read:
				// A computed attribute the provider recalculates (version,
				// arn, ...). It is a consequence, not something to explain.
			case l.Unknown:
				unknownRoots[l.Root()] = true
			case tagAttrs[l.Root()]:
				tagChange = true
			case !l.Trivial:
				knownChange = true
			}
		}
		if ch.Action == model.Read {
			for attr := range node.Attrs {
				unknownRoots[attr] = true
			}
		}
		explained := map[string]bool{}
		seen := map[[2]string]bool{}
		for _, attr := range diff.SortedKeys(unknownRoots) {
			for _, ref := range node.Attrs[attr] {
				for _, up := range instances[ref.Resource] {
					if !a.producesUnknown(up, ref.Attribute) {
						continue
					}
					key := [2]string{attr, ref.Resource}
					if seen[key] {
						continue
					}
					seen[key] = true
					explained[attr] = true
					ch.Causes = append(ch.Causes, model.Cause{Attribute: attr, Resource: ref.Resource, Address: up.Address, Action: up.Action, Via: ref.Attribute})
				}
			}
		}
		sort.SliceStable(ch.Causes, func(i, j int) bool { return ch.Causes[i].Attribute < ch.Causes[j].Attribute })
		if len(ch.Causes) == 0 {
			continue
		}
		allExplained := len(unknownRoots) > 0
		for attr := range unknownRoots {
			if !explained[attr] {
				allExplained = false
			}
		}
		switch ch.Action {
		case model.Update:
			ch.Indirect = allExplained && !knownChange
			if ch.Indirect {
				ch.Why = "Only changes because " + causeList(ch.Causes) + "."
				if tagChange {
					ch.Why = "Changes because " + causeList(ch.Causes) + ", plus tag changes."
				}
			} else {
				ch.Why = strings.TrimSuffix(ch.Why, ".") + "; " + followList(ch.Causes) + "."
			}
		case model.Replace:
			forcedByUpstream := len(ch.ForcedBy) > 0
			for _, f := range ch.ForcedBy {
				if !explained[rootOf(f)] {
					forcedByUpstream = false
				}
			}
			if forcedByUpstream && ch.Reason == "replace_because_cannot_update" {
				ch.Indirect = true
				ch.Why = "Replaced because " + codes(ch.ForcedBy) + " will change: " + causeList(ch.Causes) + "."
			}
		}
	}
}

// producesUnknown reports whether a change makes the given attribute of the
// upstream object unknown (or new) until apply.
func (a *analyzer) producesUnknown(up *model.Change, attr string) bool {
	switch up.Action {
	case model.Create, model.Replace, model.Read:
		return true
	case model.Update:
		if attr == "" {
			return false
		}
		rc := a.raw[up]
		return rc != nil && diff.Flag(rc.Change.AfterUnknown, attr)
	}
	return false
}

func causeList(causes []model.Cause) string {
	var parts []string
	seen := map[string]bool{}
	for _, c := range causes {
		if seen[c.Resource] {
			continue
		}
		seen[c.Resource] = true
		verb := "changes"
		switch c.Action {
		case model.Create:
			verb = "is new"
		case model.Replace:
			verb = "is replaced"
		case model.Read:
			verb = "is read during apply"
		}
		parts = append(parts, rules.Code(c.Resource)+" "+verb)
	}
	return rules.List(parts, 3)
}

// followList renders causes per attribute: "`subnet_ids` follows
// `module.network.aws_subnet.private` (replaced)".
func followList(causes []model.Cause) string {
	byAttr := map[string][]string{}
	var attrs []string
	for _, c := range causes {
		if byAttr[c.Attribute] == nil {
			attrs = append(attrs, c.Attribute)
		}
		byAttr[c.Attribute] = append(byAttr[c.Attribute], rules.Code(c.Resource)+" ("+pastTense(c.Action)+")")
	}
	var parts []string
	for _, attr := range attrs {
		parts = append(parts, rules.Code(attr)+" follows "+rules.List(byAttr[attr], 2))
	}
	return rules.List(parts, 3)
}

func pastTense(a model.Action) string {
	switch a {
	case model.Create:
		return "new"
	case model.Replace:
		return "replaced"
	case model.Update:
		return "updated"
	case model.Read:
		return "read during apply"
	}
	return string(a)
}

func rootOf(path string) string {
	for i, c := range path {
		if c == '.' || c == '[' {
			return path[:i]
		}
	}
	return path
}
