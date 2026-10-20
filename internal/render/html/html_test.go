package html

import (
	"bytes"
	"encoding/json"
	"html"
	"io"
	"regexp"
	"testing"

	"github.com/Jeecabs/interragate"
	"github.com/Jeecabs/interragate/internal/model"
)

func TestStandalonePagesCarryLicenceNotices(t *testing.T) {
	for name, render := range map[string]func(io.Writer) error{
		"report": func(w io.Writer) error { return Render(w, &model.Report{}) },
		"roots":  func(w io.Writer) error { return RenderRoots(w, "Roots", "", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			if err := render(&b); err != nil {
				t.Fatal(err)
			}
			m := regexp.MustCompile(`(?s)<template id="interragate-licences"><pre>(.*?)</pre></template>`).FindSubmatch(b.Bytes())
			if m == nil {
				t.Fatal("standalone page has no licence notices")
			}
			if got := html.UnescapeString(string(m[1])); got != interragate.LicenceNotices() {
				t.Fatal("standalone page changed or truncated its licence notices")
			}
		})
	}
}

// An archived live page is the same report, marked as an earlier one.
func TestArchived(t *testing.T) {
	var b bytes.Buffer
	r := &model.Report{Plan: model.PlanInfo{Dir: "infra"}}
	if err := RenderWith(&b, r, Options{Live: &Live{Version: 3, Dir: "infra"}}); err != nil {
		t.Fatal(err)
	}
	out := Archived(b.Bytes())
	m := regexp.MustCompile(`<script id="interragate-live" type="application/json">(.*?)</script>`).FindSubmatch(out)
	if m == nil {
		t.Fatal("live block missing")
	}
	var live struct {
		Historic bool `json:"historic"`
		Version  int  `json:"version"`
	}
	if err := json.Unmarshal(m[1], &live); err != nil || !live.Historic || live.Version != 3 {
		t.Errorf("live block %s: %+v, %v", m[1], live, err)
	}
	if len(out) != b.Len()+len(`"historic":true,`) {
		t.Error("archiving changed more than the live block")
	}
	// A page without a live block is left alone.
	if plain := []byte("<p>report</p>"); !bytes.Equal(Archived(plain), plain) {
		t.Error("changed a page with no live block")
	}
}
