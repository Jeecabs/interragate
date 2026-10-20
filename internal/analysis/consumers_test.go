package analysis

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

// awsConsumers analyses the aws sample with the services outside Terraform
// that its own .interragate.yaml declares, followed by more yaml.
func awsConsumers(t *testing.T, more string) *model.Report {
	t.Helper()
	declared, err := os.ReadFile("../../testdata/scenarios/aws-webapp/v2/.interragate.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return awsWith(t, string(declared)+more)
}

// awsWith analyses the aws sample with a configuration file.
func awsWith(t *testing.T, yaml string) *model.Report {
	t.Helper()
	p, err := plan.ReadFile("../../testdata/plans/aws-webapp.terraform.json")
	if err != nil {
		t.Fatal(err)
	}
	return Analyze(p, Options{Config: policyConfig(t, yaml), Generator: "interragate test"})
}

func consumer(t *testing.T, r *model.Report, name model.ServiceName) *model.Consumer {
	t.Helper()
	for _, c := range r.Consumers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no service %s in %+v", name, r.Consumers)
	return nil
}

func kinds(c *model.Consumer) []model.EffectKind {
	out := make([]model.EffectKind, 0, len(c.Effects))
	for _, e := range c.Effects {
		out = append(out, e.Kind)
	}
	slices.Sort(out)
	return out
}

func effect(c *model.Consumer, kind model.EffectKind) string {
	for _, e := range c.Effects {
		if e.Kind == kind {
			return e.Text
		}
	}
	return ""
}

func TestConsumersOnTheSample(t *testing.T) {
	r := awsConsumers(t, "")

	for _, c := range []struct {
		rule, address string
		service       model.ServiceName
		says          string
	}{
		{"consumer-loses", "aws_s3_bucket.logs", "storefront-app", "Destroys `aws_s3_bucket.logs`, which storefront-app uses as `EXPO_PUBLIC_CRASH_BUCKET`"},
		{"consumer-value", "aws_db_instance.orders", "storefront-web", "leaves its `endpoint` known only after apply, so it may change; storefront-web holds it as `DATABASE_URL`"},
		{"consumer-reach", "aws_security_group.alb", "Acme Pay callbacks", "Closes `8443/tcp` from `0.0.0.0/0` on `aws_security_group.alb`, the way in for Acme Pay callbacks"},
		{"consumer-outage", "aws_lb_target_group_attachment.web", "storefront-web", "has no target for about 6 minutes while `aws_lb_target_group_attachment.web` is replaced"},
	} {
		fs := findingsFor(r, c.rule)
		if len(fs) == 0 {
			t.Errorf("%s: no findings", c.rule)
			continue
		}
		f := fs[0]
		if f.Addresses[0] != c.address || !slices.Contains(f.Consumers, c.service) || f.Severity != model.Medium || f.Category != "consumers" {
			t.Errorf("%s = %+v", c.rule, f)
		}
		if !strings.Contains(f.Message, c.says) || !strings.Contains(f.Message, "(declared in `.interragate.yaml`)") {
			t.Errorf("%s message = %q, want it to say %q", c.rule, f.Message, c.says)
		}
	}
	if reach := findingsFor(r, "consumer-reach")[0]; !strings.Contains(reach.Message, "added outside Terraform") {
		t.Errorf("the 8443 rule was added by hand, so the finding should say so: %q", reach.Message)
	}

	web, app, pay := consumer(t, r, "storefront-web"), consumer(t, r, "storefront-app"), consumer(t, r, "Acme Pay callbacks")
	if got := kinds(web); !slices.Equal(got, []model.EffectKind{model.EffectValue, model.EffectOutage, model.EffectOutage}) {
		t.Errorf("storefront-web effects = %v", got)
	}
	if got := kinds(app); !slices.Equal(got, []model.EffectKind{model.EffectLoses, model.EffectOutage}) {
		t.Errorf("storefront-app effects = %v (destroying its bucket is not an outage too)", got)
	}
	if got := kinds(pay); !slices.Equal(got, []model.EffectKind{model.EffectReach}) {
		t.Errorf("Acme Pay callbacks effects = %v", got)
	}
	for _, c := range []*model.Consumer{web, app, pay} {
		if c.Standing() != model.StandingAffected || !c.Checked || c.Hidden != 0 || len(c.Missing) != 0 {
			t.Errorf("%s = %+v", c.Name, c)
		}
	}
	if web.Platform != "Vercel" || app.Platform != "Expo" || pay.Platform != "partner" {
		t.Errorf("platforms = %q, %q, %q", web.Platform, app.Platform, pay.Platform)
	}
	if !strings.Contains(effect(app, model.EffectLoses), "An over-the-air update can point copies of storefront-app") ||
		!strings.Contains(effect(app, model.EffectLoses), "anyone can create one with its name") {
		t.Errorf("an app keeps using a bucket it was built with: %q", effect(app, model.EffectLoses))
	}

	// The load balancer behind www has one target, which the apply replaces
	// and which stays destroyed if the apply stops as expected. The
	// database's own gap only happens once that failure is fixed.
	for _, want := range []string{"Calls `www.storefront.example.com` (`EXPO_PUBLIC_API_URL`)", "none at all if the apply stops at `aws_db_instance.orders`"} {
		if !strings.Contains(effect(app, model.EffectOutage), want) {
			t.Errorf("outage = %q, want it to say %q", effect(app, model.EffectOutage), want)
		}
	}
	var dbOutage string
	for _, e := range web.Effects {
		if e.Kind == model.EffectOutage && strings.HasPrefix(e.Text, "Uses `aws_db_instance.orders`") {
			dbOutage = e.Text
		}
	}
	if !strings.Contains(dbOutage, "while it is replaced, once the failure expected in destroying it is fixed") {
		t.Errorf("database outage = %q", dbOutage)
	}

	applySays := func(want string) bool {
		return slices.ContainsFunc(r.Apply.Narrative, func(s model.Sentence) bool { return strings.Contains(s.Text, want) })
	}
	if !applySays("`www.storefront.example.com`, which storefront-web and storefront-app call outside Terraform, has no target") {
		t.Errorf("the apply's summary should name the outage once, for both services: %+v", r.Apply.Narrative)
	}
	if !applySays("depends on it, and storefront-web uses it outside Terraform.") {
		t.Errorf("the database's gap sentence should name storefront-web: %+v", r.Apply.Narrative)
	}

	n := 0
	for _, s := range r.Narrative {
		if strings.Contains(s.Text, "storefront-") {
			n++
			if !strings.HasPrefix(s.Text, "Affects 3 services outside Terraform that `.interragate.yaml` declares") || s.Severity != model.Medium {
				t.Errorf("summary sentence = %+v", s)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d summary sentences about the services, want 1", n)
	}
}

// What disable and ignore leave out is counted, not shown, outages included.
func TestConsumersHidden(t *testing.T) {
	r := awsConsumers(t, `
disable: [consumer-reach]
ignore:
  - rule: consumer-outage
    address: aws_lb_target_group_attachment.web
    reason: we drain www before every apply
`)
	if len(findingsFor(r, "consumer-reach")) != 0 {
		t.Error("consumer-reach is disabled")
	}
	pay := consumer(t, r, "Acme Pay callbacks")
	if pay.Standing() != model.StandingHidden || pay.Hidden != 1 {
		t.Errorf("Acme Pay callbacks = %+v, want its closed port counted as hidden", pay)
	}
	app := consumer(t, r, "storefront-app")
	if got := kinds(app); !slices.Equal(got, []model.EffectKind{model.EffectLoses}) || app.Hidden != 1 {
		t.Errorf("storefront-app = %+v, want the ignored outage hidden", app)
	}
	for _, s := range r.Apply.Narrative {
		if strings.Contains(s.Text, "www.storefront.example.com") {
			t.Errorf("the ignored outage is still in the apply's summary: %q", s.Text)
		}
	}
}

func TestConsumersDeclarations(t *testing.T) {
	r := awsWith(t, `
consumers:
  - name: reporting
    platform: render
    severity: high
    uses:
      - resource: aws_db_instance.ordres
      - resource: aws_db_instance.orders
        attribute: vaule
      - output: logs_bucket
      - host: api.storefront.example.com
  - name: subnets
    uses:
      - resource: module.network.aws_subnet.public
  - name: twice
    uses:
      - host: https://www.storefront.example.com/api
      - host: www.storefront.example.com
`)
	rep := consumer(t, r, "reporting")
	want := []string{"aws_db_instance.ordres", "aws_db_instance.orders.vaule (it has no attribute vaule)", "api.storefront.example.com"}
	if !slices.Equal(rep.Missing, want) {
		t.Errorf("missing = %q, want %q", rep.Missing, want)
	}
	// The sample removes its logs_bucket output; the service's severity
	// carries to the finding.
	var removed *model.Finding
	for _, f := range findingsFor(r, "consumer-loses") {
		if f.Addresses[0] == "output.logs_bucket" {
			removed = f
		}
	}
	if removed == nil || removed.Severity != model.High || !strings.HasPrefix(removed.Message, "Removes output `logs_bucket`, which reporting reads") {
		t.Errorf("removed output finding = %+v", removed)
	}
	// A configuration address covers every instance.
	if sub := consumer(t, r, "subnets"); !sub.Checked || len(sub.Missing) != 0 || !slices.Contains(kinds(sub), model.EffectLoses) {
		t.Errorf("subnets = %+v", sub)
	}
	// A host declared twice, as a URL and on its own, counts once.
	if twice := consumer(t, r, "twice"); len(twice.Effects) != 1 {
		t.Errorf("twice = %+v", twice.Effects)
	}
}

// One finding about several services says what each needs.
func TestConsumersSharingAValue(t *testing.T) {
	r := awsWith(t, `
consumers:
  - name: storefront-web
    platform: vercel
    uses:
      - resource: aws_db_instance.orders
        attribute: endpoint
        as: DATABASE_URL
  - name: Billing and Payments
    platform: ios
    uses:
      - resource: aws_db_instance.orders
        attribute: endpoint
        as: ORDERS_HOST
`)
	fs := findingsFor(r, "consumer-value")
	if len(fs) != 1 {
		t.Fatalf("findings = %+v", fs)
	}
	f := fs[0]
	for _, want := range []string{
		"storefront-web (as `DATABASE_URL`) and Billing and Payments (as `ORDERS_HOST`) hold it",
		"copies of Billing and Payments already installed keep the old value until people update",
		"storefront-web takes the new value on its next deployment",
	} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message = %q, want it to say %q", f.Message, want)
		}
	}
	if !strings.Contains(f.Remediation, "no version of Billing and Payments") || !strings.Contains(f.Remediation, "set `DATABASE_URL` in storefront-web") {
		t.Errorf("remediation = %q", f.Remediation)
	}
}

func TestNoConsumers(t *testing.T) {
	p, err := plan.ReadFile("../../testdata/plans/aws-webapp.terraform.json")
	if err != nil {
		t.Fatal(err)
	}
	r := Analyze(p, Options{Generator: "interragate test"})
	if r.Consumers != nil {
		t.Errorf("consumers = %+v", r.Consumers)
	}
	for _, f := range r.Findings {
		if f.Category == "consumers" {
			t.Errorf("finding %s without any declared service", f.ID)
		}
	}
}

// check skips the apply's simulation, unless services outside Terraform
// need it for their outages.
func TestConsumersNeedTheApply(t *testing.T) {
	p, err := plan.ReadFile("../../testdata/plans/aws-webapp.terraform.json")
	if err != nil {
		t.Fatal(err)
	}
	declared, err := os.ReadFile("../../testdata/scenarios/aws-webapp/v2/.interragate.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r := Analyze(p, Options{Config: policyConfig(t, string(declared)), SkipApply: true, Generator: "interragate test"})
	if len(findingsFor(r, "consumer-outage")) == 0 {
		t.Error("outages need the apply simulated, even for check")
	}
	if r := Analyze(p, Options{SkipApply: true, Generator: "interragate test"}); r.Apply != nil {
		t.Error("without services outside Terraform, SkipApply skips the simulation")
	}
}
