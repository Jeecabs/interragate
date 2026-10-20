package analysis

import (
	"fmt"
	"path"
	"slices"
	"sort"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/owners"
	"github.com/Jeecabs/interragate/internal/rules"
)

// owners reads the repository's CODEOWNERS file and says who owns each
// changing resource's block: GitHub asks the owners of the files a pull
// request edits to review it, but a plan can reach resources in files
// nobody edited.
func (a *analyzer) owners(r *model.Report) {
	d := a.opts.Edits
	if d == nil || d.Top == "" || r.Git == nil {
		return
	}
	f := owners.Find(d.Top)
	if f == nil {
		return
	}
	r.Git.Codeowners = f.Path
	inRepo := func(file string) string { return path.Join(d.Prefix, file) }
	for _, ch := range r.Changes {
		switch {
		case ch.Location != nil:
			ch.Owners = f.Owners(inRepo(ch.Location.File))
		case ch.Edit != nil && ch.Edit.File != "":
			ch.Owners = f.Owners(inRepo(ch.Edit.File)) // its block was removed from there
		}
	}
}

// ownerSentence names the code owners of what the plan changes who own
// none of the files the branch edited, so will not be asked to review it.
func ownerSentence(r *model.Report) *model.Sentence {
	if r.Git == nil || r.Git.Codeowners == "" || len(r.Git.Files) == 0 {
		return nil
	}
	asked := map[string]bool{}
	byFile := map[string][]string{}
	for _, ch := range r.Changes {
		if ch.Location != nil {
			byFile[ch.Location.File] = ch.Owners
		}
	}
	for _, file := range r.Git.Files {
		for _, o := range byFile[file] {
			asked[o] = true
		}
	}
	// The most serious change each owner should hear about leads.
	changes := slices.Clone(r.Changes)
	sort.SliceStable(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		return a.Severity > b.Severity || a.Severity == b.Severity && a.Action.Rank() > b.Action.Rank()
	})
	reach := map[string][]string{}
	for _, ch := range changes {
		if ch.Action == model.NoOp || ch.Cosmetic != "" {
			continue
		}
		for _, o := range ch.Owners {
			if !asked[o] && !slices.Contains(reach[o], ch.Address) {
				reach[o] = append(reach[o], ch.Address)
			}
		}
	}
	if len(reach) == 0 {
		return nil
	}
	names := make([]string, 0, len(reach))
	for o := range reach {
		names = append(names, o)
	}
	sort.Slice(names, func(i, j int) bool {
		return len(reach[names[i]]) > len(reach[names[j]]) || len(reach[names[i]]) == len(reach[names[j]]) && names[i] < names[j]
	})
	var parts, refs []string
	for _, o := range names {
		refs = append(refs, reach[o]...)
		parts = append(parts, fmt.Sprintf("%s (%s)", o, addressList(reach[o])))
	}
	text := fmt.Sprintf("This plan changes what %s assigns to %s, who own none of the files you edited, so they will not be asked to review it.",
		r.Git.Codeowners, rules.List(parts, 4))
	if len(names) == 1 {
		text = fmt.Sprintf("This plan changes %s, which %s assigns to %s, who owns none of the files you edited, so will not be asked to review it.",
			addressList(reach[names[0]]), r.Git.Codeowners, names[0])
	}
	return &model.Sentence{Severity: model.Info, Refs: refs, Text: text}
}

// addressList names the first address and counts the rest.
func addressList(addrs []string) string {
	if len(addrs) == 1 {
		return "`" + addrs[0] + "`"
	}
	return fmt.Sprintf("`%s` and %d more", addrs[0], len(addrs)-1)
}
