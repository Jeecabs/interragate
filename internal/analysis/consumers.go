package analysis

import (
	"path/filepath"
	"slices"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/consumers"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/rules"
)

// declares reports whether the configuration declares services outside
// Terraform, whose outages need the apply simulated.
func declares(cfg *config.Config) bool { return cfg != nil && len(cfg.Consumers) > 0 }

// timeline is the simulated apply and where it is expected to stop, for
// the rules that read it.
func timeline(ap *model.Apply) *consumers.Timeline {
	t := &consumers.Timeline{Apply: ap}
	if fl := expectFailure(ap); fl != nil {
		t.StopsAt, t.Halted = fl.failingAt, fl.halted
	}
	return t
}

// consumers adds what the plan does to each service outside Terraform that
// the configuration file declares: the service's own list, a sentence in
// the summary, and its outages in the apply's summary. Package consumers
// works it out, and the rules have reported it as findings, so only what
// they show counts here.
func (a *analyzer) consumers(r *model.Report, ctx *rules.Context) {
	cfg := a.opts.Config
	if !declares(cfg) {
		return
	}
	reported := make(map[*model.Finding]bool, len(r.Findings))
	for _, f := range r.Findings {
		reported[f] = true
	}
	shown := func(f *model.Finding) bool { return reported[f] }
	assessment := ctx.Consumers()
	r.Consumers = assessment.Services(shown, cfg.SeverityFor)

	tool := r.Plan.ToolName()
	if r.Apply != nil {
		for _, o := range assessment.Reported(shown) {
			// The apply's summary may already say the resource goes missing;
			// then it only needs to say who else notices.
			if s := o.Mentioned(r.Apply.Narrative); s != nil {
				o.Extend(s, tool)
				continue
			}
			r.Apply.Narrative = append(r.Apply.Narrative, o.Sentence(tool))
		}
	}
	file := config.FileNames[0]
	if cfg.Path != "" {
		file = filepath.Base(cfg.Path)
	}
	if s := consumers.Summary(r.Consumers, file, tool); s != nil {
		// The summary is in order of severity; the sentence joins it there.
		at := slices.IndexFunc(r.Narrative, func(n model.Sentence) bool { return n.Severity < s.Severity })
		if at < 0 {
			at = len(r.Narrative)
		}
		r.Narrative = slices.Insert(r.Narrative, at, *s)
	}
}
