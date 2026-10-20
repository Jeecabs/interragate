package consumers

import (
	"slices"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/access"
	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/graph"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

// fakePlan is a plan built in a test: changes with their raw values, and
// the dependency graph between their resources.
type fakePlan struct {
	report *model.Report
	raw    map[*model.Change]*plan.ResourceChange
	graph  *graph.Graph
}

func newPlan() *fakePlan {
	return &fakePlan{report: &model.Report{}, raw: map[*model.Change]*plan.ResourceChange{}, graph: &graph.Graph{Nodes: map[string]*graph.Node{}, Outputs: map[string][]graph.Ref{}}}
}

func (p *fakePlan) Raw(ch *model.Change) *plan.ResourceChange { return p.raw[ch] }
func (p *fakePlan) DriftFor(string) *model.Drift              { return nil }

// add puts a change in the plan; its address is its resource's.
func (p *fakePlan) add(address, typ string, action model.Action, before, after map[string]any) *model.Change {
	ch := &model.Change{Address: address, Resource: address, Type: typ, Mode: "managed", Action: action}
	var b, a any
	if before != nil {
		b = before
	}
	if after != nil {
		a = after
	}
	p.raw[ch] = &plan.ResourceChange{Change: plan.Change{Before: b, After: a}}
	p.report.Changes = append(p.report.Changes, ch)
	p.graph.Nodes[address] = &graph.Node{ID: address, Type: typ}
	return ch
}

// refer records that from refers to to.
func (p *fakePlan) refer(from, to string) {
	p.graph.Nodes[from].Up = append(p.graph.Nodes[from].Up, to)
	p.graph.Nodes[to].Down = append(p.graph.Nodes[to].Down, from)
}

func (p *fakePlan) assess(services ...config.Consumer) *Assessment {
	return Assess(Input{Services: services, File: ".interragate.yaml", Report: p.report, Plan: &plan.Plan{}, Graph: p.graph, View: p})
}

func service(name string, uses ...config.Use) config.Consumer {
	return config.Consumer{Name: model.ServiceName(name), Uses: uses}
}

func host(h string) config.Use { return config.Use{Target: config.HostTarget{Host: config.Host(h)}} }

func TestUpdatePathOf(t *testing.T) {
	for _, c := range []struct {
		platform string
		update   model.UpdatePath
		as       string
		want     model.UpdatePath
	}{
		{"vercel", model.UpdateUnknown, "DATABASE_URL", model.UpdateRedeploy},
		{"Vercel", model.UpdateUnknown, "NEXT_PUBLIC_API_URL", model.UpdateRebuild},
		{"netlify", model.UpdateUnknown, "VITE_API", model.UpdateRebuild},
		{"expo", model.UpdateUnknown, "EXPO_PUBLIC_API_URL", model.UpdateOTA},
		{"expo", model.UpdateUnknown, "", model.UpdateStore},
		{"ios", model.UpdateUnknown, "API_URL", model.UpdateStore},
		{"partner", model.UpdateUnknown, "", model.UpdateManual},
		{"vercel", model.UpdateLive, "NEXT_PUBLIC_API_URL", model.UpdateLive},
		{"somewhere", model.UpdateUnknown, "X", model.UpdateUnknown},
	} {
		svc := config.Consumer{Platform: c.platform, Update: c.update}
		if got := UpdatePathOf(&svc, config.Use{As: c.as}); got != c.want {
			t.Errorf("%s/%v/%s: update = %v, want %v", c.platform, c.update, c.as, got, c.want)
		}
	}
}

// Only records that resolve a host answer for it, and a record's other
// settings do not move the host.
func TestHosts(t *testing.T) {
	p := newPlan()
	p.add("aws_route53_record.verify", "aws_route53_record", model.Update,
		map[string]any{"name": "www.example.com", "type": "TXT", "records": []any{"token-1"}},
		map[string]any{"name": "www.example.com", "type": "TXT", "records": []any{"token-2"}})
	p.add("aws_route53_record.www", "aws_route53_record", model.Update,
		map[string]any{"name": "www.example.com", "type": "A", "alias": []any{map[string]any{"name": "lb-1.aws", "zone_id": "Z1", "evaluate_target_health": false}}},
		map[string]any{"name": "www.example.com", "type": "A", "alias": []any{map[string]any{"name": "lb-1.aws", "zone_id": "Z1", "evaluate_target_health": true}}})
	p.add("aws_route53_record.api", "aws_route53_record", model.Update,
		map[string]any{"name": "*.example.com", "type": "CNAME", "records": []any{"old.example.net"}},
		map[string]any{"name": "*.example.com", "type": "CNAME", "records": []any{"new.example.net"}})
	p.add("aws_route53_record.old", "aws_route53_record", model.Update,
		map[string]any{"name": "shop.example.com", "type": "CNAME", "records": []any{"x"}},
		map[string]any{"name": "store.example.com", "type": "CNAME", "records": []any{"x"}})

	a := p.assess(service("web", host("www.example.com")), service("api", host("api.example.com")), service("shop", host("shop.example.com")))
	if m := a.Matches()[0]; len(m.Changes) != 1 || m.Changes[0].Address != "aws_route53_record.www" {
		t.Errorf("www is answered by %v, want only its A record", m.Changes)
	}
	var got []string
	for _, im := range a.Impacts() {
		got = append(got, string(im.Of().Service.Name)+" "+im.Kind().String()+" "+im.Subject())
	}
	want := []string{"api value aws_route53_record.api", "shop loses aws_route53_record.old"}
	if !slices.Equal(got, want) {
		t.Errorf("impacts = %q, want %q (a health check flag does not move www)", got, want)
	}
	if rp, ok := a.Impacts()[0].(*Repoint); !ok || rp.Update() != model.UpdateLive || levelFor(rp) != model.Low {
		t.Errorf("a new address for a host is live and low: %#v", a.Impacts()[0])
	}
	if loss := a.Impacts()[1].(*Loss); !loss.Unanswered {
		t.Error("a renamed record stops answering for the host without being destroyed")
	}
}

// A port closes for every range that loses it, not only one declared.
func TestClosure(t *testing.T) {
	p := newPlan()
	p.add("aws_security_group.alb", "aws_security_group", model.Update,
		map[string]any{"ingress": []any{
			map[string]any{"protocol": "tcp", "from_port": 8443.0, "to_port": 8443.0, "cidr_blocks": []any{"0.0.0.0/0"}, "ipv6_cidr_blocks": []any{"::/0"}},
		}},
		map[string]any{"ingress": []any{
			map[string]any{"protocol": "tcp", "from_port": 8443.0, "to_port": 8443.0, "cidr_blocks": []any{"0.0.0.0/0"}},
		}})
	port, _ := access.ParsePort("8443/tcp")
	reach := func(from string) config.Use {
		t := config.ReachTarget{Firewall: "aws_security_group.alb", Port: port}
		if from != "" {
			t.From = access.SourceOf(from)
		}
		return config.Use{Target: t}
	}
	a := p.assess(service("anywhere", reach("")), service("v4 partner", reach("198.51.100.7")), service("v6 partner", reach("2001:db8::1")))
	var got []string
	for _, im := range a.Impacts() {
		c := im.(*Closure)
		got = append(got, string(im.Of().Service.Name)+" "+c.from())
	}
	want := []string{"anywhere  from `::/0`", "v6 partner  from `2001:db8::1/128`"}
	if !slices.Equal(got, want) {
		t.Errorf("closures = %q, want %q", got, want)
	}
}

// Deposed objects and forgotten ones are not lost.
func TestNotLost(t *testing.T) {
	p := newPlan()
	dep := p.add("aws_lb.web", "aws_lb", model.Delete, map[string]any{"dns_name": "lb.aws"}, nil)
	dep.Deposed, dep.Address = "abcd", "aws_lb.web (deposed object abcd)"
	p.add("aws_security_group.alb", "aws_security_group", model.Forget, map[string]any{"ingress": []any{
		map[string]any{"protocol": "tcp", "from_port": 443.0, "to_port": 443.0, "cidr_blocks": []any{"0.0.0.0/0"}},
	}}, nil)
	port, _ := access.ParsePort("443")
	a := p.assess(
		service("lb", config.Use{Target: config.ResourceTarget{Address: "aws_lb.*"}}),
		service("partner", config.Use{Target: config.ReachTarget{Firewall: "aws_security_group.alb", Port: port}}),
	)
	if len(a.Impacts()) != 0 {
		t.Errorf("impacts = %+v", a.Impacts())
	}
	if a.Matches()[0].Found() {
		t.Error("a deposed object is not what the service uses")
	}
}

func TestWindowOverlap(t *testing.T) {
	for _, c := range []struct {
		a, b, want Window
		ok         bool
	}{
		{Window{From: 0, To: 10}, Window{From: 5, To: 20}, Window{From: 5, To: 10}, true},
		{Window{From: 0, To: 10}, Window{From: 10, To: 20}, Window{From: 10, To: 10}, false},
		{Window{From: 5, Open: true}, Window{From: 0, To: 8}, Window{From: 5, To: 8}, true},
		{Window{From: 5, Open: true}, Window{From: 7, Open: true}, Window{From: 7, Open: true}, true},
	} {
		if got, ok := c.a.overlap(c.b); ok != c.ok || ok && got != c.want {
			t.Errorf("%+v overlap %+v = %+v, %v; want %+v, %v", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

// A host behind a load balancer has no target only while every target
// group behind it is empty, and the same plan always gives the same
// outage.
func TestNoTarget(t *testing.T) {
	build := func(adminReplaced bool) (*fakePlan, Timeline) {
		p := newPlan()
		p.add("aws_route53_record.www", "aws_route53_record", model.NoOp,
			map[string]any{"name": "www.example.com", "type": "CNAME"}, map[string]any{"name": "www.example.com", "type": "CNAME"})
		p.add("aws_lb.web", "aws_lb", model.NoOp, nil, nil)
		p.add("aws_lb_listener.http", "aws_lb_listener", model.NoOp, nil, nil)
		p.add("aws_lb_listener.admin", "aws_lb_listener", model.NoOp, nil, nil)
		p.add("aws_lb_target_group.app", "aws_lb_target_group", model.NoOp, nil, nil)
		p.add("aws_lb_target_group.admin", "aws_lb_target_group", model.NoOp, nil, nil)
		p.add("aws_lb_target_group_attachment.app", "aws_lb_target_group_attachment", model.Replace, nil, nil)
		adminAction := model.NoOp
		if adminReplaced {
			adminAction = model.Replace
		}
		p.add("aws_lb_target_group_attachment.admin", "aws_lb_target_group_attachment", adminAction, nil, nil)
		p.refer("aws_route53_record.www", "aws_lb.web")
		p.refer("aws_lb_listener.http", "aws_lb.web")
		p.refer("aws_lb_listener.admin", "aws_lb.web")
		p.refer("aws_lb_listener.http", "aws_lb_target_group.app")
		p.refer("aws_lb_listener.admin", "aws_lb_target_group.admin")
		p.refer("aws_lb_target_group_attachment.app", "aws_lb_target_group.app")
		p.refer("aws_lb_target_group_attachment.admin", "aws_lb_target_group.admin")
		ap := &model.Apply{Steps: []*model.ApplyStep{
			{ID: "aws_lb_target_group_attachment.app:destroy", Address: "aws_lb_target_group_attachment.app", Op: "destroy", Start: 0, Duration: 5, Known: true},
			{ID: "aws_lb_target_group_attachment.app:create", Address: "aws_lb_target_group_attachment.app", Op: "create", Start: 300, Duration: 5},
		}}
		if adminReplaced {
			ap.Steps = append(ap.Steps,
				&model.ApplyStep{ID: "aws_lb_target_group_attachment.admin:destroy", Address: "aws_lb_target_group_attachment.admin", Op: "destroy", Start: 0, Duration: 5},
				&model.ApplyStep{ID: "aws_lb_target_group_attachment.admin:create", Address: "aws_lb_target_group_attachment.admin", Op: "create", Start: 100, Duration: 5})
		}
		return p, Timeline{Apply: ap}
	}

	p, tl := build(false)
	if out := p.assess(service("web", host("www.example.com"))).Outages(tl); len(out) != 0 {
		t.Errorf("one group still has its target, so www is served: %+v", out[0])
	}

	p, tl = build(true)
	for range 20 {
		out := p.assess(service("web", host("www.example.com")), service("app", host("www.example.com"))).Outages(tl)
		if len(out) != 1 || !out[0].NoTarget || len(out[0].Uses) != 2 || out[0].Window != (Window{From: 0, To: 105}) || out[0].Culprit != "aws_lb_target_group_attachment.admin" {
			t.Fatalf("outages = %+v", out)
		}
		if say := out[0].Sentence("Terraform").Text; !strings.Contains(say, "which web and app call outside Terraform, has no target for about 2 minutes while `aws_lb_target_group_attachment.admin` is replaced") {
			t.Fatalf("sentence = %q", say)
		}
	}
}

// replaced is a plan that replaces a database a service uses, and the
// apply that replaces it: destroying it first, or with create_before_destroy
// creating its replacement first.
func replaced(t *testing.T, cbd bool) (*fakePlan, *Assessment, Timeline) {
	t.Helper()
	p := newPlan()
	p.add("aws_db_instance.orders", "aws_db_instance", model.Replace,
		map[string]any{"endpoint": "orders.rds.amazonaws.com"}, map[string]any{"endpoint": "orders.rds.amazonaws.com"})
	endpoint, err := config.ParseAttributePath("endpoint")
	if err != nil {
		t.Fatal(err)
	}
	a := p.assess(service("web", config.Use{Target: config.ResourceTarget{Address: "aws_db_instance.orders", Attribute: endpoint}, As: "DATABASE_URL"}))
	destroy := &model.ApplyStep{ID: "aws_db_instance.orders:destroy", Address: "aws_db_instance.orders", Op: "destroy", Start: 0, Duration: 300, Known: true}
	create := &model.ApplyStep{ID: "aws_db_instance.orders:create", Address: "aws_db_instance.orders", Op: "create", Start: 300, Duration: 600, Known: true}
	if cbd {
		create.Start, destroy.Start, destroy.Deposed = 0, 600, true
	}
	return p, a, Timeline{Apply: &model.Apply{Steps: []*model.ApplyStep{destroy, create}}}
}

func TestReplaced(t *testing.T) {
	_, a, tl := replaced(t, false)
	if len(a.Impacts()) != 0 {
		t.Errorf("the endpoint stays the same, so nothing the service holds changes: %+v", a.Impacts())
	}
	out := a.Outages(tl)
	if len(out) != 1 || out[0].Window != (Window{From: 0, To: 900}) || !out[0].Timed {
		t.Fatalf("outages = %+v", out)
	}
	if got, want := out[0].Sentence("Terraform").Text, "`aws_db_instance.orders`, which web uses outside Terraform, will not exist for about 15 minutes while it is replaced."; got != want {
		t.Errorf("sentence = %q, want %q", got, want)
	}
	if got, want := out[0].ServiceSide(out[0].Uses[0]), "Uses `aws_db_instance.orders` as `DATABASE_URL`, which will not exist for about 15 minutes while it is replaced."; got != want {
		t.Errorf("service side = %q, want %q", got, want)
	}

	// The apply's summary already says the database will not exist, so the
	// outage adds the service to that sentence rather than saying it again.
	ns := []model.Sentence{
		{Text: "`aws_db_instance.orders` will not exist for about 15 minutes while it is replaced.", Refs: []string{"aws_db_instance.orders"}},
	}
	s := out[0].Mentioned(ns)
	if s != &ns[0] {
		t.Fatalf("mentioned = %v", s)
	}
	out[0].Extend(s, "Terraform")
	if want := "`aws_db_instance.orders` will not exist for about 15 minutes while it is replaced, and web uses it outside Terraform."; ns[0].Text != want {
		t.Errorf("extended = %q, want %q", ns[0].Text, want)
	}
	if ns[0].Severity != model.Medium {
		t.Errorf("extended severity = %v, want the service's", ns[0].Severity)
	}

	_, a, tl = replaced(t, true)
	if out := a.Outages(tl); len(out) != 0 {
		t.Errorf("the replacement exists before the old one goes: %+v", out[0])
	}
}

// Where the apply is expected to stop decides whether the outage happens,
// and whether it ends.
func TestExpectedFailure(t *testing.T) {
	_, a, tl := replaced(t, false)
	tl.StopsAt = []string{"aws_iam_role.app"}
	tl.Halted = map[string]bool{"aws_db_instance.orders:create": true}
	out := a.Outages(tl)
	if len(out) != 1 || !slices.Equal(out[0].Stuck, tl.StopsAt) {
		t.Fatalf("outages = %+v", out)
	}
	if got, want := out[0].Sentence("Terraform").Text, "`aws_db_instance.orders`, which web uses outside Terraform, will not exist for about 15 minutes while it is replaced, and stays missing if the apply stops at `aws_iam_role.app`, as interragate expects."; got != want {
		t.Errorf("sentence = %q, want %q", got, want)
	}
	if f := out[0].Finding(".interragate.yaml", "Terraform"); !strings.HasPrefix(f.Remediation, "Fix the failure expected at `aws_iam_role.app` before applying") {
		t.Errorf("remediation = %q", f.Remediation)
	}

	tl.Halted = map[string]bool{"aws_db_instance.orders:destroy": true, "aws_db_instance.orders:create": true}
	out = a.Outages(tl)
	if len(out) != 1 || !slices.Equal(out[0].OnceFixed, tl.StopsAt) || len(out[0].Stuck) != 0 {
		t.Fatalf("outages = %+v", out)
	}
	if got := out[0].Sentence("Terraform").Text; !strings.HasSuffix(got, "while it is replaced, once the failure expected at `aws_iam_role.app` is fixed.") {
		t.Errorf("sentence = %q", got)
	}
}

// An output a service reads goes with the resources its value comes from,
// not those of a module it names as a whole.
func TestOutputOutage(t *testing.T) {
	for _, whole := range []bool{false, true} {
		p := newPlan()
		p.add("aws_lb.web", "aws_lb", model.Replace, map[string]any{"dns_name": "web.elb.amazonaws.com"}, map[string]any{"dns_name": "web.elb.amazonaws.com"})
		p.report.Outputs = []*model.Output{{Name: "api_url", Action: model.NoOp}}
		p.graph.Outputs["api_url"] = []graph.Ref{{Resource: "aws_lb.web", Attribute: "dns_name", Whole: whole}}
		a := p.assess(service("app", config.Use{Target: config.OutputTarget{Name: "api_url"}}))
		tl := Timeline{Apply: &model.Apply{Steps: []*model.ApplyStep{
			{ID: "aws_lb.web:destroy", Address: "aws_lb.web", Op: "destroy", Start: 0, Duration: 30},
			{ID: "aws_lb.web:create", Address: "aws_lb.web", Op: "create", Start: 30, Duration: 30},
		}}}
		out := a.Outages(tl)
		if whole {
			if len(out) != 0 {
				t.Errorf("no value comes from a module named as a whole: %+v", out[0])
			}
			continue
		}
		if len(out) != 1 {
			t.Fatalf("outages = %+v", out)
		}
		if got, want := out[0].Sentence("Terraform").Text, "Output `api_url`, which app reads outside Terraform, comes from `aws_lb.web`, which will not exist while it is replaced."; got != want {
			t.Errorf("sentence = %q, want %q", got, want)
		}
	}
}

// What the plan destroys for good is a loss, not an outage as well.
func TestLossIsNotAnOutage(t *testing.T) {
	p := newPlan()
	p.add("aws_db_instance.orders", "aws_db_instance", model.Delete, map[string]any{"endpoint": "orders"}, nil)
	a := p.assess(service("web", config.Use{Target: config.ResourceTarget{Address: "aws_db_instance.orders"}}))
	tl := Timeline{Apply: &model.Apply{Steps: []*model.ApplyStep{
		{ID: "aws_db_instance.orders:destroy", Address: "aws_db_instance.orders", Op: "destroy", Start: 0, Duration: 300},
	}}}
	if len(a.Impacts()) != 1 || a.Impacts()[0].Kind() != model.EffectLoses {
		t.Fatalf("impacts = %+v", a.Impacts())
	}
	if out := a.Outages(tl); len(out) != 0 {
		t.Errorf("outages = %+v", out[0])
	}
}

// When the apply is expected to stop at the very resource that goes, the
// outage says how.
func TestFailingItself(t *testing.T) {
	_, a, tl := replaced(t, false)
	tl.StopsAt = []string{"aws_db_instance.orders"}
	tl.Halted = map[string]bool{"aws_db_instance.orders:destroy": true, "aws_db_instance.orders:create": true}
	out := a.Outages(tl)
	if len(out) != 1 {
		t.Fatalf("outages = %+v", out)
	}
	if got, want := out[0].ServiceSide(out[0].Uses[0]), "Uses `aws_db_instance.orders` as `DATABASE_URL`, which will not exist for about 15 minutes while it is replaced, once the failure expected in destroying it is fixed."; got != want {
		t.Errorf("service side = %q, want %q", got, want)
	}
	if refs := out[0].refs(); !slices.Equal(refs, []string{"aws_db_instance.orders"}) {
		t.Errorf("refs = %q", refs)
	}

	tl.Halted = map[string]bool{"aws_db_instance.orders:create": true}
	if got := a.Outages(tl)[0].Sentence("Terraform").Text; !strings.HasSuffix(got, "while it is replaced, and stays missing if its replacement fails, as interragate expects.") {
		t.Errorf("sentence = %q", got)
	}
}
