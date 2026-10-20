package html

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/model"
)

func TestRenderRoots(t *testing.T) {
	var b bytes.Buffer
	err := RenderRoots(&b, "infra", "infra · terraform 1.16.6", []Root{
		{Name: "staging", Href: "staging.html", Report: &model.Report{Risk: model.Risk{Level: model.None, Headline: "No changes."}}},
		{Name: "prod", Href: "prod.html", Report: &model.Report{Risk: model.Risk{Level: model.Critical, Headline: "Destroys an <S3> bucket."}, Summary: model.Summary{Delete: 1, Update: 2}}},
		{Name: "dev", Href: "dev.html", Error: "Error: Invalid reference"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"3 roots: 1 failed to plan, 1 critical risk and 1 with no changes.", "Destroys an &lt;S3&gt; bucket.", `href="prod.html"`, "−1", "~2"} {
		if !strings.Contains(out, want) {
			t.Errorf("index lacks %q", want)
		}
	}
	if strings.Index(out, "dev.html") > strings.Index(out, "prod.html") || strings.Index(out, "prod.html") > strings.Index(out, "staging.html") {
		t.Error("want the failed plan first, then by risk")
	}
}
