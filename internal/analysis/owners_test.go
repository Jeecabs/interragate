package analysis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/gitdiff"
	"github.com/Jeecabs/interragate/internal/model"
)

// Code owners come from the repository's CODEOWNERS, for blocks in the
// Terraform directory below its top; those who own none of the edited
// files are named.
func TestOwners(t *testing.T) {
	top := t.TempDir()
	if err := os.WriteFile(filepath.Join(top, "CODEOWNERS"), []byte("*  @platform\n/infra/modules/network/  @network\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &model.Report{Git: &model.GitInfo{Files: []string{"main.tf"}}, Changes: []*model.Change{
		{Address: "aws_db_instance.orders", Action: model.Update, Location: &model.Location{File: "main.tf"}},
		{Address: "module.network.aws_route_table.public", Action: model.Update, Location: &model.Location{File: "modules/network/main.tf"}},
		{Address: "module.network.aws_subnet.private[1]", Action: model.Replace, Severity: model.High, Location: &model.Location{File: "modules/network/main.tf"}},
		{Address: "module.network.aws_vpc.this", Action: model.Update, Cosmetic: "tags", Location: &model.Location{File: "modules/network/main.tf"}},
	}}
	a := &analyzer{opts: Options{Edits: &gitdiff.Diff{Top: top, Prefix: "infra"}}}
	a.owners(r)
	if got := strings.Join(r.Changes[2].Owners, " "); got != "@network" || r.Git.Codeowners != "CODEOWNERS" {
		t.Fatalf("owners %q, file %q", got, r.Git.Codeowners)
	}
	s := ownerSentence(r)
	want := "This plan changes `module.network.aws_subnet.private[1]` and 1 more, which CODEOWNERS assigns to @network, who owns none of the files you edited, so will not be asked to review it."
	if s == nil || s.Text != want {
		t.Errorf("sentence:\n%v\nwant:\n%s", s, want)
	}
	// With the module edited too, its owners are asked anyway.
	r.Git.Files = append(r.Git.Files, "modules/network/main.tf")
	if s := ownerSentence(r); s != nil {
		t.Errorf("owners of an edited file named: %s", s.Text)
	}
}
