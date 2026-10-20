package graph

import (
	"slices"
	"testing"

	"github.com/Jeecabs/interragate/internal/plan"
)

func load(t *testing.T, name string) *plan.Plan {
	t.Helper()
	p, err := plan.ReadFile("../../testdata/plans/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEdgesFollowModuleOutputs(t *testing.T) {
	g := Build(load(t, "aws-webapp.terraform.json").Configuration, nil)
	cases := []struct{ from, to string }{
		{"module.network.aws_vpc.this", "module.network.aws_subnet.private"},
		{"module.network.aws_vpc.this", "aws_security_group.alb"},
		{"module.network.aws_subnet.public", "aws_lb.web"},
		{"module.network.aws_subnet.private", "aws_db_subnet_group.main"},
		{"aws_db_instance.orders", "aws_ssm_parameter.db_endpoint"},
		{"aws_kms_key.data", "aws_db_instance.orders"},
		{"aws_instance.app", "aws_lb_target_group_attachment.web"},
	}
	for _, c := range cases {
		n := g.Nodes[c.to]
		if n == nil {
			t.Fatalf("missing node %s", c.to)
		}
		if !slices.Contains(n.Up, c.from) {
			t.Errorf("%s should depend on %s; up = %v", c.to, c.from, n.Up)
		}
		if !slices.Contains(g.Nodes[c.from].Down, c.to) {
			t.Errorf("%s should list %s as a dependent", c.from, c.to)
		}
	}
}

func TestBareModuleRefIsNotEveryOutput(t *testing.T) {
	g := Build(load(t, "aws-webapp.terraform.json").Configuration, nil)
	if up := g.Nodes["aws_db_subnet_group.main"].Up; slices.Contains(up, "module.network.aws_subnet.public") {
		t.Fatalf("db subnet group only reads private subnets, got %v", up)
	}
}

func TestAttributeRefs(t *testing.T) {
	g := Build(load(t, "aws-webapp.terraform.json").Configuration, nil)
	refs := g.Nodes["aws_ssm_parameter.db_endpoint"].Attrs["value"]
	if !slices.Contains(refs, Ref{Resource: "aws_db_instance.orders", Attribute: "address"}) {
		t.Fatalf("value refs = %v", refs)
	}
}

func TestDownstream(t *testing.T) {
	g := Build(load(t, "aws-webapp.terraform.json").Configuration, nil)
	down := g.Downstream("aws_db_instance.orders")
	for _, want := range []string{"aws_ssm_parameter.db_endpoint", "aws_cloudwatch_metric_alarm.db_cpu"} {
		if !slices.Contains(down, want) {
			t.Errorf("downstream of the database should include %s: %v", want, down)
		}
	}
}

func TestLocalsResolver(t *testing.T) {
	cfg := load(t, "mechanics.terraform.json").Configuration
	without := Build(cfg, nil)
	if slices.Contains(without.Nodes["terraform_data.deploy"].Up, "random_pet.cluster") {
		t.Fatal("without locals the edge through local.labels cannot be known")
	}
	with := Build(cfg, func(module, name string) ([]string, bool) {
		if module == "" && name == "labels" {
			return []string{"var.environment", "random_pet.cluster.id"}, true
		}
		return nil, false
	})
	if !slices.Contains(with.Nodes["terraform_data.deploy"].Up, "random_pet.cluster") {
		t.Fatalf("locals should connect terraform_data.deploy to random_pet.cluster: %v", with.Nodes["terraform_data.deploy"].Up)
	}
}

func TestTraversal(t *testing.T) {
	got := Traversal(`module.a["x.y"].out[0].id`)
	if want := []string{"module", "a", "out", "id"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A provider is configured before anything uses it, so what its
// configuration reads comes first, through module inputs too. None of a
// resource's values come from there, so it is kept apart from Up.
func TestProviderConfigurationReads(t *testing.T) {
	p, err := plan.Parse([]byte(`{"format_version":"1.2","planned_values":{},"configuration":{
	  "provider_config":{
	    "tls":{"name":"tls","full_name":"registry.terraform.io/hashicorp/tls",
	      "expressions":{"proxy":[{"url":{"references":["random_pet.p.id","random_pet.p"]}}]}},
	    "module.k:tls":{"name":"tls","full_name":"registry.terraform.io/hashicorp/tls","module_address":"module.k",
	      "expressions":{"proxy":[{"url":{"references":["var.host"]}}]}},
	    "random":{"name":"random","full_name":"registry.terraform.io/hashicorp/random"}
	  },
	  "root_module":{
	    "resources":[
	      {"address":"random_pet.p","mode":"managed","type":"random_pet","name":"p","provider_config_key":"random"},
	      {"address":"random_pet.q","mode":"managed","type":"random_pet","name":"q","provider_config_key":"random"},
	      {"address":"tls_private_key.k","mode":"managed","type":"tls_private_key","name":"k","provider_config_key":"tls"}
	    ],
	    "module_calls":{"k":{"source":"./k","expressions":{"host":{"references":["random_pet.q.id","random_pet.q"]}},
	      "module":{"resources":[{"address":"tls_private_key.x","mode":"managed","type":"tls_private_key","name":"x","provider_config_key":"module.k:tls"}]}}}
	  }}}`))
	if err != nil {
		t.Fatal(err)
	}
	g := Build(p.Configuration, nil)
	if n := g.Nodes["tls_private_key.k"]; !slices.Equal(n.Provider, []string{"random_pet.p"}) || len(n.Up) != 0 {
		t.Errorf("tls_private_key.k: provider %v, up %v", n.Provider, n.Up)
	}
	if n := g.Nodes["module.k.tls_private_key.x"]; !slices.Equal(n.Provider, []string{"random_pet.q"}) {
		t.Errorf("a provider in a module reads through its inputs: %v", n.Provider)
	}
	if got := g.Providers["tls"]; !slices.Equal(got, []string{"random_pet.p"}) || g.Providers["random"] != nil {
		t.Errorf("providers = %v", g.Providers)
	}
}

// Terraform waits for everything in a module referred to as a whole, though
// the value is only its outputs.
func TestWholeModuleRef(t *testing.T) {
	p, err := plan.Parse([]byte(`{"format_version":"1.2","planned_values":{},"configuration":{"root_module":{
	  "resources":[
	    {"address":"terraform_data.reader","mode":"managed","type":"terraform_data","name":"reader",
	      "expressions":{"input":{"references":["module.m"]}}},
	    {"address":"terraform_data.out","mode":"managed","type":"terraform_data","name":"out",
	      "expressions":{"input":{"references":["module.m.out","module.m"]}}}
	  ],
	  "module_calls":{"m":{"source":"./m","module":{
	    "resources":[
	      {"address":"terraform_data.hidden","mode":"managed","type":"terraform_data","name":"hidden"},
	      {"address":"terraform_data.shown","mode":"managed","type":"terraform_data","name":"shown"}
	    ],
	    "outputs":{"out":{"expression":{"references":["terraform_data.shown.output","terraform_data.shown"]}}}}}}
	  }}}`))
	if err != nil {
		t.Fatal(err)
	}
	g := Build(p.Configuration, nil)
	reader := g.Nodes["terraform_data.reader"]
	if !slices.Equal(reader.Up, []string{"module.m.terraform_data.shown"}) ||
		!slices.Equal(reader.Modules, []string{"module.m.terraform_data.hidden", "module.m.terraform_data.shown"}) {
		t.Errorf("reader: up %v, modules %v", reader.Up, reader.Modules)
	}
	if out := g.Nodes["terraform_data.out"]; !slices.Equal(out.Up, []string{"module.m.terraform_data.shown"}) || out.Modules != nil {
		t.Errorf("an output is not the whole module: up %v, modules %v", out.Up, out.Modules)
	}
	for _, r := range g.Nodes["terraform_data.reader"].Attrs["input"] {
		if r.Resource == "module.m.terraform_data.hidden" {
			t.Errorf("no value comes from a resource behind no output: %v", r)
		}
	}
}

// A bare module reference in another module's count waits for the whole
// module, but its resources behind no output give no value.
func TestWholeModuleRefInModuleCount(t *testing.T) {
	p, err := plan.Parse([]byte(`{"format_version":"1.2","planned_values":{},"configuration":{"root_module":{
	  "module_calls":{
	    "m":{"source":"./m","module":{
	      "resources":[
	        {"address":"terraform_data.hidden","mode":"managed","type":"terraform_data","name":"hidden"},
	        {"address":"terraform_data.shown","mode":"managed","type":"terraform_data","name":"shown"}
	      ],
	      "outputs":{"out":{"expression":{"references":["terraform_data.shown.output","terraform_data.shown"]}}}}},
	    "n":{"source":"./n","count_expression":{"references":["module.m"]},"module":{
	      "resources":[{"address":"terraform_data.inside","mode":"managed","type":"terraform_data","name":"inside"}]}}
	  }}}}`))
	if err != nil {
		t.Fatal(err)
	}
	inside := Build(p.Configuration, nil).Nodes["module.n.terraform_data.inside"]
	if slices.Contains(inside.Up, "module.m.terraform_data.hidden") || !slices.Contains(inside.Modules, "module.m.terraform_data.hidden") {
		t.Errorf("inside: up %v, modules %v", inside.Up, inside.Modules)
	}
}

func TestDependsOnModuleWithoutOutputs(t *testing.T) {
	p, err := plan.Parse([]byte(`{"format_version":"1.2","planned_values":{},"configuration":{"root_module":{
	  "resources":[
	    {"address":"aws_instance.app","mode":"managed","type":"aws_instance","name":"app","depends_on":["module.net"]},
	    {"address":"aws_instance.counted","mode":"managed","type":"aws_instance","name":"counted"}
	  ],
	  "module_calls":{
	    "net":{"source":"./net","module":{"resources":[{"address":"aws_vpc.this","mode":"managed","type":"aws_vpc","name":"this"}]}},
	    "workers":{"source":"./w","count_expression":{"references":["aws_instance.counted.id","aws_instance.counted"]},
	      "module":{"resources":[{"address":"aws_instance.w","mode":"managed","type":"aws_instance","name":"w"}]}}
	  }}}}`))
	if err != nil {
		t.Fatal(err)
	}
	g := Build(p.Configuration, nil)
	if up := g.Nodes["aws_instance.app"].Up; !slices.Contains(up, "module.net.aws_vpc.this") {
		t.Errorf("depends_on a module should mean all its resources: %v", up)
	}
	if up := g.Nodes["module.workers.aws_instance.w"].Up; !slices.Contains(up, "aws_instance.counted") {
		t.Errorf("a module's count references are dependencies of its resources: %v", up)
	}
}
