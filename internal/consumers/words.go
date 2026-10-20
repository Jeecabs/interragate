package consumers

// This file says everything the package finds: from the plan's side for
// findings, which name the change and every service it affects, and from
// each service's side for its own list. Text uses backticks around
// addresses and values, as the rest of the report does.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
)

// user is a service that something the plan does affects, with what it
// calls the value and how a new value reaches it.
type user struct {
	svc    *config.Consumer
	as     []string
	update model.UpdatePath
}

// users are services in the order they were declared.
type users []user

// usersOf lists the services a group of impacts affects.
func usersOf(group []Impact) users {
	var us users
	for _, im := range group {
		us = us.with(im.Of(), im.Update())
	}
	return us
}

// usersOfMatches lists the services that make some uses.
func usersOfMatches(ms []*Match) users {
	var us users
	for _, m := range ms {
		us = us.with(m, UpdatePathOf(m.Service, m.Use))
	}
	return us
}

func (us users) with(m *Match, update model.UpdatePath) users {
	i := slices.IndexFunc(us, func(u user) bool { return u.svc == m.Service })
	if i < 0 {
		us = append(us, user{svc: m.Service, update: update})
		i = len(us) - 1
	}
	if a := m.Use.As; a != "" && !slices.Contains(us[i].as, a) {
		us[i].as = append(us[i].as, a)
	}
	return us
}

// names lists the services by name: "storefront-web and storefront-app".
func (us users) names() string {
	ns := make([]string, len(us))
	for i, u := range us {
		ns[i] = plain(string(u.svc.Name))
	}
	return list(ns, 3)
}

// verb picks the form of a verb that agrees with the services.
func (us users) verb(one, many string) string {
	if len(us) == 1 {
		return one
	}
	return many
}

// holding names the services with the verb and what each calls the value:
// "storefront-web holds it as `DATABASE_URL`", or "storefront-web (as `A`)
// and storefront-app (as `B`) hold it". it adds the object.
func (us users) holding(one, many string, it bool) string {
	object := ""
	if it {
		object = " it"
	}
	if len(us) == 1 {
		text := plain(string(us[0].svc.Name)) + " " + one + object
		if len(us[0].as) > 0 {
			text += " as " + list(codes(us[0].as), 3)
		}
		return text
	}
	parts := make([]string, len(us))
	for i, u := range us {
		parts[i] = plain(string(u.svc.Name))
		if len(u.as) > 0 {
			parts[i] += " (as " + list(codes(u.as), 3) + ")"
		}
	}
	return list(parts, 4) + " " + many + object
}

// byUpdate groups services by how a new value reaches them, the slowest
// first, since what they need is the most pressing.
func (us users) byUpdate() []users {
	order := []model.UpdatePath{model.UpdateStore, model.UpdateOTA, model.UpdateManual, model.UpdateRebuild, model.UpdateRedeploy, model.UpdateLive, model.UpdateUnknown}
	var out []users
	for _, path := range order {
		var group users
		for _, u := range us {
			if u.update == path {
				group = append(group, u)
			}
		}
		if len(group) > 0 {
			out = append(out, group)
		}
	}
	return out
}

func (us users) serviceNames() []model.ServiceName {
	out := make([]model.ServiceName, len(us))
	for i, u := range us {
		out[i] = u.svc.Name
	}
	return out
}

// findingFor makes the finding that reports a group of impacts that one
// change has on one or more services. file is the configuration file's
// name, and tool is Terraform or OpenTofu.
func findingFor(group []Impact, file, tool string) *model.Finding {
	us := usersOf(group)
	f := &model.Finding{Addresses: []string{group[0].Subject()}, Consumers: us.serviceNames()}
	for _, im := range group {
		f.Severity = max(f.Severity, levelFor(im))
	}
	declared := " (declared in " + code(file) + ")"
	switch first := group[0].(type) {
	case *Loss:
		first.describe(f, us, declared)
	case *Shift:
		first.describe(f, us, declared)
	case *Repoint:
		first.describe(f, us, declared)
	case *Closure:
		first.describe(f, us, declared, tool)
	default:
		panic(fmt.Sprintf("consumers: an impact of unknown type %T", first))
	}
	return f
}

func (l *Loss) describe(f *model.Finding, us users, declared string) {
	who := us.names()
	host := code(string(l.use.Host))
	switch {
	case l.change == nil:
		f.Short = "removes an output " + who + " " + us.verb("reads", "read")
		f.Message = "Removes output " + code(outputName(l.at)) + ", which " + us.holding("reads", "read", false) + declared + "."
	case l.use.Host != "" && l.Unanswered:
		f.Short = "stops answering for a host " + who + " " + us.verb("calls", "call")
		f.Message = code(l.at) + " stops answering for " + host + "; " + who + " " + us.verb("calls", "call") + " that host" + declared + "."
	case l.use.Host != "":
		f.Short = "destroys what answers for a host " + who + " " + us.verb("calls", "call")
		f.Message = "Destroys " + code(l.at) + ", which answers for " + host + "; " + who + " " + us.verb("calls", "call") + " that host" + declared + "."
	default:
		f.Short = "destroys what " + who + " " + us.verb("uses", "use")
		f.Message = "Destroys " + code(l.at) + ", which " + us.holding("uses", "use", false) + declared + "."
	}
	if s := l.lingers(us); s != "" {
		f.Message += " " + s
	}
	var remedies []string
	for _, group := range us.byUpdate() {
		names := group.names()
		switch group[0].update {
		case model.UpdateStore, model.UpdateOTA:
			remedies = append(remedies, "Ship a version of "+names+" that no longer uses it, and keep it until the versions people still run do not need it.")
		case model.UpdateManual:
			remedies = append(remedies, "Agree a date with "+names+", and keep it until "+group.verb("it has", "they have")+" moved off it.")
		default:
			remedies = append(remedies, "Move "+names+" off it before applying, or keep it until nothing uses it.")
		}
	}
	f.Remediation = strings.Join(slices.Compact(remedies), " ")
}

// lingers says what losing something means for services that cannot move
// off it quickly: apps already installed, and partners.
func (l *Loss) lingers(us users) string {
	var out []string
	installed := false
	for _, group := range us.byUpdate() {
		names := group.names()
		switch group[0].update {
		case model.UpdateStore:
			installed = true
			out = append(out, "Copies of "+names+" already installed keep using it until people update.")
		case model.UpdateOTA:
			installed = true
			out = append(out, "An over-the-air update can point copies of "+names+" on a compatible build elsewhere when they next open; the rest keep using it until people update.")
		case model.UpdateManual:
			out = append(out, names+" "+group.verb("has", "have")+" to stop using it on their side.")
		}
	}
	// Bucket names are global: once one is gone, anyone may take its name.
	if installed && l.change != nil && l.change.Type == "aws_s3_bucket" {
		out = append(out, "Bucket names are global, so once it is gone anyone can create one with its name and receive what installed copies send.")
	}
	return strings.Join(out, " ")
}

func (s *Shift) describe(f *model.Finding, us users, declared string) {
	who := us.names()
	f.Short = "changes a value " + who + " " + us.verb("holds", "hold")
	if s.Path != "" {
		f.Paths = []string{s.Path}
	}
	switch {
	case s.change == nil && s.Unknown:
		f.Message = "Leaves output " + code(outputName(s.at)) + " known only after apply, so it may change; " + us.holding("reads", "read", true) + declared + "."
	case s.change == nil:
		f.Message = "Changes output " + code(outputName(s.at)) + s.fromTo() + ", which " + us.holding("reads", "read", false) + declared + "."
	case s.Identity:
		f.Short = "replaces what " + who + " " + us.verb("uses", "use")
		f.Message = "Replacing " + code(s.at) + " puts a new one in its place, with a new " + code(s.Path) + knownAfter(s.Unknown) + "; " + us.holding("uses", "use", true) + declared + "."
		f.Remediation = "Check how " + who + " " + us.verb("refers", "refer") + " to it before applying: a reference by ID or ARN needs the new one once it exists."
		return
	case s.Unknown:
		f.Message = doing(s.change) + " " + code(s.at) + " leaves its " + code(s.Path) + " known only after apply, so it may change; " + us.holding("holds", "hold", true) + declared + "."
	default:
		f.Message = doing(s.change) + " " + code(s.at) + " changes its " + code(s.Path) + s.fromTo() + ", which " + us.holding("holds", "hold", false) + declared + "."
	}
	if c := consequences(us, s.Unknown); c != "" {
		f.Message += " " + c
	}
	f.Remediation = valueRemedies(us, s.Unknown)
}

func (s *Shift) fromTo() string {
	if s.Before == "" || s.After == "" {
		return ""
	}
	return " from " + code(s.Before) + " to " + code(s.After)
}

// consequences says how soon each service takes a new value, a clause for
// each way one reaches them. A value known only after apply may not change
// at all, so then it says what happens if it does.
func consequences(us users, unknown bool) string {
	var clauses []string
	for _, group := range us.byUpdate() {
		names := group.names()
		switch group[0].update {
		case model.UpdateLive:
			clauses = append(clauses, names+" "+group.verb("reads", "read")+" it as "+group.verb("it runs", "they run")+" and "+group.verb("picks", "pick")+" up the new value by "+group.verb("itself", "themselves"))
		case model.UpdateRedeploy:
			clauses = append(clauses, names+" "+group.verb("takes", "take")+" the new value on "+group.verb("its", "their")+" next deployment")
		case model.UpdateRebuild:
			clauses = append(clauses, "it is built into "+names+", so "+group.verb("it needs", "they need")+" a new build to take the new value")
		case model.UpdateOTA:
			clauses = append(clauses, "an over-the-air update gives it to copies of "+names+" on a compatible build when they next open, and the rest keep the old value")
		case model.UpdateStore:
			clauses = append(clauses, "copies of "+names+" already installed keep the old value until people update")
		case model.UpdateManual:
			clauses = append(clauses, names+" "+group.verb("has", "have")+" to change it on their side")
		}
	}
	if len(clauses) == 0 {
		return ""
	}
	text := strings.Join(clauses, "; ")
	if unknown {
		return "If it does, " + text + "."
	}
	// A clause may start with a service's name, which keeps its spelling.
	if first := us.byUpdate()[0][0]; !strings.HasPrefix(text, plain(string(first.svc.Name))) {
		text = capitalize(text)
	}
	return text + "."
}

// valueRemedies says what to do for each service whose value changes, the
// most pressing first.
func valueRemedies(us users, unknown bool) string {
	check := "After applying, "
	if unknown {
		check = "After applying, check whether the value changed; if it did, "
	}
	var out []string
	for _, group := range us.byUpdate() {
		for _, u := range group {
			name := plain(string(u.svc.Name))
			set := "give " + name + " the new value"
			if len(u.as) > 0 {
				set = "set " + list(codes(u.as), 3) + " in " + name + " to the new value"
			}
			var r string
			switch u.update {
			case model.UpdateLive:
				continue
			case model.UpdateRedeploy:
				r = check + set + " and redeploy."
			case model.UpdateRebuild:
				r = check + set + " and build it again."
			case model.UpdateOTA:
				r = check + "publish an update of " + name + " with the new value, and keep the old one working for builds an update cannot reach."
			case model.UpdateStore:
				r = "Keep the old value working until no version of " + name + " people still run holds it, for example behind a stable DNS name."
				if unknown {
					r = "Find out before applying whether the value will change. If it will, keep the old one working until no version of " + name + " people still run holds it."
				}
			case model.UpdateManual:
				r = "Give " + name + " the new value before applying, and keep the old one working until they switch."
				if unknown {
					r = "Find out before applying whether the value will change. If it will, agree the switch with " + name + "."
				}
			default:
				r = check + "update " + name + " with the new value."
			}
			out = append(out, r)
		}
	}
	return strings.Join(slices.Compact(out), " ")
}

func (r *Repoint) describe(f *model.Finding, us users, declared string) {
	who := us.names()
	host := code(string(r.use.Host))
	f.Short = "points a host " + who + " " + us.verb("calls", "call") + " somewhere else"
	f.Message = "Changes where " + host + " points (" + code(r.at) + "); " + who + " " + us.verb("calls", "call") + " that host" + declared + "."
	if r.Unknown {
		f.Message += " Where it points next is known only after apply."
	}
	f.Message += " " + capitalize(us.verb("it follows", "they follow")) + " the change as DNS caches expire."
	f.Remediation = "Check that what " + host + " points at next serves what " + who + " " + us.verb("needs", "need") + "."
}

func (c *Closure) describe(f *model.Finding, us users, declared, tool string) {
	who := us.names()
	f.Short = "closes the way in for " + who
	f.Message = "Closes " + code(c.Port.String()) + c.from() + " on " + code(c.at) + ", the way in for " + who + declared + "."
	if c.Drifted {
		f.Message += " The rule was added outside " + tool + "; if it was added for " + who + ", copy it into the configuration before applying."
	}
	f.Remediation = "Keep the rule in the configuration, or agree a date with " + who + " before closing it."
}

// from says where the firewall stops letting the port in from.
func (c *Closure) from() string {
	if len(c.Lost) == 0 {
		return ""
	}
	sources := make([]string, len(c.Lost))
	for i, s := range c.Lost {
		sources[i] = code(s.String())
	}
	return " from " + list(sources, 3)
}

// ServiceSide says a loss from the service's side.
func (l *Loss) ServiceSide(string) string {
	u := usersOf([]Impact{l})
	var text string
	switch {
	case l.change == nil:
		text = "Reads output " + code(outputName(l.at)) + as(l.use) + ", which this plan removes."
	case l.use.Host != "" && l.Unanswered:
		text = "Calls " + code(string(l.use.Host)) + named(l.use) + "; " + code(l.at) + " stops answering for it."
	case l.use.Host != "":
		text = "Calls " + code(string(l.use.Host)) + named(l.use) + "; this plan destroys " + code(l.at) + ", which answers for it."
	default:
		text = "Uses " + code(l.at) + as(l.use) + ", which this plan destroys."
	}
	if s := l.lingers(u); s != "" {
		text += " " + s
	}
	return text
}

// ServiceSide says a changing value from the service's side.
func (s *Shift) ServiceSide(string) string {
	var text string
	switch {
	case s.change == nil && s.Unknown:
		text = "Reads output " + code(outputName(s.at)) + as(s.use) + ", which is known only after apply, so it may change."
	case s.change == nil:
		text = "Reads output " + code(outputName(s.at)) + as(s.use) + ", which changes" + s.fromTo() + "."
	case s.Identity:
		return "Uses " + code(s.at) + as(s.use) + ", which this plan replaces with a new one, with a new " + code(s.Path) + knownAfter(s.Unknown) + "."
	case s.Unknown:
		text = "Holds the " + code(s.Path) + " of " + code(s.at) + as(s.use) + "; " + strings.ToLower(doing(s.change)) + " it leaves the value known only after apply, so it may change."
	default:
		text = "Holds the " + code(s.Path) + " of " + code(s.at) + as(s.use) + ", which changes" + s.fromTo()
		if s.change.Action == model.Replace {
			text += " when it is replaced"
		}
		text += "."
	}
	if c := consequences(usersOf([]Impact{s}), s.Unknown); c != "" {
		text += " " + c
	}
	return text
}

// ServiceSide says a moved host from the service's side.
func (r *Repoint) ServiceSide(string) string {
	text := "Calls " + code(string(r.use.Host)) + named(r.use) + ", which this plan points somewhere else (" + code(r.at) + ")"
	if r.Unknown {
		text += "; where it points next is known only after apply"
	}
	return text + ". It follows the change as DNS caches expire."
}

// ServiceSide says a closed way in from the service's side.
func (c *Closure) ServiceSide(tool string) string {
	text := "Comes in through " + code(c.Port.String()) + c.from() + " on " + code(c.at) + ", which this plan closes."
	if c.Drifted {
		text += " The rule was added outside " + tool + "; if it was added for this service, copy it into the configuration before applying."
	}
	return text
}

// Finding reports an outage, naming every service it affects.
func (o *Outage) Finding(file, tool string) *model.Finding {
	us := usersOfMatches(o.Uses)
	f := &model.Finding{Addresses: []string{o.Culprit}, Consumers: us.serviceNames()}
	for _, u := range us {
		f.Severity = max(f.Severity, levelOf(u.svc))
	}
	f.Short = "takes away what " + us.names() + " " + us.verb("relies", "rely") + " on during the apply"
	f.Message = o.say(us, tool, " (declared in "+code(file)+")")
	switch {
	case o.NoTarget:
		f.Remediation = "Keep a target registered while " + code(o.Culprit) + " is replaced, for example with lifecycle { create_before_destroy = true } on it, or agree a time for the apply with " + us.names() + "."
	default:
		f.Remediation = "Agree a time for the apply with " + us.names() + ", or keep " + code(o.Culprit) + " in place with lifecycle { create_before_destroy = true } if two can exist at once."
	}
	if len(o.Stuck) > 0 {
		f.Remediation = "Fix the failure expected at " + list(codes(o.Stuck), 3) + " before applying, since it leaves this down until it is fixed. " + f.Remediation
	}
	return f
}

// Sentence says an outage for the apply's summary, naming every service it
// affects.
func (o *Outage) Sentence(tool string) model.Sentence {
	us := usersOfMatches(o.Uses)
	s := model.Sentence{Text: o.say(us, tool, ""), Refs: o.refs()}
	for _, u := range us {
		s.Severity = max(s.Severity, levelOf(u.svc))
	}
	return s
}

// say says an outage from the plan's side.
func (o *Outage) say(us users, tool, declared string) string {
	who := us.names()
	switch {
	case strings.HasPrefix(o.Subject, "output."):
		return "Output " + code(outputName(o.Subject)) + ", which " + who + " " + us.verb("reads", "read") + " outside " + tool + declared +
			", comes from " + code(o.Culprit) + ", which will not exist" + o.when(true) + o.stays(true) + "."
	case o.Uses[0].Host != "":
		return code(o.Subject) + ", which " + who + " " + us.verb("calls", "call") + " outside " + tool + declared + ", " + o.state() + o.when(false) + o.stays(false) + "."
	}
	return code(o.Subject) + ", which " + who + " " + us.verb("uses", "use") + " outside " + tool + declared + ", " + o.state() + o.when(o.Culprit == o.Subject) + o.stays(o.Culprit == o.Subject) + "."
}

// ServiceSide says an outage from one service's side.
func (o *Outage) ServiceSide(m *Match) string {
	switch {
	case m.Output != nil:
		return "Reads output " + code(m.Output.Name) + as(m) + "; " + code(o.Culprit) + ", which it comes from, will not exist" + o.when(true) + o.stays(true) + "."
	case m.Host != "":
		return "Calls " + code(o.Subject) + named(m) + ", which " + o.state() + o.when(false) + o.stays(false) + "."
	}
	return "Uses " + code(o.Subject) + as(m) + ", which " + o.state() + o.when(o.Culprit == o.Subject) + o.stays(o.Culprit == o.Subject) + "."
}

// state says what happens to what the services rely on.
func (o *Outage) state() string {
	switch {
	case o.NoTarget:
		return "has no target"
	case o.Culprit == o.Subject:
		return "will not exist"
	}
	return "goes down"
}

// when says how long an outage lasts and what it waits for; self is set
// when the sentence is about the culprit itself.
func (o *Outage) when(self bool) string {
	it := code(o.Culprit)
	if self {
		it = "it"
	}
	if o.Window.Open {
		return " once " + it + " is destroyed"
	}
	text := ""
	if o.Timed {
		text = " for " + model.Roughly(o.Window.length())
	}
	return text + " while " + it + " is replaced"
}

// stays says what the expected failure does to the outage: leaves it
// without an end, or puts it off until the failure is fixed. self is set
// when the sentence is about the culprit itself.
func (o *Outage) stays(self bool) string {
	alone := func(at []string) bool { return self && len(at) == 1 && at[0] == o.Culprit }
	switch {
	case len(o.Stuck) > 0 && o.NoTarget:
		return ", and none at all if the apply stops at " + list(codes(o.Stuck), 3) + ", as interragate expects"
	case alone(o.Stuck):
		// Its destroy runs, so what fails is creating its replacement.
		return ", and stays missing if its replacement fails, as interragate expects"
	case len(o.Stuck) > 0:
		return ", and stays missing if the apply stops at " + list(codes(o.Stuck), 3) + ", as interragate expects"
	case alone(o.OnceFixed):
		// Its destroy does not run, so what fails is destroying it.
		return ", once the failure expected in destroying it is fixed"
	case len(o.OnceFixed) > 0:
		return ", once the failure expected at " + list(codes(o.OnceFixed), 3) + " is fixed"
	}
	return ""
}

// Mentioned finds the sentence in the apply's summary that already says the
// resource the services use will not exist while it is replaced, or nil.
func (o *Outage) Mentioned(ns []model.Sentence) *model.Sentence {
	if o.NoTarget || o.Culprit != o.Subject {
		return nil
	}
	for i := range ns {
		if len(ns[i].Refs) > 0 && ns[i].Refs[0] == o.Culprit && strings.HasPrefix(ns[i].Text, code(o.Culprit)+" will not exist") {
			return &ns[i]
		}
	}
	return nil
}

// Extend adds the services an outage affects to the sentence that already
// says the resource will not exist.
func (o *Outage) Extend(s *model.Sentence, tool string) {
	us := usersOfMatches(o.Uses)
	for _, u := range us {
		s.Severity = max(s.Severity, levelOf(u.svc))
	}
	s.Text = strings.TrimSuffix(s.Text, ".") + ", and " + us.names() + " " + us.verb("uses", "use") + " it outside " + tool + "."
}

// Summary says which declared services the plan affects, for the report's
// summary, or nil when it affects none; each service's list says how.
func Summary(cs []*model.Consumer, file, tool string) *model.Sentence {
	var names, refs []string
	s := &model.Sentence{}
	for _, c := range model.ByStanding(cs) {
		if c.Standing() != model.StandingAffected {
			continue
		}
		names = append(names, plain(string(c.Name)))
		s.Severity = max(s.Severity, c.Worst())
		for _, e := range c.Effects {
			for _, ref := range e.Refs {
				if !slices.Contains(refs, ref) {
					refs = append(refs, ref)
				}
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	s.Refs = refs
	if len(names) == 1 {
		s.Text = "Affects " + names[0] + ", a service outside " + tool + " that " + code(file) + " declares."
		return s
	}
	s.Text = "Affects " + strconv.Itoa(len(names)) + " services outside " + tool + " that " + code(file) + " declares: " + list(names, 4) + "."
	return s
}

// as says what a use calls a value: " as `DATABASE_URL`".
func as(m *Match) string {
	if m.Use.As == "" {
		return ""
	}
	return " as " + code(m.Use.As)
}

// named says what a use calls a host it calls, in brackets: " (`API_URL`)".
func named(m *Match) string {
	if m.Use.As == "" {
		return ""
	}
	return " (" + code(m.Use.As) + ")"
}

func knownAfter(unknown bool) string {
	if unknown {
		return " known only after apply"
	}
	return ""
}

// doing says what the plan does to a change, to start a sentence.
func doing(ch *model.Change) string {
	if ch.Action == model.Replace {
		return "Replacing"
	}
	return "Updating"
}

func outputName(address string) string { return strings.TrimPrefix(address, "output.") }

// code wraps an address or value in backticks for the report's markup.
func code(s string) string { return "`" + plain(s) + "`" }

func codes(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = code(x)
	}
	return out
}

// plain makes text from a plan or a configuration file safe in prose: no
// backticks, which delimit code, no line breaks, and a bounded length.
func plain(s string) string {
	s = strings.NewReplacer("`", "'", "\n", " ", "\r", " ").Replace(s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return s
}

// shown renders a value for a code span: a string as it is, anything else as
// the plan writes it.
func shown(v any) string {
	if s, ok := v.(string); ok {
		return plain(s)
	}
	return plain(diff.String(v))
}

// list joins items as "a, b and c", counting those beyond max.
func list(items []string, max int) string {
	if len(items) > max {
		items = append(slices.Clone(items[:max]), strconv.Itoa(len(items)-max)+" more")
	}
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
