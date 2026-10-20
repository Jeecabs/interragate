package main

import (
	"testing"

	"github.com/Jeecabs/interragate/internal/delta"
	"github.com/Jeecabs/interragate/internal/model"
)

// Only a save that takes the plan to high or critical risk is worth
// interrupting someone for.
func TestRiseWorthANotice(t *testing.T) {
	at := func(level model.Severity) *model.Report {
		return &model.Report{Risk: model.Risk{Level: level, Headline: "Destroys an S3 bucket."}}
	}
	d := &delta.Delta{Sentences: []model.Sentence{{Text: "The verdict rose from Low risk to Critical risk."}, {Text: "New: Destroying `aws_s3_bucket.logs` deletes every object."}}}
	title, body, ok := riseWorthANotice(at(model.Low), at(model.Critical), d)
	if !ok || title != "interragate: Critical risk" || body != "Destroying aws_s3_bucket.logs deletes every object." {
		t.Errorf("rise: %v %q %q", ok, title, body)
	}
	for _, c := range []struct{ was, now model.Severity }{{model.Low, model.Medium}, {model.High, model.High}, {model.Critical, model.High}} {
		if _, _, ok := riseWorthANotice(at(c.was), at(c.now), d); ok {
			t.Errorf("%s to %s should not notify", c.was, c.now)
		}
	}
	if _, _, ok := riseWorthANotice(nil, at(model.Critical), nil); ok {
		t.Error("the first plan should not notify")
	}
}
