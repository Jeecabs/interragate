package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
)

func loadYAML(t *testing.T, yaml string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".interragate.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestConsumersLoad(t *testing.T) {
	cfg, err := loadYAML(t, `
consumers:
  - name: storefront-web
    platform: vercel
    owners: ["@acme/web"]
    link: https://vercel.com/acme/storefront-web
    uses:
      - host: https://www.storefront.example.com/api
        as: NEXT_PUBLIC_API_URL
      - resource: aws_db_instance.orders
        attribute: endpoint
        as: DATABASE_URL
      - output: api_url
  - name: Acme Pay callbacks
    platform: partner
    owners: "@acme/payments"
    notify: true
    update: manual
    severity: high
    uses:
      - resource: aws_security_group.alb
        reach: 8443/tcp
        from: 198.51.100.7
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Consumers) != 2 {
		t.Fatalf("consumers = %+v", cfg.Consumers)
	}
	web, pay := cfg.Consumers[0], cfg.Consumers[1]
	if web.Name != "storefront-web" || web.Update != model.UpdateUnknown || web.Severity != model.None || len(web.Uses) != 3 {
		t.Errorf("web = %+v", web)
	}
	if host, ok := web.Uses[0].Target.(HostTarget); !ok || host.Host != "www.storefront.example.com" {
		t.Errorf("host use = %#v", web.Uses[0].Target)
	}
	if db, ok := web.Uses[1].Target.(ResourceTarget); !ok || db.Address != "aws_db_instance.orders" || db.Attribute.String() != "endpoint" || len(db.Attribute.Steps()) != 1 {
		t.Errorf("resource use = %#v", web.Uses[1].Target)
	}
	if out, ok := web.Uses[2].Target.(OutputTarget); !ok || out.String() != "output.api_url" {
		t.Errorf("output use = %#v", web.Uses[2].Target)
	}
	reach, ok := pay.Uses[0].Target.(ReachTarget)
	if !ok || reach.String() != "aws_security_group.alb 8443/tcp" || reach.From.String() != "198.51.100.7/32" {
		t.Errorf("reach use = %#v", pay.Uses[0].Target)
	}
	if !pay.Notify || len(pay.Owners) != 1 || pay.Update != model.UpdateManual || pay.Severity != model.High {
		t.Errorf("pay = %+v", pay)
	}
}

func TestConsumersInvalid(t *testing.T) {
	for _, c := range []struct{ yaml, want string }{
		{"consumers: {name: x}", "consumers must be a list"},
		{"consumers:\n  - storefront", "consumers[0]: a service must be a mapping"},
		{"consumers:\n  - uses: [{resource: a.b}]", "consumers[0]: name is required"},
		{"consumers:\n  - name: web", "consumers[0] (web): uses is required"},
		{"consumers:\n  - name: web\n    uses: [{resource: a.b}]\n  - name: Web\n    uses: [{resource: a.b}]", "consumers[1] (Web): name is already used by consumers[0]"},
		{"consumers:\n  - name: web\n    colour: blue\n    uses: [{resource: a.b}]", `consumers[0] (web): unknown key "colour"`},
		{"consumers:\n  - name: web\n    update: soon\n    uses: [{resource: a.b}]", `unknown update "soon"; use live, redeploy, rebuild, ota, store or manual`},
		{"consumers:\n  - name: web\n    severity: huge\n    uses: [{resource: a.b}]", `unknown severity "huge"; use critical, high, medium, low or info`},
		{"consumers:\n  - name: web\n    severity: none\n    uses: [{resource: a.b}]", `unknown severity "none"`},
		{"consumers:\n  - name: web\n    link: vercel.com/x\n    uses: [{resource: a.b}]", "is not a web address"},
		{"consumers:\n  - name: web\n    notify: yes please\n    uses: [{resource: a.b}]", "notify must be true or false"},
		{"consumers:\n  - name: web\n    uses: [{as: X}]", "uses[0]: name a resource, an output or a host"},
		{"consumers:\n  - name: web\n    uses: [{resource: a.b, output: x}]", "names resource or output; a use names one"},
		{"consumers:\n  - name: web\n    uses: [{output: x, attribute: y}]", "attribute goes with resource"},
		{"consumers:\n  - name: web\n    uses: [{host: a.example.com, reach: 443}]", "reach goes with resource"},
		{"consumers:\n  - name: web\n    uses: [{resource: a.b, from: 10.0.0.0/8}]", "from goes with reach"},
		{"consumers:\n  - name: web\n    uses: [{resource: a.b, attribute: x, reach: 443}]", "an attribute or a reach, not both"},
		{"consumers:\n  - name: web\n    uses: [{resource: a.b, reach: https}]", `reach "https" is not a port`},
		{"consumers:\n  - name: web\n    uses: [{resource: a.b, reach: 443, from: the office}]", `from "the office" is not an address or range`},
		{"consumers:\n  - name: web\n    uses: [{host: localhost}]", `host "localhost" is not a host name`},
		{"consumers:\n  - name: web\n    uses: [{output: output.api_url}]", "use the name alone"},
		{"consumers:\n  - name: web\n    uses: [{resource: a.b, attribute: \"tags[\"}]", "not closed"},
	} {
		_, err := loadYAML(t, c.yaml)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error = %v, want it to say %q", c.yaml, err, c.want)
		}
	}
}

func TestParseHost(t *testing.T) {
	for in, want := range map[string]Host{
		"api.example.com":                 "api.example.com",
		"API.Example.com.":                "api.example.com",
		"https://api.example.com:8443/v1": "api.example.com",
		"api.example.com/v1":              "api.example.com",
	} {
		if got, err := ParseHost(in); err != nil || got != want {
			t.Errorf("ParseHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"localhost", "*.example.com", "", "https://"} {
		if _, err := ParseHost(in); err == nil {
			t.Errorf("ParseHost(%q) did not fail", in)
		}
	}
}

func TestHostAnsweredBy(t *testing.T) {
	h := Host("api.example.com")
	for name, want := range map[string]bool{
		"api.example.com":   true,
		"API.example.com.":  true,
		"*.example.com":     true,
		"*.api.example.com": false,
		"*.com":             false,
		"www.example.com":   false,
	} {
		if got := h.AnsweredBy(name); got != want {
			t.Errorf("%s answered by %q = %v, want %v", h, name, got, want)
		}
	}
}
