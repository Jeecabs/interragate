package text

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
)

func TestRenderRoots(t *testing.T) {
	var b bytes.Buffer
	err := RenderRoots(&b, []Root{
		{Name: "envs/staging", Report: &model.Report{Risk: model.Risk{Level: model.None, Headline: "No changes."}}},
		{Name: "envs/prod", Report: &model.Report{Risk: model.Risk{Level: model.Critical, Headline: "Destroys an S3 bucket."}, Summary: model.Summary{Delete: 1, Update: 2}}},
		{Name: "envs/dev", Error: "Error: Invalid reference\n\non main.tf line 3"},
	}, Options{Width: 80})
	if err != nil {
		t.Fatal(err)
	}
	want := "  ● envs/dev      plan failed\n    Error: Invalid reference\n" +
		"  ● envs/prod     Critical risk  -1 ~2\n    Destroys an S3 bucket.\n" +
		"  · envs/staging  No changes\n"
	if got := b.String(); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(b.String(), "\x1b[") {
		t.Error("colour without Color")
	}
}
