package analysis

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/rules"
)

// minPattern is how many resources must share a change before it is
// reported as a pattern rather than read resource by resource.
const minPattern = 3

// patterns finds attribute changes repeated across many resources, such as
// a default tag changing everywhere, so the summary can say it once.
func (a *analyzer) patterns(r *model.Report) []*model.Pattern {
	type acc struct {
		p     *model.Pattern
		addrs map[string]bool
	}
	groups := map[string]*acc{}
	var order []string
	for _, ch := range r.Changes {
		if ch.Action != model.Update {
			continue
		}
		for _, l := range a.full[ch].Leaves() {
			if l.Unknown || l.Sensitive || l.Lines != nil || len(l.Children) > 0 {
				continue
			}
			if isComposite(l.Before) || isComposite(l.After) {
				continue
			}
			key := l.PathString() + "\x00" + string(l.Kind) + "\x00" + diff.String(l.Before) + "\x00" + diff.String(l.After)
			g := groups[key]
			if g == nil {
				g = &acc{p: &model.Pattern{Path: l.PathString(), Before: l.Before, After: l.After}, addrs: map[string]bool{}}
				groups[key] = g
				order = append(order, key)
			}
			g.addrs[ch.Address] = true
		}
	}
	defaultTags := a.hasDefaultTags()
	var out []*model.Pattern
	for _, key := range order {
		g := groups[key]
		if len(g.addrs) < minPattern {
			continue
		}
		for addr := range g.addrs {
			g.p.Addresses = append(g.p.Addresses, addr)
		}
		sort.Strings(g.p.Addresses)
		g.p.Count = len(g.p.Addresses)
		g.p.Text = patternText(g.p, defaultTags)
		out = append(out, g.p)
	}
	out = dropTagEchoes(out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// dropTagEchoes removes tags.K patterns that only mirror a tags_all.K
// pattern on the same resources, which is how a default_tags change shows up.
func dropTagEchoes(ps []*model.Pattern) []*model.Pattern {
	all := map[string]map[string]bool{}
	for _, p := range ps {
		if key, ok := strings.CutPrefix(p.Path, "tags_all."); ok {
			set := map[string]bool{}
			for _, a := range p.Addresses {
				set[a] = true
			}
			all[key] = set
		}
	}
	out := ps[:0]
	for _, p := range ps {
		if key, ok := strings.CutPrefix(p.Path, "tags."); ok && all[key] != nil {
			overlap := 0
			for _, a := range p.Addresses {
				if all[key][a] {
					overlap++
				}
			}
			if overlap*5 >= len(p.Addresses)*4 {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

func isComposite(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

func patternText(p *model.Pattern, defaultTags bool) string {
	var change string
	switch {
	case p.Before == nil:
		change = fmt.Sprintf("%s set to %s", rules.Code(p.Path), rules.Value(p.After))
	case p.After == nil:
		change = fmt.Sprintf("%s (%s) removed", rules.Code(p.Path), rules.Value(p.Before))
	default:
		change = fmt.Sprintf("%s changes %s → %s", rules.Code(p.Path), rules.Value(p.Before), rules.Value(p.After))
	}
	s := fmt.Sprintf("%s on %d resources", change, p.Count)
	if defaultTags && (strings.HasPrefix(p.Path, "tags_all") || strings.HasPrefix(p.Path, "tags.")) {
		s += " (provider `default_tags`)"
	}
	return s
}

func (a *analyzer) hasDefaultTags() bool {
	if a.plan.Configuration == nil {
		return false
	}
	for _, pc := range a.plan.Configuration.ProviderConfig {
		if _, ok := pc.Expressions["default_tags"]; ok {
			return true
		}
	}
	return false
}
