package source

import (
	"slices"
	"testing"

	"github.com/Jeecabs/interragate/internal/plan"
)

func TestLoadMechanics(t *testing.T) {
	p, err := plan.ReadFile("../../testdata/plans/mechanics.terraform.json")
	if err != nil {
		t.Fatal(err)
	}
	idx, err := Load("../../testdata/scenarios/mechanics/v2", p.Configuration)
	if err != nil {
		t.Fatal(err)
	}
	refs, ok := idx.Locals("", "labels")
	if !ok || !slices.Contains(refs, "random_pet.cluster.id") || !slices.Contains(refs, "var.environment") {
		t.Fatalf("locals.labels refs = %v", refs)
	}
	az := idx.Block("", "random_shuffle.az")
	if az == nil || az.File != "main.tf" || az.Line == 0 || az.EndLine <= az.Line {
		t.Fatalf("random_shuffle.az block = %+v", az)
	}
	if az.Lifecycle.CreateBeforeDestroy == nil || !*az.Lifecycle.CreateBeforeDestroy {
		t.Fatal("create_before_destroy not read")
	}
	if mig := idx.Block("", "terraform_data.migrations"); mig == nil || !slices.Equal(mig.Lifecycle.ReplaceTriggeredBy, []string{"terraform_data.deploy"}) {
		t.Fatalf("replace_triggered_by = %+v", mig)
	}
	if node := idx.Block("module.cache", "random_id.node"); node == nil || node.File != "modules/cache/main.tf" {
		t.Fatalf("module block = %+v", node)
	}
	if call := idx.Block("", "module.cache"); call == nil || call.File != "main.tf" || call.Line != 182 || call.EndLine != 185 {
		t.Fatalf("module call = %+v", call)
	}
	if data := idx.Block("", "data.local_file.policy_read"); data == nil {
		t.Fatal("data block not indexed")
	}
	moved := idx.Modules[""].Moved
	if len(moved) != 1 || moved[0].From != "random_string.session_secret" || moved[0].To != "random_string.session_key" {
		t.Fatalf("moved = %+v", moved)
	}
}

// Outline reads a file held in memory, such as a file as it was in an
// earlier commit; a syntax error loses only what it hides.
func TestOutline(t *testing.T) {
	blocks := Outline("main.tf", []byte(`resource "random_pet" "a" {
  length = 2
}

data "local_file" "b" {
  filename = "b.txt"
}

module "c" {
  source = "./c"
}

resource "random_pet" "broken" {
  length =
`))
	for key, want := range map[string][2]int{"random_pet.a": {1, 3}, "data.local_file.b": {5, 7}, "module.c": {9, 11}} {
		b := blocks[key]
		if b == nil || b.File != "main.tf" || b.Line != want[0] || b.EndLine != want[1] {
			t.Errorf("%s = %+v, want lines %d to %d", key, b, want[0], want[1])
		}
	}
	if len(Outline("bad.tf", []byte("}}} {{{"))) != 0 {
		t.Error("nothing to read in nonsense")
	}
}

// Parse reads each argument of a block with the lines it spans and whether
// it writes a value out, and whether a variable is declared sensitive.
func TestParseArguments(t *testing.T) {
	m := Parse("main.tf", []byte(`variable "pw" {
  type      = string
  sensitive = true
}

resource "aws_db_instance" "db" {
  password   = var.pw
  name       = var.name # was "orders"
  identifier = "${var.prefix}-db"
  user_data  = <<EOT
#!/bin/sh
echo hi
EOT
  tags = {
    Team = "a"
  }
  dynamic "setting" {
    for_each = var.settings
    content {}
  }
}
`))
	if v := m.Inputs["var.pw"]; v == nil || !v.Sensitive {
		t.Fatalf("var.pw = %+v", v)
	}
	db := m.Blocks["aws_db_instance.db"]
	if db == nil {
		t.Fatal("no block")
	}
	want := []Arg{
		{Name: "password", Line: 7, EndLine: 7},
		{Name: "name", Line: 8, EndLine: 8, Literal: true}, // the comment could say anything
		{Name: "identifier", Line: 9, EndLine: 9, Literal: true},
		{Name: "user_data", Line: 10, EndLine: 13, Literal: true},
		{Name: "tags", Line: 14, EndLine: 16, Literal: true},
		{Name: "setting", Line: 17, EndLine: 20, Literal: true},
	}
	if !slices.Equal(db.Args, want) {
		t.Errorf("args:\n got %+v\nwant %+v", db.Args, want)
	}
}

// A .tf.json block ends where its object closes.
func TestParseJSONBlockEnds(t *testing.T) {
	m := Parse("main.tf.json", []byte(`{
  "resource": {
    "random_pet": {
      "x": {
        "length": 3,
        "prefix": "a"
      }
    }
  }
}
`))
	b := m.Blocks["random_pet.x"]
	if b == nil || b.EndLine != 7 || b.EndLine < b.Line {
		t.Fatalf("block = %+v", b)
	}
	want := []Arg{{Name: "length", Line: 5, EndLine: 5, Literal: true}, {Name: "prefix", Line: 6, EndLine: 6, Literal: true}}
	if !slices.Equal(b.Args, want) {
		t.Errorf("args:\n got %+v\nwant %+v", b.Args, want)
	}
}
