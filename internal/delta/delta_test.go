package delta

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/analysis"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

func report(risk model.Severity, changes map[string]model.Action, findings ...*model.Finding) *model.Report {
	r := &model.Report{Risk: model.Risk{Level: risk}, Findings: findings}
	for addr, a := range changes {
		r.Changes = append(r.Changes, &model.Change{Address: addr, Action: a})
	}
	return r
}

func texts(d *Delta) string {
	var out []string
	for _, s := range d.Sentences {
		out = append(out, s.Text)
	}
	return strings.Join(out, "\n")
}

// rc is one resource change in a plan's JSON. Change holds more fields of
// the change, such as after_unknown, and top more fields of the resource
// change, such as previous_address.
type rc struct {
	addr, actions, before, after string
	change, top                  string
}

func (c rc) json() string {
	typ, name, _ := strings.Cut(c.addr, ".")
	more := func(s string) string {
		if s == "" {
			return ""
		}
		return "," + s
	}
	return fmt.Sprintf(`{"address":%q,"mode":"managed","type":%q,"name":%q,"provider_name":"registry.terraform.io/hashicorp/aws",`+
		`"change":{"actions":[%s],"before":%s,"after":%s,"after_unknown":{}%s}%s}`,
		c.addr, typ, name, `"`+strings.ReplaceAll(c.actions, ",", `","`)+`"`, c.before, c.after, more(c.change), more(c.top))
}

// analyze builds the report for a plan of the given changes, as interragate
// does for each plan it compares.
func analyze(t *testing.T, changes ...rc) *model.Report {
	t.Helper()
	var parts []string
	for _, c := range changes {
		parts = append(parts, c.json())
	}
	doc := `{"format_version":"1.2","terraform_version":"1.16.6","planned_values":{},"resource_changes":[` + strings.Join(parts, ",") + `]}`
	p, err := plan.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return analysis.Analyze(p, analysis.Options{})
}

// sg is a plan that opens ports to the internet on one security group.
func sg(t *testing.T, ports ...int) *model.Report {
	t.Helper()
	var rules []string
	for _, p := range ports {
		rules = append(rules, fmt.Sprintf(`{"from_port":%d,"to_port":%d,"protocol":"tcp","cidr_blocks":["0.0.0.0/0"],"description":"port %d"}`, p, p, p))
	}
	return analyze(t, rc{addr: "aws_security_group.app", actions: "update",
		before: `{"name":"app","ingress":[]}`, after: `{"name":"app","ingress":[` + strings.Join(rules, ",") + `]}`})
}

func TestCompare(t *testing.T) {
	ssh := &model.Finding{Rule: "public-ingress", Severity: model.Critical, Title: "Opens a port to the internet", Message: "Opens SSH (22/tcp) to the internet on `aws_security_group.app`. The rule is described as \"TEMP\".", Addresses: []string{"aws_security_group.app"}}
	tags := &model.Finding{Rule: "required-tags", Severity: model.Low, Title: "Missing required tags", Addresses: []string{"aws_s3_bucket.logs"}}
	before := report(model.High, map[string]model.Action{
		"aws_instance.web":       model.Update,
		"aws_s3_bucket.logs":     model.Delete,
		"aws_security_group.app": model.NoOp,
	}, tags)
	after := report(model.Critical, map[string]model.Action{
		"aws_instance.web":       model.Replace,
		"aws_security_group.app": model.Update,
		"aws_iam_role.ci":        model.Create,
	}, ssh)

	d := Compare(before, after)
	want := strings.Join([]string{
		"The verdict rose from High risk to Critical risk.",
		"New: Opens SSH (22/tcp) to the internet on `aws_security_group.app`.",
		"Fixed: Missing required tags.",
		"`aws_instance.web` is now replaced, not updated.",
		"No longer destroys `aws_s3_bucket.logs`.",
		"Now creates `aws_iam_role.ci`.",
		"Now updates `aws_security_group.app`.",
	}, "\n")
	if got := texts(d); got != want {
		t.Errorf("sentences:\n%s\nwant:\n%s", got, want)
	}
	if d.Empty() {
		t.Error("a delta with changes is not empty")
	}
}

func TestCompareSame(t *testing.T) {
	r := report(model.Medium, map[string]model.Action{"aws_instance.web": model.Update})
	d := Compare(r, r)
	if !d.Empty() || texts(d) != "The plan is the same as before." {
		t.Errorf("delta = %+v", d)
	}
}

// The verdict is told in the words the report gives it, and info and low
// are both "Low risk", so a change between them is no change of verdict.
func TestVerdictWords(t *testing.T) {
	for _, c := range []struct {
		before, after model.Severity
		want          string
		sev           model.Severity
	}{
		{model.High, model.Critical, "The verdict rose from High risk to Critical risk.", model.Critical},
		{model.None, model.Info, "The verdict rose from No changes to Low risk.", model.Info},
		{model.Info, model.None, "The verdict fell from Low risk to No changes.", model.None},
		{model.Critical, model.Low, "The verdict fell from Critical risk to Low risk.", model.None},
		{model.Info, model.Low, "The plan is the same as before.", model.None},
	} {
		d := Compare(report(c.before, nil), report(c.after, nil))
		if len(d.Sentences) != 1 || d.Sentences[0].Text != c.want || d.Sentences[0].Severity != c.sev {
			t.Errorf("%s to %s: %+v, want %q (%s)", c.before, c.after, d.Sentences, c.want, c.sev)
		}
	}
}

// Finding IDs are positional, so a finding is the same finding when it
// comes from the same rule about the same resources, in any order.
func TestFindingIdentity(t *testing.T) {
	a := &model.Finding{ID: "x#1", Rule: "x", Title: "X", Addresses: []string{"b", "a"}}
	b := &model.Finding{ID: "x#2", Rule: "x", Title: "X", Addresses: []string{"a", "b"}}
	d := Compare(report(model.Low, nil, a), report(model.Low, nil, b))
	if len(d.Found) != 0 || len(d.Fixed) != 0 {
		t.Errorf("found %v, fixed %v", d.Found, d.Fixed)
	}
}

// Two findings from one rule about one resource are two findings: opening
// RDP next to SSH is new, even though the rule and the resource are the
// same, and interragate diff --fail-on critical must see it.
func TestFindingsOnOneResource(t *testing.T) {
	ssh, both, rdp := sg(t, 22), sg(t, 22, 3389), sg(t, 3389)

	d := Compare(ssh, both)
	if len(d.Found) != 1 || d.Found[0].Severity != model.Critical || len(d.Fixed) != 0 {
		t.Fatalf("found %+v, fixed %+v", d.Found, d.Fixed)
	}
	if got, want := d.Sentences[0].Text, "New: Opens RDP (3389/tcp) to the internet on `aws_security_group.app`."; got != want {
		t.Errorf("first sentence %q, want %q", got, want)
	}

	// Swapping one port for another fixes one finding and brings another.
	d = Compare(ssh, rdp)
	if len(d.Found) != 1 || !strings.Contains(d.Found[0].Text, "RDP") || len(d.Fixed) != 1 || !strings.Contains(d.Fixed[0].Text, "SSH") {
		t.Errorf("SSH for RDP: found %+v, fixed %+v", d.Found, d.Fixed)
	}

	// Closing one of them is a fix, not news.
	d = Compare(both, ssh)
	if len(d.Found) != 0 || len(d.Fixed) != 1 || !strings.Contains(d.Fixed[0].Text, "RDP") {
		t.Errorf("closing RDP: found %+v, fixed %+v", d.Found, d.Fixed)
	}
	if d := Compare(both, sg(t, 22, 3389)); len(d.Found)+len(d.Fixed) != 0 {
		t.Errorf("the same plan again: found %+v, fixed %+v", d.Found, d.Fixed)
	}
}

// A finding stays the same one when an unrelated change rewords it (a
// count, the tool that planned it), but not when it names something else.
func TestFindingRewording(t *testing.T) {
	blast := func(n int) *model.Finding {
		return &model.Finding{Rule: "large-blast-radius", Severity: model.Medium, Title: "Many resources depend on what is replaced",
			Message: fmt.Sprintf("%d resources depend on `aws_vpc.main`, which is replaced: `aws_subnet.a`, `aws_subnet.b` and %d more.", n, n-2), Addresses: []string{"aws_vpc.main"}}
	}
	drift := func(tool string) *model.Finding {
		return &model.Finding{Rule: "drift-revert", Severity: model.High, Title: "Reverts a change made outside " + tool,
			Message: "`aws_lb.web` was changed outside " + tool + " (`tags.Owner` added), and this plan reverts it.", Addresses: []string{"aws_lb.web"}}
	}
	wildcard := func(action string) *model.Finding {
		return &model.Finding{Rule: "iam-wildcard", Severity: model.High, Title: "Grants wildcard permissions",
			Message: "Adds a statement to `policy` on `aws_iam_role_policy.app` that allows `" + action + "` on every resource.", Addresses: []string{"aws_iam_role_policy.app"}}
	}
	for _, c := range []struct {
		name          string
		before, after *model.Finding
		same          bool
	}{
		{"a count", blast(14), blast(15), true},
		{"the tool", drift("Terraform"), drift("OpenTofu"), true},
		{"another statement", wildcard("s3:*"), wildcard("iam:*"), false},
	} {
		d := Compare(report(model.High, nil, c.before), report(model.High, nil, c.after))
		if same := len(d.Found) == 0 && len(d.Fixed) == 0; same != c.same {
			t.Errorf("%s: found %+v, fixed %+v", c.name, d.Found, d.Fixed)
		}
	}

	// The same words twice are two findings, as for SSH opened to both
	// IPv4 and IPv6.
	open := &model.Finding{Rule: "public-ingress", Severity: model.Critical, Title: "Opens SSH (22/tcp) to the internet", Message: "Opens SSH (22/tcp) to the internet on `x`.", Addresses: []string{"x"}}
	twice := &model.Finding{Rule: "public-ingress", Severity: model.Critical, Title: "Opens SSH (22/tcp) to the internet", Message: "Opens SSH (22/tcp) to the internet on `x`.", Addresses: []string{"x"}}
	if d := Compare(report(model.Critical, nil, open), report(model.Critical, nil, open, twice)); len(d.Found) != 1 || len(d.Fixed) != 0 {
		t.Errorf("twice: found %+v, fixed %+v", d.Found, d.Fixed)
	}
}

// A finding the later plan rates more severe counts as found, for
// --fail-on and in the sentences; one rated less severe is not news.
func TestFindingSeverityRose(t *testing.T) {
	port := func(sev model.Severity) *model.Finding {
		return &model.Finding{Rule: "public-ingress", Severity: sev, Title: "Opens port 8080/tcp to the internet", Message: "Opens port 8080/tcp to the internet on `aws_security_group.app`.", Addresses: []string{"aws_security_group.app"}}
	}
	d := Compare(report(model.High, nil, port(model.High)), report(model.Critical, nil, port(model.Critical)))
	if len(d.Found) != 1 || d.Found[0].Severity != model.Critical || d.Found[0].Was != model.High || len(d.Fixed) != 0 {
		t.Fatalf("found %+v, fixed %+v", d.Found, d.Fixed)
	}
	want := "The verdict rose from High risk to Critical risk.\nNow critical, not high: Opens port 8080/tcp to the internet on `aws_security_group.app`."
	if got := texts(d); got != want {
		t.Errorf("sentences:\n%s\nwant:\n%s", got, want)
	}
	if d.Sentences[1].Severity != model.Critical {
		t.Errorf("severity %s", d.Sentences[1].Severity)
	}
	b, _ := json.Marshal(d.Found[0])
	if !strings.Contains(string(b), `"severity":"critical","was":"high"`) {
		t.Errorf("JSON %s", b)
	}

	d = Compare(report(model.Critical, nil, port(model.Critical)), report(model.Critical, nil, port(model.High)))
	if len(d.Found) != 0 || len(d.Fixed) != 0 || texts(d) != "The plan is the same as before." {
		t.Errorf("lowered: found %+v, fixed %+v, sentences %s", d.Found, d.Fixed, texts(d))
	}
	if b, _ := json.Marshal(Compare(report(model.Low, nil), report(model.High, nil, port(model.High))).Found[0]); strings.Contains(string(b), "was") {
		t.Errorf("a new finding has no earlier severity: %s", b)
	}
}

// deploy is a plan that updates terraform_data.deploy's input from v1.
func deploy(t *testing.T, input string) *model.Report {
	t.Helper()
	return deployFrom(t, `"v1"`, input)
}

func deployFrom(t *testing.T, was, input string) *model.Report {
	t.Helper()
	return analyze(t, rc{addr: "terraform_data.deploy", actions: "update",
		before: `{"id":"d1","input":` + was + `,"output":` + was + `}`, after: `{"id":"d1","input":` + input + `}`, change: `"after_unknown":{"output":true}`})
}

// Two plans that do the same things to the same resources can still set
// different values; the delta says which, briefly, rather than that the
// plan is the same as before.
func TestCompareValues(t *testing.T) {
	d := Compare(deploy(t, `"v2"`), deploy(t, `"v3"`))
	if got, want := texts(d), "`terraform_data.deploy` now sets `input` to \"v3\"."; got != want {
		t.Errorf("sentences:\n%s\nwant:\n%s", got, want)
	}
	if d.Empty() || len(d.Changes) != 1 || d.Changes[0].Before != model.Update || d.Changes[0].After != model.Update || strings.Join(d.Changes[0].Attributes, ",") != "input" {
		t.Errorf("changes %+v", d.Changes)
	}
	if d.Sentences[0].Severity != model.Info || strings.Join(d.Sentences[0].Refs, ",") != "terraform_data.deploy" {
		t.Errorf("sentence %+v", d.Sentences[0])
	}
	b, _ := json.Marshal(d.Changes)
	if string(b) != `[{"address":"terraform_data.deploy","before":"update","after":"update","attributes":["input"]}]` {
		t.Errorf("JSON %s", b)
	}
	if d := Compare(deploy(t, `"v2"`), deploy(t, `"v2"`)); !d.Empty() || texts(d) != "The plan is the same as before." {
		t.Errorf("the same plan: %s", texts(d))
	}

	// Objects open up to the value that changed; lists are told whole.
	was := `{"size":1,"zone":"a"}`
	d = Compare(deployFrom(t, was, `{"size":2,"zone":"a"}`), deployFrom(t, was, `{"size":99,"zone":"a"}`))
	if got, want := texts(d), "`terraform_data.deploy` now sets `input.size` to 99."; got != want {
		t.Errorf("object: %s", got)
	}
	d = Compare(deployFrom(t, `["a"]`, `["a","b"]`), deployFrom(t, `["a"]`, `["a","c"]`))
	if got, want := texts(d), "`terraform_data.deploy` now sets `input` differently."; got != want {
		t.Errorf("list: %s", got)
	}
	// A value whose type changes is told whole, as the report shows it.
	d = Compare(deploy(t, `["a","b"]`), deploy(t, `["b","a"]`))
	if got, want := texts(d), "`terraform_data.deploy` now sets `input` to [\"b\",\"a\"]."; got != want {
		t.Errorf("new type: %s", got)
	}

	// A long value is cut short and keeps its quotes; backticks in it
	// cannot break the markup.
	d = Compare(deploy(t, `"v2"`), deploy(t, `"`+strings.Repeat("x", 60)+"`"+`"`))
	if got, want := texts(d), "`terraform_data.deploy` now sets `input` to \""+strings.Repeat("x", 39)+"…\"."; got != want {
		t.Errorf("long: %s", got)
	}
	d = Compare(deploy(t, `"v2"`), deploy(t, "\"a`b\""))
	if got := texts(d); got != "`terraform_data.deploy` now sets `input` to \"a'b\"." {
		t.Errorf("backtick: %s", got)
	}
}

// What only the earlier plan changes, the later one no longer changes.
// Values are compared as planned: two plans that make the same thing from
// different state are the same.
func TestCompareValuesLeftAlone(t *testing.T) {
	plan := func(before, after string) *model.Report {
		return analyze(t, rc{addr: "aws_instance.web", actions: "update", before: before, after: after})
	}
	both := plan(`{"a":1,"b":1}`, `{"a":2,"b":2}`)
	for _, c := range []struct{ name, before, after, want string }{
		{"one left alone", `{"a":1,"b":1}`, `{"a":2,"b":1}`, "`aws_instance.web` no longer changes `b`."},
		{"one set, one left alone", `{"a":1,"b":1}`, `{"a":3,"b":1}`, "`aws_instance.web` now sets `a` to 3 and no longer changes `b`."},
		{"a new value and a removed one", `{"a":1,"b":1}`, `{"a":2,"b":2,"c":null,"d":4}`, "`aws_instance.web` now sets `d` to 4."},
		{"other state, the same result", `{"a":0,"b":0}`, `{"a":2,"b":2}`, "The plan is the same as before."},
	} {
		if got := texts(Compare(both, plan(c.before, c.after))); got != c.want {
			t.Errorf("%s: %s\nwant %s", c.name, got, c.want)
		}
	}
	if got, want := texts(Compare(plan(`{"a":1,"c":1}`, `{"a":2,"c":1}`), plan(`{"a":1,"c":1}`, `{"a":2,"c":null}`))), "`aws_instance.web` now sets `c` to null."; got != want {
		t.Errorf("removed: %s", got)
	}

	// Elements of a list are told apart by what they were, so dropping one
	// element is not the same as dropping another.
	list := func(after string) *model.Report { return plan(`{"l":["a","b"]}`, `{"l":`+after+`}`) }
	if got := texts(Compare(list(`["a"]`), list(`["b"]`))); got != "`aws_instance.web` now sets `l` differently." {
		t.Errorf("list: %s", got)
	}
}

// A value that becomes known only at apply, or is sensitive, is told the
// way the report shows it, and a secret never shows.
func TestCompareValuesMasked(t *testing.T) {
	unknown := analyze(t, rc{addr: "aws_instance.web", actions: "update", before: `{"ami":"a","arn":"arn:1"}`, after: `{"ami":"b"}`, change: `"after_unknown":{"arn":true}`})
	known := analyze(t, rc{addr: "aws_instance.web", actions: "update", before: `{"ami":"a","arn":"arn:1"}`, after: `{"ami":"b","arn":"arn:1"}`})
	if got, want := texts(Compare(known, unknown)), "`aws_instance.web` now sets `arn` to (known after apply)."; got != want {
		t.Errorf("unknown: %s", got)
	}

	secret := func(after string) *model.Report {
		return analyze(t, rc{addr: "aws_ssm_parameter.key", actions: "update", before: `{"value":"s3cr3t-zero","note":"a"}`, after: after,
			change: `"before_sensitive":{"value":true},"after_sensitive":{"value":true}`})
	}
	d := Compare(secret(`{"value":"s3cr3t-zero","note":"b"}`), secret(`{"value":"s3cr3t-two","note":"copy of s3cr3t-two"}`))
	if got, want := texts(d), "`aws_ssm_parameter.key` now sets `note` to \"copy of (sensitive value)\" and `value` to (sensitive value)."; got != want {
		t.Errorf("sensitive: %s\nwant %s", got, want)
	}
	if b, _ := json.Marshal(d); strings.Contains(string(b), "s3cr3t") {
		t.Errorf("the delta shows a secret: %s", b)
	}
	// The report cannot tell two sensitive values apart, and nor can the delta.
	if d := Compare(secret(`{"value":"s3cr3t-one","note":"a"}`), secret(`{"value":"s3cr3t-two","note":"a"}`)); !d.Empty() {
		t.Errorf("two secrets: %s", texts(d))
	}
}

// Resources that now set the same values are told together, and the
// summary stays capped.
func TestCompareValuesGrouped(t *testing.T) {
	tagged := func(env string, addrs ...string) *model.Report {
		var cs []rc
		for _, a := range addrs {
			cs = append(cs, rc{addr: a, actions: "update", before: `{"tags":{"env":"dev"}}`, after: `{"tags":{"env":"` + env + `"}}`})
		}
		return analyze(t, cs...)
	}
	d := Compare(tagged("test", "aws_instance.a", "aws_instance.b"), tagged("prod", "aws_instance.a", "aws_instance.b"))
	if got, want := texts(d), "`aws_instance.a` and `aws_instance.b` now set `tags.env` to \"prod\"."; got != want {
		t.Errorf("grouped: %s", got)
	}

	var before, after []rc
	for i := range 12 {
		addr := fmt.Sprintf("aws_instance.n%02d", i)
		before = append(before, rc{addr: addr, actions: "update", before: `{"size":1}`, after: `{"size":2}`})
		after = append(after, rc{addr: addr, actions: "update", before: `{"size":1}`, after: fmt.Sprintf(`{"size":%d}`, 10+i)})
	}
	d = Compare(analyze(t, before...), analyze(t, after...))
	if len(d.Sentences) != maxSentences || d.Sentences[maxSentences-1].Text != "And 5 more differences." {
		t.Errorf("sentences = %s", texts(d))
	}
}

// A replacement that changes order says which way round it now goes.
// Creating first avoids the gap; destroying first opens one.
func TestCompareOrder(t *testing.T) {
	replaced := func(order string, addrs ...string) *model.Report {
		r := &model.Report{}
		for _, a := range addrs {
			r.Changes = append(r.Changes, &model.Change{Address: a, Action: model.Replace, Order: order, Severity: model.Low})
		}
		return r
	}
	d := Compare(replaced("delete-first", "aws_lb.main"), replaced("create-first", "aws_lb.main"))
	if got, want := texts(d), "Now creates the replacement for `aws_lb.main` before destroying the old one."; got != want {
		t.Errorf("create first: %s", got)
	}
	if d.Sentences[0].Severity != model.None || len(d.Changes) != 1 || d.Changes[0].Order != "create-first" || d.Empty() {
		t.Errorf("create first: %+v", d)
	}
	d = Compare(replaced("create-first", "aws_lb.main"), replaced("delete-first", "aws_lb.main"))
	if got, want := texts(d), "Now destroys `aws_lb.main` before creating its replacement."; got != want {
		t.Errorf("delete first: %s", got)
	}
	if d.Sentences[0].Severity < model.Low {
		t.Errorf("destroying first is bad news: %s", d.Sentences[0].Severity)
	}
	d = Compare(replaced("delete-first", "aws_lb.a", "aws_lb.b"), replaced("create-first", "aws_lb.a", "aws_lb.b"))
	if got, want := texts(d), "Now creates the replacements for `aws_lb.a` and `aws_lb.b` before destroying the old ones."; got != want {
		t.Errorf("two: %s", got)
	}
}

// A move or import both plans make is not news, and a sentence does not
// say the resource is no longer moved when it still is.
func TestMovedAndImported(t *testing.T) {
	moved := func(a model.Action) *model.Report {
		return &model.Report{Changes: []*model.Change{{Address: "aws_s3_bucket.new", PrevAddress: "aws_s3_bucket.old", Moved: true, Action: a}}}
	}
	imported := func(a model.Action) *model.Report {
		return &model.Report{Changes: []*model.Change{{Address: "aws_s3_bucket.imp", Importing: true, Action: a}}}
	}
	plain := func(a model.Action) *model.Report {
		return &model.Report{Changes: []*model.Change{{Address: "aws_s3_bucket.new", Action: a}}}
	}
	for _, c := range []struct {
		name          string
		before, after *model.Report
		want          string
	}{
		{"moved, now updated too", moved(model.NoOp), moved(model.Update), "`aws_s3_bucket.new` is now updated as well as moved."},
		{"moved, no longer updated", moved(model.Update), moved(model.NoOp), "`aws_s3_bucket.new` is no longer updated, only moved."},
		{"moved, now replaced", moved(model.Update), moved(model.Replace), "`aws_s3_bucket.new` is now replaced, not updated."},
		{"the moved block dropped", moved(model.NoOp), plain(model.Create), "`aws_s3_bucket.new` is now created, not moved."},
		{"newly moved", &model.Report{}, moved(model.NoOp), "Now moves `aws_s3_bucket.new`."},
		{"no longer moved", moved(model.Update), &model.Report{}, "No longer moves and updates `aws_s3_bucket.new`."},
		{"imported, now updated too", imported(model.NoOp), imported(model.Update), "`aws_s3_bucket.imp` is now updated as well as imported."},
		{"imported, no longer updated", imported(model.Update), imported(model.NoOp), "`aws_s3_bucket.imp` is no longer updated, only imported."},
	} {
		if got := texts(Compare(c.before, c.after)); got != c.want {
			t.Errorf("%s: %s\nwant %s", c.name, got, c.want)
		}
	}

	// A resource moved in both plans still has its values compared.
	movedSize := func(size int) *model.Report {
		return analyze(t, rc{addr: "aws_s3_bucket.new", actions: "update", before: `{"size":1}`, after: fmt.Sprintf(`{"size":%d}`, size), top: `"previous_address":"aws_s3_bucket.old"`})
	}
	if got, want := texts(Compare(movedSize(2), movedSize(99))), "`aws_s3_bucket.new` now sets `size` to 99."; got != want {
		t.Errorf("moved and resized: %s", got)
	}
}

// follower is a change that only follows from cause.
func follower(addr string, a model.Action, cause string) *model.Change {
	return &model.Change{Address: addr, Action: a, Indirect: true, Causes: []model.Cause{{Attribute: "triggers_replace", Resource: cause, Address: cause, Action: model.Replace}}}
}

// One edit that replaces a resource replaces what reads it too; the delta
// says so in one sentence, cause first.
func TestCompareGroupsFollowers(t *testing.T) {
	before := report(model.Low, nil)
	after := &model.Report{Risk: model.Risk{Level: model.Low}, Changes: []*model.Change{
		{Address: "random_pet.cluster", Action: model.Replace},
		follower("local_file.config", model.Replace, "random_pet.cluster"),
		follower("null_resource.worker[0]", model.Replace, "random_pet.cluster"),
		follower("null_resource.worker[1]", model.Replace, "random_pet.cluster"),
		follower("terraform_data.deploy", model.Update, "random_pet.cluster"),
	}}
	want := strings.Join([]string{
		"Now replaces `random_pet.cluster` and 3 more that follow from it.",
		"Now updates `terraform_data.deploy`.",
	}, "\n")
	if got := texts(Compare(before, after)); got != want {
		t.Errorf("sentences:\n%s\nwant:\n%s", got, want)
	}

	// With more followers than are named, the count says where they come from.
	after.Changes = append(after.Changes, follower("null_resource.worker[2]", model.Replace, "random_pet.cluster"))
	got := texts(Compare(before, after))
	if !strings.HasPrefix(got, "Now replaces `random_pet.cluster` and 4 more that follow from it.") {
		t.Errorf("sentences:\n%s", got)
	}
}

// A sentence only names a cause the others follow from. Here the followers
// follow from terraform_data.root, which has a sentence of its own, so the
// one other resource replaced alongside them is not their cause.
func TestCompareNamesTheRightCause(t *testing.T) {
	before := &model.Report{Changes: []*model.Change{{Address: "terraform_data.root", Action: model.Update}}}
	after := &model.Report{Changes: []*model.Change{
		{Address: "terraform_data.root", Action: model.Replace},
		follower("terraform_data.f1", model.Replace, "terraform_data.root"),
		follower("terraform_data.f2", model.Replace, "terraform_data.root"),
		{Address: "terraform_data.other", Action: model.Replace},
	}}
	want := strings.Join([]string{
		"`terraform_data.root` is now replaced, not updated.",
		"Now replaces `terraform_data.other`, `terraform_data.f1` and `terraform_data.f2`.",
	}, "\n")
	if got := texts(Compare(before, after)); got != want {
		t.Errorf("sentences:\n%s\nwant:\n%s", got, want)
	}
}

func TestNames(t *testing.T) {
	l := &lineage{
		follows: map[string]bool{"b": true, "c": true, "d": true, "e": true, "f": true},
		causes:  map[string][]string{"b": {"a"}, "c": {"a"}, "d": {"x"}, "e": {"c"}, "f": {"elsewhere"}},
	}
	g := []Change{{Address: "a"}, {Address: "b"}, {Address: "c"}, {Address: "d"}}
	for _, c := range []struct {
		name string
		g    []Change
		l    *lineage
		want string
	}{
		{"one", g[:1], l, "`a`"},
		{"two", g[:2], l, "`a` and `b`"},
		{"a cause and followers", g[:3], l, "`a` and 2 more that follow from it"},
		{"followers through another", []Change{{Address: "a"}, {Address: "b"}, {Address: "e"}}, l, "`a` and 2 more that follow from it"},
		{"two causes", []Change{{Address: "a"}, {Address: "x"}, {Address: "b"}, {Address: "d"}}, l, "`a`, `x` and 2 more that follow from them"},
		{"a cause they do not follow from", []Change{{Address: "x"}, {Address: "b"}, {Address: "c"}}, l, "`x`, `b` and `c`"},
		{"one does not follow", []Change{{Address: "a"}, {Address: "b"}, {Address: "f"}}, l, "`a`, `b` and `f`"},
		{"no followers", []Change{{Address: "a"}, {Address: "x"}, {Address: "y"}, {Address: "z"}}, l, "`a`, `x` and 2 more"},
		{"three", []Change{{Address: "x"}, {Address: "y"}, {Address: "z"}}, l, "`x`, `y` and `z`"},
		{"no lineage", g[:3], nil, "`a`, `b` and `c`"},
	} {
		if got := names(c.g, c.l); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	// A cycle explains nothing and ends.
	cycle := &lineage{follows: map[string]bool{"p": true, "q": true}, causes: map[string][]string{"p": {"q"}, "q": {"p"}}}
	if cycle.from("p", "z") {
		t.Error("a cycle explained p")
	}
}

func TestCompareCapsSentences(t *testing.T) {
	var fs []*model.Finding
	for _, c := range "abcdefghijkl" {
		fs = append(fs, &model.Finding{Rule: "r", Title: "T", Message: "Finding " + string(c) + ".", Addresses: []string{string(c)}})
	}
	d := Compare(report(model.Low, nil), report(model.Low, nil, fs...))
	if len(d.Sentences) != maxSentences || d.Sentences[maxSentences-1].Text != "And 5 more differences." {
		t.Errorf("sentences = %s", texts(d))
	}
}

// A moved block added since the earlier plan is one change to one
// resource, not a change that went away and another that came.
func TestNewMovedBlock(t *testing.T) {
	before := &model.Report{Changes: []*model.Change{{Address: "aws_s3_bucket.old", Action: model.Update}}}
	after := &model.Report{Changes: []*model.Change{{Address: "aws_s3_bucket.new", PrevAddress: "aws_s3_bucket.old", Moved: true, Action: model.Update}}}
	got := texts(Compare(before, after))
	if strings.Contains(got, "No longer") || !strings.Contains(got, "aws_s3_bucket.new") {
		t.Errorf("%s", got)
	}
}

// The same destroy from different state is the same plan.
func TestSameDestroy(t *testing.T) {
	destroy := func(tags string) *model.Report {
		return analyze(t, rc{addr: "aws_s3_bucket.logs", actions: "delete", before: `{"bucket":"logs","tags":` + tags + `}`, after: "null"})
	}
	if d := Compare(destroy(`{"a":"1"}`), destroy(`null`)); !d.Empty() {
		t.Errorf("%s", texts(d))
	}
}

// Outputs count: a plan that changes only an output is not the same plan.
func TestOutputs(t *testing.T) {
	with := func(outputs ...*model.Output) *model.Report { return &model.Report{Outputs: outputs} }
	d := Compare(with(&model.Output{Name: "url", Action: model.NoOp}), with(&model.Output{Name: "url", Action: model.Update}))
	if d.Empty() || texts(d) != "Now changes output `url`." {
		t.Errorf("%s", texts(d))
	}
	d = Compare(with(&model.Output{Name: "url", Action: model.Update}), with())
	if texts(d) != "No longer changes output `url`." {
		t.Errorf("%s", texts(d))
	}
}
