package analysis

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/rules"
)

// risk derives the overall verdict from the findings.
func risk(r *model.Report) model.Risk {
	rk := model.Risk{Counts: map[string]int{}}
	for _, f := range r.Findings {
		rk.Counts[f.Severity.String()]++
		if f.Severity > rk.Level {
			rk.Level = f.Severity
		}
	}
	s := r.Summary
	if s.Total() > 0 && rk.Level < model.Info {
		rk.Level = model.Info
	}
	if (s.Delete > 0 || s.Replace > 0) && rk.Level < model.Low {
		rk.Level = model.Low
	}
	rk.Headline = headline(r, rk.Level)
	return rk
}

func headline(r *model.Report, level model.Severity) string {
	if r.Plan.Errored {
		return "The plan failed, so it is incomplete and cannot be applied."
	}
	if r.Summary.Total() == 0 {
		if r.Summary.Outputs > 0 {
			return "Only output values change."
		}
		return "No changes. The infrastructure matches the configuration."
	}
	// One phrase per rule, in severity order; repeated rules are counted.
	var order []string
	byRule := map[string][]*model.Finding{}
	for _, f := range r.Findings {
		if f.Severity < model.Medium || f.Short == "" {
			continue
		}
		if byRule[f.Rule] == nil {
			order = append(order, f.Rule)
		}
		byRule[f.Rule] = append(byRule[f.Rule], f)
	}
	var shorts []string
	seen := map[string]bool{}
	for _, rule := range order {
		fs := byRule[rule]
		short := fs[0].Short
		if len(fs) > 1 {
			if format, ok := pluralShort[rule]; ok {
				short = fmt.Sprintf(format, len(fs))
			}
		}
		if seen[short] {
			continue
		}
		seen[short] = true
		shorts = append(shorts, short)
		if len(shorts) == 3 {
			break
		}
	}
	if len(shorts) > 0 {
		return capitalize(rules.List(mergeFailures(shorts), 3)) + "."
	}
	return capitalize(actionSummary(r.Summary)) + ". Nothing stands out as risky."
}

// mergeFailures folds the phrases for an apply that will fail into the
// first of them: "will fail on deletion protection and on a name that is
// taken" rather than saying "will fail" twice.
func mergeFailures(shorts []string) []string {
	const prefix = "will fail on "
	var out, fails []string
	at := -1
	for _, s := range shorts {
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			if at < 0 {
				at = len(out)
				out = append(out, "")
			}
			fails = append(fails, rest)
			continue
		}
		out = append(out, s)
	}
	if at >= 0 {
		out[at] = prefix + rules.List(fails, 3)
	}
	return out
}

// pluralShort phrases a rule that fired more than once, for the headline.
var pluralShort = map[string]string{
	"stateful-destroy":             "destroys %d resources that hold data",
	"stateful-replace":             "replaces %d resources that hold data",
	"protected-delete":             "will fail on deletion protection for %d resources",
	"name-taken":                   "will fail on %d names that are taken",
	"engine-downgrade":             "will fail on %d engine downgrades",
	"major-upgrade-not-allowed":    "will fail on %d major upgrades that are not allowed",
	"storage-shrink":               "will fail on %d storage sizes that cannot shrink",
	"untied-destroy":               "destroys %d things no edit explains",
	"consumer-loses":               "destroys %d things services outside Terraform use",
	"consumer-value":               "changes %d values services outside Terraform hold",
	"consumer-reach":               "closes %d ways in that services outside Terraform use",
	"eks-version-skip":             "will fail on %d EKS version jumps",
	"role-delete-conflict":         "will fail on %d roles with policies attached",
	"replace-in-use":               "will fail on %d resources still in use",
	"role-recreated":               "breaks what refers to %d recreated roles",
	"import-replace":               "destroys %d resources it imports",
	"public-ingress":               "opens %d rules to the internet",
	"iam-wildcard":                 "grants %d wildcard permissions",
	"public-principal":             "makes %d resources public",
	"admin-access":                 "grants administrator access %d times",
	"public-bucket":                "makes storage public in %d places",
	"public-database":              "exposes %d databases",
	"encryption-disabled":          "stores data unencrypted in %d places",
	"missing-moved-block":          "recreates %d renamed resources",
	"count-index-shift":            "shifts count indexes in %d places",
	"network-disruption":           "disrupts %d network resources",
	"downtime-replace":             "replaces %d resources with a gap",
	"disruptive-update":            "restarts %d resources",
	"dns-change":                   "changes %d DNS records",
	"drift-revert":                 "undoes %d changes made by hand",
	"drift-deleted":                "recreates %d resources deleted by hand",
	"deletion-protection-disabled": "turns off deletion protection on %d resources",
	"final-snapshot-disabled":      "disables final snapshots on %d databases",
	"force-destroy-enabled":        "turns on force_destroy for %d resources",
	"secret-output":                "prints %d secret-looking outputs",
	"plaintext-parameter":          "stores %d secrets in plain text",
	"check-failed":                 "fails %d checks",
	"large-blast-radius":           "replaces %d things many resources depend on",
}

// actionSummary renders counts as "creates 3 resources and updates 2".
func actionSummary(s model.Summary) string {
	var parts []string
	add := func(n int, verb string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", verb, n))
		}
	}
	add(s.Create, "creates")
	add(s.Update, "updates")
	add(s.Replace, "replaces")
	add(s.Delete, "destroys")
	add(s.Import, "imports")
	add(s.Move, "moves")
	add(s.Forget, "forgets")
	add(s.Read, "reads")
	if len(parts) == 0 {
		return "no resource changes"
	}
	parts[0] += map[bool]string{true: " resource", false: " resources"}[firstCount(s) == 1]
	return rules.List(parts, 8)
}

func firstCount(s model.Summary) int {
	for _, n := range []int{s.Create, s.Update, s.Replace, s.Delete, s.Import, s.Move, s.Forget, s.Read} {
		if n > 0 {
			return n
		}
	}
	return 0
}

// narrative writes the plain-English summary: the findings that matter,
// then what the rest of the plan amounts to.
func narrative(r *model.Report) []model.Sentence {
	var out []model.Sentence
	minor := 0
	perRule := map[string]int{}
	more := map[string]int{} // rule -> index of its "more like that" sentence
	for _, f := range r.Findings {
		// What the plan does to services outside Terraform has a sentence
		// of its own, and a list per service.
		if f.Category == rules.Consumers {
			continue
		}
		if f.Severity < model.Medium {
			minor++
			continue
		}
		// Say a rule twice at most; then count the rest.
		perRule[f.Rule]++
		if perRule[f.Rule] > 2 {
			if i, ok := more[f.Rule]; ok {
				out[i].Refs = append(out[i].Refs, f.Addresses...)
				continue
			}
			out = append(out, model.Sentence{Severity: f.Severity, Refs: append([]string(nil), f.Addresses...)})
			more[f.Rule] = len(out) - 1
			continue
		}
		out = append(out, model.Sentence{Severity: f.Severity, Text: firstSentence(f.Message), Refs: f.Addresses})
	}
	for rule, i := range more {
		n := perRule[rule] - 2
		out[i].Text = fmt.Sprintf("%s like that (%s).", rules.Plural(n, "more finding", "more findings"), rule)
	}
	if text := editSentence(r); text != "" {
		out = append(out, model.Sentence{Severity: model.Info, Text: text})
	}
	if s := providerSentence(r); s != nil {
		out = append(out, *s)
	}
	if s := ownerSentence(r); s != nil {
		out = append(out, *s)
	}
	if s := deploySentence(r); s != nil {
		out = append(out, *s)
	}
	out = append(out, scanSentences(r)...)
	s := r.Summary
	if len(r.Patterns) > 0 {
		p := r.Patterns[0]
		out = append(out, model.Sentence{Severity: model.Info, Text: capitalize(p.Text) + "."})
	}
	if s.Cosmetic > 0 {
		out = append(out, model.Sentence{Severity: model.Info, Text: fmt.Sprintf("%s only change tags or formatting.", rules.Plural(s.Cosmetic, "update", "updates"))})
	}
	if s.Indirect == 1 {
		out = append(out, model.Sentence{Severity: model.Info, Text: "1 change only happens because something it depends on changes."})
	} else if s.Indirect > 1 {
		out = append(out, model.Sentence{Severity: model.Info, Text: fmt.Sprintf("%d changes only happen because something they depend on changes.", s.Indirect)})
	}
	if s.Create > 0 {
		var nouns []string
		for _, ch := range r.Changes {
			if ch.Action == model.Create && !ch.Importing {
				nouns = append(nouns, ch.Noun)
			}
		}
		out = append(out, model.Sentence{Severity: model.Info, Text: "Creates " + countNouns(nouns, 4) + "."})
	}
	if s.Move > 0 || s.Import > 0 || s.Forget > 0 {
		var parts []string
		if s.Move > 0 {
			parts = append(parts, "moves "+rules.Plural(s.Move, "resource", "resources"))
		}
		if s.Import > 0 {
			parts = append(parts, "imports "+rules.Plural(s.Import, "resource", "resources"))
		}
		if s.Forget > 0 {
			parts = append(parts, "stops managing "+rules.Plural(s.Forget, "resource", "resources")+" without destroying "+map[bool]string{true: "it", false: "them"}[s.Forget == 1])
		}
		out = append(out, model.Sentence{Severity: model.Info, Text: capitalize(rules.List(parts, 3)) + "."})
	}
	if minor > 0 {
		out = append(out, model.Sentence{Severity: model.Low, Text: fmt.Sprintf("Plus %s.", rules.Plural(minor, "minor finding", "minor findings"))})
	}
	return out
}

// editSentence says how much of the plan comes from the edits to the
// configuration since the git base: the changes whose blocks were edited,
// those that only follow from them, and the rest, which a reviewer of the
// edit did not ask for.
func editSentence(r *model.Report) string {
	if r.Git == nil {
		return ""
	}
	edited, following, other := 0, 0, 0
	for _, ch := range r.Changes {
		switch {
		case !acts(ch):
		case ch.Origin == "edit":
			edited++
		case ch.Origin == "follows":
			following++
		default:
			other++
		}
	}
	if edited == 0 {
		return ""
	}
	who := "Your edits since " + rules.Code(r.Git.Base)
	if r.Git.Base == "HEAD" {
		who = "Your uncommitted edits"
	}
	text := who + " touch " + rules.Plural(edited, "resource", "resources")
	var rest []string
	switch {
	case following == 1:
		rest = append(rest, "1 more changes because of them")
	case following > 1:
		rest = append(rest, fmt.Sprintf("%d more change because of them", following))
	}
	switch {
	case other == 1:
		rest = append(rest, "1 other change is not tied to an edit")
	case other > 1:
		rest = append(rest, fmt.Sprintf("%d other changes are not tied to an edit", other))
	}
	if len(rest) > 0 {
		text += "; " + rules.List(rest, 2)
	}
	return text + "."
}

// scanSentences say what each scanner reported and how much of it this
// plan touches; the rest was there before and is not news.
func scanSentences(r *model.Report) []model.Sentence {
	var out []model.Sentence
	for _, s := range r.Scans {
		var text string
		switch {
		case s.Error != "":
			out = append(out, model.Sentence{Severity: model.Low, Text: fmt.Sprintf("%s could not run: %s.", s.Tool, strings.TrimSuffix(s.Error, "."))})
			continue
		case s.Issues == 0:
			text = s.Tool + " reports no issues in the configuration."
		case s.Shown == 0:
			text = fmt.Sprintf("%s reports %s, none on what this plan creates, replaces or edits.", s.Tool, rules.Plural(s.Issues, "issue", "issues"))
		case s.Shown == s.Issues:
			text = fmt.Sprintf("%s reports %s, all on what this plan creates, replaces or edits.", s.Tool, rules.Plural(s.Issues, "issue", "issues"))
		default:
			text = fmt.Sprintf("%s reports %s: %d on what this plan creates, replaces or edits, and %d that were there before.", s.Tool, rules.Plural(s.Issues, "issue", "issues"), s.Shown, s.Issues-s.Shown)
		}
		out = append(out, model.Sentence{Severity: model.Info, Text: text})
	}
	return out
}

// providerSentence says which providers' locked versions moved since the
// git base, and how many of the changes nobody edited they may explain: a
// provider upgrade changes defaults and computed values, and is a common
// reason a plan shows changes no one asked for.
func providerSentence(r *model.Report) *model.Sentence {
	if r.Git == nil {
		return nil
	}
	var moved []model.ProviderChange
	for _, p := range r.Git.Providers {
		if p.From != "" && p.To != "" {
			moved = append(moved, p)
		}
	}
	if len(moved) == 0 {
		return nil
	}
	short := func(src string) string { return src[strings.LastIndex(src, "/")+1:] }
	untied := 0
	names := map[string]bool{}
	for _, p := range moved {
		names[short(p.Source)] = true
	}
	// A change is known not to be tied to an edit when interragate traced
	// the edits, or when nothing was edited at all; in a destroy plan,
	// -destroy explains every change.
	for _, ch := range r.Changes {
		if acts(ch) && ch.Origin == "" && names[ch.Provider] && (rules.Traced(r) || len(r.Git.Files) == 0 && !r.Plan.Destroy) {
			untied++
		}
	}
	since := "since " + rules.Code(r.Git.Base)
	if r.Git.Base == "HEAD" {
		since = "in your uncommitted changes"
	}
	var text string
	var majors []string // the providers that move to a new major version
	for _, p := range moved {
		if majorOf(p.From) != majorOf(p.To) {
			majors = append(majors, rules.Code(short(p.Source)))
		}
	}
	major := len(majors) > 0
	if len(moved) == 1 {
		p := moved[0]
		text = fmt.Sprintf("The %s provider moves from %s to %s %s", rules.Code(short(p.Source)), p.From, p.To, since)
	} else {
		var parts []string
		for _, p := range moved {
			parts = append(parts, fmt.Sprintf("%s %s → %s", rules.Code(short(p.Source)), p.From, p.To))
		}
		text = "Providers move " + since + ": " + rules.List(parts, 3)
	}
	sev := model.Info
	switch whose := map[bool]string{true: "its", false: "their"}[len(moved) == 1]; {
	case untied == 1:
		text += ", which may explain the one change to " + whose + " resources that is not tied to an edit"
	case untied > 1:
		text += fmt.Sprintf(", which may explain the %d changes to %s resources that are not tied to an edit", untied, whose)
	}
	text += "."
	if major {
		switch {
		case len(moved) == 1:
			text += " That is a new major version, so its upgrade guide is worth a read."
		case len(majors) == 1:
			text += " " + majors[0] + " moves to a new major version, so its upgrade guide is worth a read."
		default:
			text += " " + rules.List(majors, 3) + " move to new major versions, so their upgrade guides are worth a read."
		}
		if untied > 0 {
			sev = model.Low
		}
	}
	return &model.Sentence{Severity: sev, Text: text}
}

func majorOf(v string) string {
	major, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), ".")
	return major
}

// countNouns renders "2 EC2 instances, an SNS topic and a CloudWatch alarm".
func countNouns(nouns []string, max int) string {
	counts := map[string]int{}
	var order []string
	for _, n := range nouns {
		if counts[n] == 0 {
			order = append(order, n)
		}
		counts[n]++
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	var parts []string
	for _, n := range order {
		if counts[n] == 1 {
			parts = append(parts, rules.Article(n))
		} else {
			parts = append(parts, fmt.Sprintf("%d %s", counts[n], pluralize(n)))
		}
	}
	return rules.List(parts, max)
}

func pluralize(noun string) string {
	switch {
	case strings.HasSuffix(noun, "s"), strings.HasSuffix(noun, "x"), strings.HasSuffix(noun, "ch"), strings.HasSuffix(noun, "sh"):
		return noun + "es"
	case strings.HasSuffix(noun, "y") && len(noun) > 1 && !strings.ContainsRune("aeiou", rune(noun[len(noun)-2])):
		return noun[:len(noun)-1] + "ies"
	}
	return noun + "s"
}

// firstSentence returns the text up to the first full stop that is not
// inside backticks.
func firstSentence(s string) string {
	inCode := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '`':
			inCode = !inCode
		case '.':
			if !inCode && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\n') {
				return s[:i+1]
			}
		}
	}
	return s
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// deploySentence says what new versions the plan ships, when it ships any:
// for many plans that is the whole point, and the rest is what follows.
func deploySentence(r *model.Report) *model.Sentence {
	var parts, refs []string
	n := 0
	for _, ch := range r.Changes {
		for _, d := range ch.Deploys {
			n++
			if len(parts) < 3 {
				parts = append(parts, deployWords(d, true)+" in `"+ch.Address+"`")
			}
			refs = append(refs, ch.Address)
		}
	}
	if n == 0 {
		return nil
	}
	if n > len(parts) {
		parts = append(parts, fmt.Sprintf("%d more", n-len(parts)))
	}
	return &model.Sentence{Severity: model.Info, Refs: refs, Text: "Deploys " + rules.List(parts, 4) + "."}
}
