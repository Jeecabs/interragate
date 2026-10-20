//go:build ignore

// Build writes interragate's website, the one GitHub Pages serves, into a
// directory: the landing page, the rules, and the sample reports exactly as
// interragate draws them. Run it from the top of the repository:
//
//	go run site/build.go [-o _site]
//
// The screenshots in site/images and the social card, og.png, are pictures
// of the aws sample's report; take them again when its look changes.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Jeecabs/interragate"
	"github.com/Jeecabs/interragate/internal/demo"
	"github.com/Jeecabs/interragate/internal/rules"
)

// published is where GitHub Pages serves the site. Links that must be
// absolute, such as the social card's, start with it.
const published = "https://jeecabs.github.io/interragate/"

func main() {
	log.SetFlags(0)
	out := flag.String("o", "_site", "the directory to write the site to, replacing what is there")
	flag.Parse()
	if err := build(*out); err != nil {
		log.Fatal("site: ", err)
	}
}

func build(out string) error {
	if _, err := os.Stat("site/build.go"); err != nil {
		return errors.New("run this from the top of the interragate repository: go run site/build.go")
	}
	tmp, err := os.MkdirTemp("", "interragate-site-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "interragate")
	if err := run("go", "build", "-o", bin, "./cmd/interragate"); err != nil {
		return err
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}

	// The samples interragate demo explains, as the reports it writes.
	for _, s := range []struct{ name, path string }{
		{"aws", "demo/index.html"},
		{"mechanics", "demo/mechanics/index.html"},
	} {
		path := filepath.Join(out, s.path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := run(bin, "demo", s.name, "--format", "html", "--out", path); err != nil {
			return err
		}
	}

	sample, err := readSample(bin, tmp)
	if err != nil {
		return err
	}
	term, err := terminal(bin)
	if err != nil {
		return err
	}
	review, err := readComment(bin)
	if err != nil {
		return err
	}
	all := rules.All()
	data := map[string]any{
		"Published": published,
		"Sample":    sample,
		"Terminal":  term,
		"Comment":   review,
		"Rules":     all,
		"Groups":    groups(all),
	}

	funcs := sample.funcs()
	funcs["rule"] = func(id string) (*rules.Rule, error) {
		if r := rules.Lookup(id); r != nil {
			return r, nil
		}
		return nil, fmt.Errorf("no rule %q", id)
	}
	funcs["doc"] = doc
	funcs["sev"] = sevIcon
	funcs["sevLabel"] = func(sev string) string { return strings.ToUpper(sev[:1]) + sev[1:] }
	funcs["add"] = func(a, b int) int { return a + b }
	funcs["list"] = func(xs ...string) []string { return xs }
	funcs["lines"] = func(s string) []string { return strings.Split(s, "\n") }
	funcs["seq"] = func(n int) []int {
		s := make([]int, n)
		for i := range s {
			s[i] = i
		}
		return s
	}
	pages, err := template.New("").Funcs(funcs).ParseGlob("site/*.html")
	if err != nil {
		return err
	}
	for _, p := range []struct{ tmpl, path, title, about string }{
		{"index.html", "index.html",
			"interragate · Terraform and OpenTofu plans, explained",
			"Understand your Terraform or OpenTofu plan before you apply. See what it destroys, why it changes and where the apply will stop."},
		{"rules.html", "rules/index.html",
			"Rules · interragate",
			fmt.Sprintf("The %d rules interragate checks a Terraform or OpenTofu plan with, and why each matters.", len(all))},
		{"404.html", "404.html", "Not found · interragate", ""},
	} {
		// Pages link to each other relatively, so the site works wherever
		// it is served, except the 404 page, which answers for any path.
		root := strings.Repeat("../", strings.Count(p.path, "/"))
		if p.tmpl == "404.html" {
			u, err := url.Parse(published)
			if err != nil {
				return err
			}
			root = u.Path
		}
		page := map[string]any{"Root": root, "Path": strings.TrimSuffix(p.path, "index.html"), "Title": p.title, "About": p.about}
		for k, v := range data {
			page[k] = v
		}
		var b bytes.Buffer
		if err := pages.ExecuteTemplate(&b, p.tmpl, page); err != nil {
			return err
		}
		if err := write(filepath.Join(out, p.path), b.Bytes()); err != nil {
			return err
		}
	}

	// Everything else in site/ is copied as it is, with the report's fonts
	// and their licence.
	if err := copyTree("site", out, func(name string) bool {
		return !strings.HasSuffix(name, ".html") && !strings.HasSuffix(name, ".go")
	}); err != nil {
		return err
	}
	if err := copyTree("internal/render/html/assets/fonts", filepath.Join(out, "fonts"), func(string) bool { return true }); err != nil {
		return err
	}
	if err := copyTree("third_party/geist", filepath.Join(out, "fonts"), func(string) bool { return true }); err != nil {
		return err
	}
	return write(filepath.Join(out, "licences.txt"), []byte(interragate.LicenceNotices()))
}

// run runs a command, passing on what it prints when it fails.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, b)
	}
	return nil
}

// sample is the aws sample as the landing page draws it, read from the
// report interragate writes for it and from the plan behind that, so the
// page never claims what the sample does not.
type sample struct {
	Figure *figure
	Branch *branchFigure
	Apply  *applyFigure

	report *report
	plan   map[string]map[string]map[string]any // address, "before" or "after", attribute
}

// report is the part of interragate's JSON report that the page reads.
type report struct {
	Summary map[string]int `json:"summary"`
	Risk    struct {
		Level    string `json:"level"`
		Headline string `json:"headline"`
	} `json:"risk"`
	Plan struct {
		Tool    string `json:"tool"`
		Version string `json:"version"`
		Dir     string `json:"dir"`
	} `json:"plan"`
	Git struct {
		Base   string   `json:"base"`
		Branch string   `json:"branch"`
		Files  []string `json:"files"`
	} `json:"git"`
	Changes  []change  `json:"changes"`
	Findings []finding `json:"findings"`
	Apply    struct {
		Duration float64 `json:"duration"`
		Steps    []struct {
			ID       string   `json:"id"`
			Address  string   `json:"address"`
			Op       string   `json:"op"`
			After    []string `json:"after"`
			Start    float64  `json:"start"`
			Duration float64  `json:"duration"`
			Fails    string   `json:"fails"`
		} `json:"steps"`
		Joins []struct {
			ID    string   `json:"id"`
			After []string `json:"after"`
		} `json:"joins"`
		Gaps []struct {
			Address string  `json:"address"`
			From    float64 `json:"from"`
			To      float64 `json:"to"`
		} `json:"gaps"`
		Narrative []struct {
			Text string `json:"text"`
		} `json:"narrative"`
	} `json:"apply"`
}

type change struct {
	Address  string   `json:"address"`
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Key      string   `json:"key"`
	Action   string   `json:"action"`
	Order    string   `json:"order"`
	Why      string   `json:"why"`
	Summary  string   `json:"summary"`
	Noun     string   `json:"noun"`
	Severity string   `json:"severity"`
	Cosmetic string   `json:"cosmetic"`
	Origin   string   `json:"origin"`
	Findings []string `json:"findings"`
	Edit     *struct {
		Lines []struct {
			Line int    `json:"line"`
			Text string `json:"text"`
		} `json:"lines"`
	} `json:"edit"`
}

type finding struct {
	ID          string   `json:"id"`
	Severity    string   `json:"severity"`
	Title       string   `json:"title"`
	Short       string   `json:"short"`
	Message     string   `json:"message"`
	Remediation string   `json:"remediation"`
	Snippet     string   `json:"snippet"`
	Addresses   []string `json:"addresses"`
}

func readSample(bin, dir string) (*sample, error) {
	path := filepath.Join(dir, "aws.json")
	if err := run(bin, "demo", "aws", "--format", "json", "--out", path); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &sample{report: &report{}, plan: map[string]map[string]map[string]any{}}
	if err := json.Unmarshal(b, s.report); err != nil {
		return nil, err
	}

	d, err := demo.Find("aws")
	if err != nil {
		return nil, err
	}
	if b, err = d.Plan(); err != nil {
		return nil, err
	}
	var plan struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Change  struct {
				Before map[string]any `json:"before"`
				After  map[string]any `json:"after"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(b, &plan); err != nil {
		return nil, err
	}
	for _, rc := range plan.ResourceChanges {
		s.plan[rc.Address] = map[string]map[string]any{"before": rc.Change.Before, "after": rc.Change.After}
	}

	if s.Figure, err = s.report.figure(); err != nil {
		return nil, err
	}
	if s.Branch, err = s.report.branch(); err != nil {
		return nil, err
	}
	s.Apply, err = s.report.apply(
		"aws_db_instance.orders",
		"module.network.aws_subnet.public[1]",
		"module.network.aws_route_table_association.public[1]",
		"aws_lb_target_group_attachment.web",
		"aws_instance.web",
		"aws_instance.app",
		"aws_ssm_parameter.db_endpoint",
	)
	return s, err
}

var aligned = regexp.MustCompile(`\s+=\s+`)

// funcs read the sample for the page's templates. Each fails the build when
// the sample no longer has what the page asks for.
func (s *sample) funcs() template.FuncMap {
	return template.FuncMap{
		// finding is one of the report's findings, by its ID.
		"finding": func(id string) (*finding, error) {
			for i, f := range s.report.Findings {
				if f.ID == id {
					return &s.report.Findings[i], nil
				}
			}
			return nil, fmt.Errorf("the sample has no finding %s", id)
		},
		// plan is an attribute of a resource as the plan has it, before
		// or after the change.
		"plan": func(address, when, attr string) (any, error) {
			v, ok := s.plan[address][when][attr]
			if !ok || v == nil {
				return nil, fmt.Errorf("the sample's plan has no %s for %s %s", when, address, attr)
			}
			return v, nil
		},
		// edit is a line of the edit behind a change, as it reads in the
		// configuration, without the spaces that align its = with its
		// neighbours'.
		"edit": func(address string, line int) (string, error) {
			for _, c := range s.report.Changes {
				if c.Address != address || c.Edit == nil {
					continue
				}
				for _, l := range c.Edit.Lines {
					if l.Line == line {
						indent := l.Text[:len(l.Text)-len(strings.TrimLeft(l.Text, " "))]
						return indent + aligned.ReplaceAllString(strings.TrimSpace(l.Text), " = "), nil
					}
				}
			}
			return "", fmt.Errorf("the sample's edit to %s has no line %d", address, line)
		},
		// change is one of the report's changes, by address.
		"change": func(address string) (*change, error) {
			for i, c := range s.report.Changes {
				if c.Address == address {
					return &s.report.Changes[i], nil
				}
			}
			return nil, fmt.Errorf("the sample no longer changes %s", address)
		},
		// gap is how many minutes the apply leaves a replaced resource not
		// existing, as the report rounds it.
		"gap": func(address string) (int, error) {
			for _, g := range s.report.Apply.Gaps {
				if g.Address == address {
					return int(math.Round((g.To - g.From) / 60)), nil
				}
			}
			return 0, fmt.Errorf("the sample's apply leaves no gap for %s", address)
		},
		// ingress is a security group's inbound rules, as they are and with
		// what the plan adds or takes away.
		"ingress": s.ingress,
		// first is the first sentence of a message.
		"first": func(msg string) string {
			if i := strings.Index(msg, ". "); i >= 0 {
				return msg[:i+1]
			}
			return msg
		},
	}
}

// rule is one inbound rule of a security group, as the page lists it.
type rule struct {
	Port, From, About string
	Added, Removed    bool
}

func (s *sample) ingress(address string) ([]rule, error) {
	list := func(when string) []map[string]any {
		var out []map[string]any
		rs, _ := s.plan[address][when]["ingress"].([]any)
		for _, r := range rs {
			if m, ok := r.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	view := func(m map[string]any) rule {
		port := fmt.Sprint(m["from_port"])
		if to := fmt.Sprint(m["to_port"]); to != port {
			port += "-" + to
		}
		from := "security group"
		if cidrs, _ := m["cidr_blocks"].([]any); len(cidrs) > 0 {
			var cs []string
			for _, c := range cidrs {
				cs = append(cs, fmt.Sprint(c))
			}
			from = strings.Join(cs, ", ")
		}
		about, _ := m["description"].(string)
		return rule{Port: port + "/" + fmt.Sprint(m["protocol"]), From: from, About: about}
	}
	before, after := list("before"), list("after")
	if len(before)+len(after) == 0 {
		return nil, fmt.Errorf("the sample's plan has no inbound rules for %s", address)
	}
	has := func(rs []map[string]any, r map[string]any) bool {
		for _, x := range rs {
			if fmt.Sprint(x) == fmt.Sprint(r) {
				return true
			}
		}
		return false
	}
	var out []rule
	for _, r := range before {
		v := view(r)
		v.Removed = !has(after, r)
		out = append(out, v)
	}
	for _, r := range after {
		if !has(before, r) {
			v := view(r)
			v.Added = true
			out = append(out, v)
		}
	}
	return out, nil
}

// figure is the sample plan as the report's overview draws it: the verdict,
// then a row of squares for each kind of change.
type figure struct {
	Risk, RiskLabel string
	Headline        []phrase
	Meta            []string // where the plan comes from
	Rows            []figureRow
	Detail          *unit // the resource the headline names first
}

// phrase is part of the headline, and the resource it is about when it
// names one.
type phrase struct {
	Text, Address string
}

type figureRow struct {
	Count int
	Label string
	Units []unit
	Note  string
}

// unit is one square of the figure: a changed resource, shaped by what
// happens to it.
type unit struct {
	Action  string
	Class   string // the action, and cosmetic for an update that only touches tags or formatting
	Sym     string // the action in Terraform's notation
	Address string
	Noun    string
	Origin  string // edit, follows or untied
	Index   int    // its place in the figure
	Why     string // why it changes, in the report's words
	Finding string // its worst finding's title, if it has one
	Sev     string // that finding's severity
}

var kinds = []struct{ action, word string }{
	{"delete", "destroyed"},
	{"replace", "replaced"},
	{"update", "updated"},
	{"create", "created"},
	{"read", "read"},
}

// units are the changed resources, in the figure's order: worst action
// first, then the updates that matter before those that only touch tags
// or formatting, then by address.
func (r *report) units() []unit {
	rank := map[string]int{}
	for i, k := range kinds {
		rank[k.action] = i + 1
	}
	var cs []change
	for _, c := range r.Changes {
		if rank[c.Action] > 0 {
			cs = append(cs, c)
		}
	}
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if rank[a.Action] != rank[b.Action] {
			return rank[a.Action] < rank[b.Action]
		}
		if (a.Cosmetic == "") != (b.Cosmetic == "") {
			return a.Cosmetic == ""
		}
		return a.Address < b.Address
	})
	found := map[string]finding{}
	for _, f := range r.Findings {
		found[f.ID] = f
	}
	us := make([]unit, len(cs))
	for i, c := range cs {
		u := unit{Action: c.Action, Class: c.Action, Address: c.Address, Noun: c.Noun, Origin: c.Origin, Index: i}
		if c.Cosmetic != "" {
			u.Class += " cosmetic"
		}
		u.Sym = map[string]string{"delete": "-", "replace": "-/+", "update": "~", "create": "+", "read": "<="}[c.Action]
		if c.Action == "replace" && c.Order == "create-first" {
			u.Sym = "+/-"
		}
		if u.Origin == "" {
			u.Origin = "untied"
		}
		u.Why = c.Why
		if len(c.Findings) > 0 {
			f := found[c.Findings[0]]
			u.Finding, u.Sev = f.Title, f.Severity
		}
		us[i] = u
	}
	return us
}

func (r *report) figure() (*figure, error) {
	f := &figure{Risk: r.Risk.Level, Headline: r.phrases()}
	if l := r.Risk.Level; l != "" {
		f.RiskLabel = strings.ToUpper(l[:1]) + l[1:] + " risk"
	}
	tool := map[string]string{"terraform": "Terraform", "tofu": "OpenTofu"}[r.Plan.Tool]
	for _, m := range []string{r.Plan.Dir, strings.TrimSpace(tool + " " + r.Plan.Version), "branch " + r.Git.Branch} {
		if m != "" && m != "branch " {
			f.Meta = append(f.Meta, m)
		}
	}
	units := r.units()
	for _, k := range kinds {
		row := figureRow{Label: k.word}
		hollow := 0
		for _, u := range units {
			if u.Action == k.action {
				row.Units = append(row.Units, u)
				if u.Class != u.Action {
					hollow++
				}
			}
		}
		if row.Count = len(row.Units); row.Count == 0 {
			continue
		}
		if hollow > 0 {
			row.Note = strconv.Itoa(hollow) + " only touch tags or formatting"
		}
		f.Rows = append(f.Rows, row)
	}
	if r.Risk.Headline == "" || len(f.Rows) == 0 {
		return nil, errors.New("the sample report has no verdict or changes")
	}
	for _, p := range f.Headline {
		for _, u := range units {
			if p.Address != "" && u.Address == p.Address && f.Detail == nil {
				f.Detail = &u
			}
		}
	}
	if f.Detail == nil {
		return nil, errors.New("the sample's headline no longer names a resource's finding")
	}
	return f, nil
}

// phrases splits the headline into the findings it names, each with the
// resource it is about, and the words between them.
func (r *report) phrases() []phrase {
	h := r.Risk.Headline
	lower := strings.ToLower(h)
	type hit struct {
		at, end int
		address string
	}
	var hits []hit
	for _, f := range r.Findings {
		if f.Short == "" || len(f.Addresses) == 0 {
			continue
		}
		i := strings.Index(lower, strings.ToLower(f.Short))
		if i < 0 {
			continue
		}
		clash := false
		for _, o := range hits {
			clash = clash || (i < o.end && o.at < i+len(f.Short))
		}
		if !clash {
			hits = append(hits, hit{i, i + len(f.Short), f.Addresses[0]})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].at < hits[j].at })
	var out []phrase
	at := 0
	for _, x := range hits {
		if x.at > at {
			out = append(out, phrase{Text: h[at:x.at]})
		}
		out = append(out, phrase{Text: h[x.at:x.end], Address: x.address})
		at = x.end
	}
	if at < len(h) {
		out = append(out, phrase{Text: h[at:]})
	}
	return out
}

// branchFigure is the sample's changes sorted by where they come from: the
// branch's edits, what follows from them, and what no edit explains.
type branchFigure struct {
	Base, Branch string
	Files        []string
	Units        []unit // in the figure's order
	Groups       []branchGroup
}

type branchGroup struct {
	Origin string
	Units  []unit
}

func (r *report) branch() (*branchFigure, error) {
	b := &branchFigure{Base: r.Git.Base, Branch: r.Git.Branch, Files: r.Git.Files, Units: r.units()}
	for _, o := range []string{"edit", "follows", "untied"} {
		g := branchGroup{Origin: o}
		for _, u := range b.Units {
			if u.Origin == o {
				g.Units = append(g.Units, u)
			}
		}
		b.Groups = append(b.Groups, g)
	}
	if b.Base == "" || len(b.Groups[0].Units) == 0 || len(b.Groups[2].Units) == 0 {
		return nil, errors.New("the sample report no longer tells its edits from the rest")
	}
	return b, nil
}

// applyFigure is the sample's apply as the report's timeline draws it,
// with the step that is expected to fail and what that leaves behind.
type applyFigure struct {
	Fails    string  // the resource whose step fails
	StopAt   float64 // where it fails, in percent of the time axis
	Skipped  int     // resources skipped because they wait for it
	Stranded int     // resources left destroyed with nothing in their place
	Ticks    []applyTick
	Rows     []applyRow
}

type applyTick struct {
	Label string
	At    float64
}

type applyRow struct {
	Type, Name string
	Fails      bool // its step is the one that fails
	Skipped    bool // every step of it is skipped
	Stranded   bool // destroyed, and its replacement skipped
	Bars       []applyBar
	Gap        float64 // for a stranded resource, where it stops existing
}

type applyBar struct {
	Op, State   string // State is done, fails or skipped
	Left, Width float64
}

// apply draws the rows for the given resources, and counts what the
// failing step skips and strands across the whole apply.
func (r *report) apply(rows ...string) (*applyFigure, error) {
	a := r.Apply
	after := map[string][]string{}
	for _, j := range a.Joins {
		after[j.ID] = j.After
	}
	fails, failing := "", ""
	for _, s := range a.Steps {
		after[s.ID] = s.After
		if s.Fails != "" {
			fails, failing = s.ID, s.Address
		}
	}
	if fails == "" || a.Duration <= 0 {
		return nil, errors.New("the sample's apply no longer has a step expected to fail")
	}

	// A step is skipped when anything it waits for fails or is skipped.
	skip := map[string]bool{}
	var waits func(id string) bool
	waits = func(id string) bool {
		if v, ok := skip[id]; ok {
			return v
		}
		skip[id] = false
		for _, d := range after[id] {
			if d == fails || waits(d) {
				skip[id] = true
				break
			}
		}
		return skip[id]
	}
	state := func(id string) string {
		switch {
		case id == fails:
			return "fails"
		case waits(id):
			return "skipped"
		}
		return "done"
	}

	f := &applyFigure{Fails: failing}
	skipped, destroyed, recreated := map[string]bool{}, map[string]float64{}, map[string]bool{}
	for _, s := range a.Steps {
		switch st := state(s.ID); {
		case st == "skipped" && s.Address != failing:
			skipped[s.Address] = true
			if s.Op == "create" {
				recreated[s.Address] = false
			}
		case st == "done" && s.Op == "destroy":
			destroyed[s.Address] = s.Start + s.Duration
		case s.ID == fails:
			f.StopAt = pct(s.Start+s.Duration, a.Duration)
		}
	}
	f.Skipped = len(skipped)
	for addr := range destroyed {
		if _, ok := recreated[addr]; ok {
			f.Stranded++
		}
	}
	// The report says how many are skipped; the drawing must agree.
	if len(a.Narrative) == 0 || !strings.Contains(a.Narrative[0].Text, fmt.Sprintf("The %d resources that wait for it are skipped", f.Skipped)) {
		return nil, fmt.Errorf("the sample's apply no longer skips %d resources", f.Skipped)
	}

	for t := 0.0; t <= a.Duration; t += 300 {
		label := strconv.Itoa(int(t/60)) + " min"
		if t == 0 {
			label = "0"
		}
		f.Ticks = append(f.Ticks, applyTick{Label: label, At: pct(t, a.Duration)})
	}

	changes := map[string]change{}
	for _, c := range r.Changes {
		changes[c.Address] = c
	}
	for _, addr := range rows {
		c, ok := changes[addr]
		if !ok {
			return nil, fmt.Errorf("the sample no longer changes %s", addr)
		}
		row := applyRow{Type: c.Type, Name: c.Name + c.Key, Fails: addr == failing, Skipped: true}
		for _, s := range a.Steps {
			if s.Address != addr {
				continue
			}
			st := state(s.ID)
			bar := applyBar{Op: s.Op, State: st, Left: pct(s.Start, a.Duration), Width: max(pct(s.Duration, a.Duration), 0.7)}
			bar.Left = min(bar.Left, 100-bar.Width)
			row.Bars = append(row.Bars, bar)
			if st != "skipped" {
				row.Skipped = false
			}
		}
		if len(row.Bars) == 0 {
			return nil, fmt.Errorf("the sample's apply has no step for %s", addr)
		}
		if end, ok := destroyed[addr]; ok {
			if _, ok := recreated[addr]; ok {
				row.Stranded, row.Gap = true, pct(end, a.Duration)
			}
		}
		f.Rows = append(f.Rows, row)
	}
	return f, nil
}

// pct is t as a percentage of total, rounded for the page.
func pct(t, total float64) float64 {
	return math.Round(t/total*1000) / 10
}

// comment is the start of the pull request comment interragate writes for
// the sample: its title, the verdict, the counts and the first findings.
type comment struct {
	Risk     string
	Title    string
	Headline template.HTML
	Counts   template.HTML
	Items    []commentItem
}

type commentItem struct {
	Severity string
	Text     template.HTML
}

var (
	marks  = map[string]string{"🔴": "critical", "🟠": "high", "🟡": "medium", "🔵": "low"}
	strong = regexp.MustCompile(`\*\*([^*]+)\*\*`)
)

func readComment(bin string) (*comment, error) {
	b, err := exec.Command(bin, "demo", "aws", "--format", "markdown", "--no-open").Output()
	if err != nil {
		return nil, fmt.Errorf("interragate demo aws --format markdown: %v", err)
	}
	// inline renders the Markdown the comment uses inside a line.
	inline := func(s string) template.HTML {
		s = strings.NewReplacer(`\[`, "[", `\]`, "]", "\u200b", "").Replace(s)
		h := template.HTMLEscapeString(s)
		h = strong.ReplaceAllString(h, "<strong>$1</strong>")
		h = codeSpan.ReplaceAllString(h, "<code>$1</code>")
		return template.HTML(h)
	}
	// mark splits the severity a line starts with from the rest of it.
	mark := func(s string) (string, string) {
		for m, sev := range marks {
			if rest, ok := strings.CutPrefix(s, m+" "); ok {
				return sev, rest
			}
		}
		return "", s
	}
	c := &comment{}
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(l, "### "):
			c.Risk, c.Title = mark(strings.TrimPrefix(l, "### "))
		case c.Headline == "" && strings.HasPrefix(l, "**") && strings.HasSuffix(l, "**"):
			c.Headline = inline(strings.Trim(l, "*"))
		case c.Headline != "" && c.Counts == "" && l != "":
			c.Counts = inline(l)
		case strings.HasPrefix(l, "- ") && len(c.Items) < 4:
			sev, text := mark(strings.TrimPrefix(l, "- "))
			c.Items = append(c.Items, commentItem{Severity: sev, Text: inline(text)})
		case l == "" && len(c.Items) > 0:
			if c.Title == "" || c.Counts == "" {
				return nil, errors.New("the sample's pull request comment no longer starts with a title, verdict and counts")
			}
			return c, nil
		}
	}
	return nil, errors.New("the sample's pull request comment has no findings")
}

var (
	sgr     = regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	escapes = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[A-Za-ln-z]`)
	colours = map[int]string{31: "red", 32: "green", 33: "yellow", 34: "blue", 35: "magenta", 36: "cyan"}
)

// terminal is the start of what interragate demo prints, in its colours.
// interragate colours its output only for a terminal, so it runs under
// script(1); where that differs (macOS), the page shows the same words
// uncoloured.
func terminal(bin string) (template.HTML, error) {
	quoted := "'" + strings.ReplaceAll(bin, "'", `'\''`) + "'"
	cmd := exec.Command("script", "-qec", "stty cols 80 rows 400; exec "+quoted+" demo aws --no-open", "/dev/null")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "NO_COLOR=")
	b, err := cmd.Output()
	if err != nil || !bytes.Contains(b, []byte("\x1b[")) {
		if b, err = exec.Command(bin, "demo", "aws", "--no-open", "--no-color").Output(); err != nil {
			return "", fmt.Errorf("interragate demo aws: %v", err)
		}
	}
	text := escapes.ReplaceAllString(strings.ReplaceAll(string(b), "\r", ""), "")
	lines := strings.Split(text, "\n")

	// Stop before the bullet that would pass 24 lines, so none is cut in two.
	end := 0
	for i, l := range lines {
		plain := sgr.ReplaceAllString(l, "")
		if i > 0 && (strings.HasPrefix(plain, "  ●") || strings.HasPrefix(plain, "  ·") || plain == "") {
			if i > 24 {
				break
			}
			end = i
		}
	}
	return ansi(strings.Join(lines[:end], "\n")), nil
}

// ansi turns text with SGR escapes into HTML spans.
func ansi(s string) template.HTML {
	var b strings.Builder
	bold, dim, fg := false, false, 0
	text := func(t string) {
		if t == "" {
			return
		}
		var class []string
		if bold {
			class = append(class, "b")
		}
		if dim {
			class = append(class, "d")
		}
		if c, ok := colours[fg]; ok {
			class = append(class, c)
		}
		if len(class) == 0 {
			b.WriteString(template.HTMLEscapeString(t))
			return
		}
		fmt.Fprintf(&b, `<span class="%s">%s</span>`, strings.Join(class, " "), template.HTMLEscapeString(t))
	}
	last := 0
	for _, m := range sgr.FindAllStringSubmatchIndex(s, -1) {
		text(s[last:m[0]])
		for _, p := range strings.Split(s[m[2]:m[3]], ";") {
			switch n, _ := strconv.Atoi(p); {
			case p == "" || n == 0:
				bold, dim, fg = false, false, 0
			case n == 1:
				bold = true
			case n == 2:
				dim = true
			case n == 22:
				bold, dim = false, false
			case n == 39:
				fg = 0
			case n >= 30 && n <= 37:
				fg = n
			case n >= 90 && n <= 97:
				fg = n - 60
			}
		}
		last = m[1]
	}
	text(s[last:])
	return template.HTML(b.String())
}

// group is the rules of one category, as the rules page lists them.
type group struct {
	ID, Name, About string
	Rules           []*rules.Rule
}

func groups(all []*rules.Rule) []group {
	about := []group{
		{ID: rules.DataLoss, Name: "Data loss", About: "Destroying or recreating what holds data, and turning off what would save it."},
		{ID: rules.Correctness, Name: "Correctness", About: "Applies that will fail partway, and plans that cannot be trusted as they are."},
		{ID: rules.Security, Name: "Security", About: "Access this change opens: to the internet, to other accounts, to everyone."},
		{ID: rules.Availability, Name: "Availability", About: "Changes that restart, disconnect or briefly remove what others depend on."},
		{ID: rules.Consumers, Name: "Outside Terraform", About: "What services you declare in .interragate.yaml, such as a site on Vercel or an app on phones, lose, see change, can no longer reach or go without while the apply runs."},
		{ID: rules.Refactor, Name: "Refactoring", About: "Renames and reshuffles that destroy and recreate instead of moving."},
		{ID: rules.Drift, Name: "Drift", About: "Changes made outside Terraform that this plan undoes."},
		{ID: rules.Hygiene, Name: "Hygiene", About: "Pinning, outputs, tags and runtimes."},
		{ID: rules.Scanner, Name: "Scanners", About: "What trivy, checkov or tflint report, kept to what this plan creates, replaces or edits."},
	}
	var out []group
	for _, g := range about {
		for _, r := range all {
			if r.Category == g.ID {
				g.Rules = append(g.Rules, r)
			}
		}
		if len(g.Rules) > 0 {
			out = append(out, g)
		}
	}
	return out
}

var (
	codeSpan = regexp.MustCompile("`([^`]+)`")
	link     = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
)

// doc renders a rule's documentation: plain text with Markdown's code spans
// and links.
func doc(s string) template.HTML {
	h := template.HTMLEscapeString(s)
	h = codeSpan.ReplaceAllString(h, "<code>$1</code>")
	h = link.ReplaceAllString(h, `<a href="$2">$1</a>`)
	return template.HTML(h)
}

// sevIcon draws a severity as the report does: octagon, triangle, diamond
// or dot, so it never rests on colour alone.
func sevIcon(sev any) template.HTML {
	shapes := map[string]string{
		"critical": `<path d="M5.2 1h5.6L15 5.2v5.6L10.8 15H5.2L1 10.8V5.2z" fill="var(--sev-critical)"/><path d="M8 4.4v4.4" stroke="#fff" stroke-width="1.8" stroke-linecap="round"/><circle cx="8" cy="11.4" r="1.05" fill="#fff"/>`,
		"high":     `<path d="M8 1.3 15.2 14H.8z" fill="var(--sev-high)"/><path d="M8 6v3.6" stroke="#0b0b0c" stroke-width="1.7" stroke-linecap="round"/><circle cx="8" cy="11.6" r=".95" fill="#0b0b0c"/>`,
		"medium":   `<path d="M8 1.2 14.8 8 8 14.8 1.2 8z" fill="var(--sev-medium)"/>`,
	}
	shape, ok := shapes[fmt.Sprint(sev)]
	if !ok {
		shape = `<circle cx="8" cy="8" r="4.6" fill="var(--sev-low)"/>`
	}
	return template.HTML(`<svg class="sev-icon" viewBox="0 0 16 16" aria-hidden="true">` + shape + `</svg>`)
}

func write(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// copyTree copies the files under src that keep accepts into dst.
func copyTree(src, dst string, keep func(name string) bool) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !keep(d.Name()) {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return write(filepath.Join(dst, rel), b)
	})
}
