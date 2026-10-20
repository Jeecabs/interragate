package analysis

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/Jeecabs/interragate/internal/graph"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

// applyChange makes a change for the simulation from an instance address.
func applyChange(t *testing.T, addr string, action model.Action, order string) *model.Change {
	t.Helper()
	a, err := plan.ParseAddress(strings.SplitN(addr, " ", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	return &model.Change{Address: addr, Resource: a.Config(), Module: a.ModuleString(), Mode: a.Mode,
		Type: a.Type, Name: a.Name, Key: a.Key, Action: action, Order: order}
}

// depGraph builds a configuration graph from [dependency, dependent] pairs,
// the way the report writes its edges.
func depGraph(edges ...[2]string) *graph.Graph {
	g := &graph.Graph{Nodes: map[string]*graph.Node{}}
	node := func(id string) *graph.Node {
		if n := g.Nodes[id]; n != nil {
			return n
		}
		a, _ := plan.ParseAddress(id)
		n := &graph.Node{ID: id, Module: a.ModuleString(), Mode: a.Mode, Type: a.Type, Name: a.Name}
		g.Nodes[id] = n
		return n
	}
	for _, e := range edges {
		from, to := node(e[0]), node(e[1])
		to.Up = append(to.Up, e[0])
		from.Down = append(from.Down, e[1])
	}
	return g
}

func stepsByID(ap *model.Apply) map[string]*model.ApplyStep {
	out := map[string]*model.ApplyStep{}
	for _, st := range ap.Steps {
		out[st.ID] = st
	}
	return out
}

func end(st *model.ApplyStep) int { return st.Start + st.Duration }

// direct lists the steps a step or join waits for directly, looking
// through the joins it waits for to the steps they stand for.
func direct(ap *model.Apply, after []string) []string {
	joins := map[string]*model.ApplyJoin{}
	for _, j := range ap.Joins {
		joins[j.ID] = j
	}
	var out []string
	var visit func(ids []string)
	visit = func(ids []string) {
		for _, id := range ids {
			if j := joins[id]; j != nil {
				visit(j.After)
			} else if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	visit(after)
	slices.Sort(out)
	return out
}

// checkSchedule asserts what every simulated apply must satisfy: steps are
// listed as they start, none starts before what it waits for has finished
// (directly or through a join), no more than the parallelism run at once,
// and the critical chain is a chain of waits ending when the apply does.
func checkSchedule(t *testing.T, ap *model.Apply) {
	t.Helper()
	steps := stepsByID(ap)
	for _, j := range ap.Joins {
		if len(j.After) < 2 || steps[j.ID] != nil {
			t.Errorf("join %s stands for %v", j.ID, j.After)
		}
	}
	last := 0
	for i, st := range ap.Steps {
		if i > 0 && st.Start < ap.Steps[i-1].Start {
			t.Errorf("%s is listed after a step that starts later", st.ID)
		}
		for _, id := range direct(ap, st.After) {
			if p := steps[id]; p == nil {
				t.Errorf("%s waits for unknown step %s", st.ID, id)
			} else if end(p) > st.Start {
				t.Errorf("%s starts at %d, before %s ends at %d", st.ID, st.Start, id, end(p))
			}
		}
		running := 0
		for _, o := range ap.Steps {
			if o.Start <= st.Start && st.Start < end(o) {
				running++
			}
		}
		if running > ap.Parallelism {
			t.Errorf("%d steps running at %d", running, st.Start)
		}
		last = max(last, end(st))
	}
	if last != ap.Duration {
		t.Errorf("duration = %d, last step ends at %d", ap.Duration, last)
	}
	for i := 1; i < len(ap.Critical); i++ {
		if st := steps[ap.Critical[i]]; st == nil || !slices.Contains(direct(ap, st.After), ap.Critical[i-1]) {
			t.Errorf("critical chain is broken at %s", ap.Critical[i])
		}
	}
	if n := len(ap.Critical); n == 0 || end(steps[ap.Critical[n-1]]) != ap.Duration {
		t.Errorf("critical chain %v does not end the apply", ap.Critical)
	}
}

// expectStep checks when a step starts and what it waits for directly.
func expectStep(t *testing.T, ap *model.Apply, id string, start int, after ...string) *model.ApplyStep {
	t.Helper()
	st := stepsByID(ap)[id]
	if st == nil {
		t.Fatalf("no step %s in %v", id, ap.Critical)
	}
	if st.Start != start {
		t.Errorf("%s starts at %d, want %d", id, st.Start, start)
	}
	if !slices.Equal(st.After, after) {
		t.Errorf("%s waits for %v, want %v", id, st.After, after)
	}
	return st
}

func TestApplyCreatesFollowReferences(t *testing.T) {
	// The subnet does not change, but the instance still waits for the VPC
	// behind it.
	g := depGraph(
		[2]string{"aws_vpc.main", "aws_subnet.a"},
		[2]string{"aws_subnet.a", "aws_instance.web"},
		[2]string{"aws_vpc.main", "aws_route_table.main"},
	)
	ap := simulateApply(applyInput{graph: g, changes: []*model.Change{
		applyChange(t, "aws_instance.web", model.Create, ""),
		applyChange(t, "aws_route_table.main", model.Create, ""),
		applyChange(t, "aws_s3_bucket.logs", model.Create, ""),
		applyChange(t, "aws_subnet.a", model.NoOp, ""),
		applyChange(t, "aws_vpc.main", model.Update, ""),
	}})
	checkSchedule(t, ap)
	expectStep(t, ap, "aws_vpc.main:update", 0)
	expectStep(t, ap, "aws_s3_bucket.logs:create", 0)
	expectStep(t, ap, "aws_route_table.main:create", 5, "aws_vpc.main:update")
	web := expectStep(t, ap, "aws_instance.web:create", 5, "aws_vpc.main:update")
	if !web.Known || web.Duration != 60 {
		t.Errorf("an EC2 instance takes a known minute: %+v", web)
	}
	if ap.Duration != 65 || !slices.Equal(ap.Critical, []string{"aws_vpc.main:update", "aws_instance.web:create"}) {
		t.Errorf("duration %d, critical %v", ap.Duration, ap.Critical)
	}
	if len(ap.Steps) != 4 {
		t.Errorf("a no-op takes no step: %d steps", len(ap.Steps))
	}
	// The longer chain starts first.
	if ap.Steps[0].ID != "aws_vpc.main:update" {
		t.Errorf("first step = %s", ap.Steps[0].ID)
	}
}

func TestApplyDestroysRunInReverse(t *testing.T) {
	changes := func() []*model.Change {
		return []*model.Change{
			applyChange(t, "aws_vpc.main", model.Delete, ""),
			applyChange(t, "aws_subnet.a", model.Delete, ""),
			applyChange(t, "aws_instance.web", model.Delete, ""),
		}
	}
	check := func(t *testing.T, ap *model.Apply) {
		checkSchedule(t, ap)
		expectStep(t, ap, "aws_instance.web:destroy", 0)
		expectStep(t, ap, "aws_subnet.a:destroy", 60, "aws_instance.web:destroy")
		expectStep(t, ap, "aws_vpc.main:destroy", 65, "aws_subnet.a:destroy")
	}
	t.Run("configuration", func(t *testing.T) {
		g := depGraph([2]string{"aws_vpc.main", "aws_subnet.a"}, [2]string{"aws_subnet.a", "aws_instance.web"})
		check(t, simulateApply(applyInput{graph: g, changes: changes()}))
	})
	// Removed from the configuration, the objects are ordered by what state
	// recorded; recorded dependencies are transitive.
	t.Run("state", func(t *testing.T) {
		stored := map[string][]string{
			"aws_vpc.main":     nil,
			"aws_subnet.a":     {"aws_vpc.main"},
			"aws_instance.web": {"aws_subnet.a", "aws_vpc.main"},
		}
		check(t, simulateApply(applyInput{graph: depGraph(), stored: stored, changes: changes()}))
	})
}

func TestApplyDestroyFirstReplacement(t *testing.T) {
	g := depGraph(
		[2]string{"aws_db_subnet_group.main", "aws_db_instance.orders"},
		[2]string{"aws_db_instance.orders", "aws_ssm_parameter.endpoint"},
		[2]string{"aws_db_instance.orders", "aws_cloudwatch_metric_alarm.cpu"},
		[2]string{"aws_db_instance.orders", "data.aws_db_instance.orders"},
		[2]string{"data.aws_db_instance.orders", "aws_route53_record.db"},
	)
	ap := simulateApply(applyInput{graph: g, changes: []*model.Change{
		applyChange(t, "aws_cloudwatch_metric_alarm.cpu", model.Create, ""),
		applyChange(t, "aws_db_instance.orders", model.Replace, "delete-first"),
		applyChange(t, "aws_db_subnet_group.main", model.Update, ""),
		applyChange(t, "aws_route53_record.db", model.NoOp, ""),
		applyChange(t, "aws_ssm_parameter.endpoint", model.Update, ""),
	}})
	checkSchedule(t, ap)
	destroy := expectStep(t, ap, "aws_db_instance.orders:destroy", 0)
	// What the database depended on is updated only once it is gone.
	expectStep(t, ap, "aws_db_subnet_group.main:update", 300, "aws_db_instance.orders:destroy")
	create := expectStep(t, ap, "aws_db_instance.orders:create", 305, "aws_db_subnet_group.main:update")
	expectStep(t, ap, "aws_ssm_parameter.endpoint:update", 905, "aws_db_instance.orders:create")
	expectStep(t, ap, "aws_cloudwatch_metric_alarm.cpu:create", 905, "aws_db_instance.orders:create")
	if destroy.Duration != 300 || create.Duration != 600 || !destroy.Known || !create.Known {
		t.Errorf("database durations: destroy %+v, create %+v", destroy, create)
	}
	// The new alarm only appears once the database is back, so it is not
	// affected; the DNS record reads it through a data source.
	want := []*model.ApplyGap{{Address: "aws_db_instance.orders", From: 0, To: 905,
		Dependents: []string{"aws_route53_record.db", "aws_ssm_parameter.endpoint"}}}
	if got, _ := json.Marshal(ap.Gaps); string(got) != string(must(json.Marshal(want))) {
		t.Errorf("gaps = %s", got)
	}
	if ap.Critical[0] != "aws_db_instance.orders:destroy" || ap.Critical[2] != "aws_db_instance.orders:create" {
		t.Errorf("critical = %v", ap.Critical)
	}
}

func TestApplyCreateBeforeDestroy(t *testing.T) {
	g := depGraph(
		[2]string{"aws_launch_template.web", "aws_autoscaling_group.web"},
		[2]string{"aws_autoscaling_group.web", "aws_autoscaling_policy.cpu"},
	)
	ap := simulateApply(applyInput{graph: g, changes: []*model.Change{
		applyChange(t, "aws_autoscaling_group.web", model.Update, ""),
		applyChange(t, "aws_autoscaling_policy.cpu", model.Update, ""),
		applyChange(t, "aws_launch_template.web", model.Replace, "create-first"),
	}})
	checkSchedule(t, ap)
	expectStep(t, ap, "aws_launch_template.web:create", 0)
	expectStep(t, ap, "aws_autoscaling_group.web:update", 5, "aws_launch_template.web:create")
	expectStep(t, ap, "aws_autoscaling_policy.cpu:update", 10, "aws_autoscaling_group.web:update")
	// The old object goes last, once everything that used it has moved on.
	old := expectStep(t, ap, "aws_launch_template.web:destroy", 15, "aws_autoscaling_policy.cpu:update")
	if !old.Deposed {
		t.Error("the destroy of a create-first replacement removes the deposed object")
	}
	if len(ap.Gaps) != 0 {
		t.Errorf("create_before_destroy leaves no gap: %+v", ap.Gaps[0])
	}
}

// Terraform forces create_before_destroy onto everything a resource with it
// depends on, and records that in state, so a removed object such a
// resource depends on is destroyed last too.
func TestApplyForcesCreateBeforeDestroy(t *testing.T) {
	g := depGraph([2]string{"aws_subnet.a", "aws_lb.web"})
	stored := map[string][]string{"aws_subnet.a[0]": nil, "aws_subnet.a[1]": nil, "aws_lb.web": {"aws_subnet.a"}}
	subnets := func() []*model.Change {
		return []*model.Change{applyChange(t, "aws_subnet.a[0]", model.NoOp, ""), applyChange(t, "aws_subnet.a[1]", model.Delete, "")}
	}
	t.Run("replaced create-first", func(t *testing.T) {
		ap := simulateApply(applyInput{graph: g, stored: stored, changes: append(subnets(),
			applyChange(t, "aws_lb.web", model.Replace, "create-first"))})
		checkSchedule(t, ap)
		expectStep(t, ap, "aws_lb.web:create", 0)
		expectStep(t, ap, "aws_lb.web:destroy", 180, "aws_lb.web:create")
		expectStep(t, ap, "aws_subnet.a[1]:destroy", 240, "aws_lb.web:destroy")
	})
	// The load balancer moves off the subnet before it goes.
	t.Run("configured", func(t *testing.T) {
		ap := simulateApply(applyInput{graph: g, stored: stored, cbd: map[string]bool{"aws_lb.web": true}, changes: append(subnets(),
			applyChange(t, "aws_lb.web", model.Update, ""))})
		checkSchedule(t, ap)
		expectStep(t, ap, "aws_lb.web:update", 0)
		expectStep(t, ap, "aws_subnet.a[1]:destroy", 5, "aws_lb.web:update")
	})
	// Another instance replaced create-first shows the resource has the
	// setting, which the removed instance has too.
	t.Run("same resource", func(t *testing.T) {
		stored := map[string][]string{"aws_subnet.a[0]": nil, "aws_subnet.a[1]": nil, "aws_lb.web": {"aws_subnet.a"}}
		ap := simulateApply(applyInput{graph: g, stored: stored, changes: []*model.Change{
			applyChange(t, "aws_subnet.a[0]", model.Replace, "create-first"),
			applyChange(t, "aws_subnet.a[1]", model.Delete, ""),
			applyChange(t, "aws_lb.web", model.Update, "")}})
		checkSchedule(t, ap)
		expectStep(t, ap, "aws_lb.web:update", 5, "aws_subnet.a[0]:create")
		expectStep(t, ap, "aws_subnet.a[1]:destroy", 10, "aws_lb.web:update")
	})
	// What the forced destroy depends on is forced in turn.
	t.Run("in turn", func(t *testing.T) {
		g := depGraph([2]string{"aws_vpc.v", "aws_subnet.a"}, [2]string{"aws_subnet.a", "aws_lb.web"})
		stored := map[string][]string{"aws_subnet.a[0]": {"aws_vpc.v"}, "aws_subnet.a[1]": {"aws_vpc.v"}, "aws_vpc.v[1]": nil,
			"aws_lb.web": {"aws_subnet.a"}} // recorded before the subnet's own reference
		ap := simulateApply(applyInput{graph: g, stored: stored, changes: append(subnets(),
			applyChange(t, "aws_lb.web", model.Replace, "create-first"),
			applyChange(t, "aws_vpc.v[0]", model.NoOp, ""),
			applyChange(t, "aws_vpc.v[1]", model.Delete, ""))})
		checkSchedule(t, ap)
		expectStep(t, ap, "aws_subnet.a[1]:destroy", 240, "aws_lb.web:destroy")
		expectStep(t, ap, "aws_vpc.v[1]:destroy", 245, "aws_subnet.a[1]:destroy")
	})
	// A new object did not exist when state recorded the setting.
	t.Run("new", func(t *testing.T) {
		ap := simulateApply(applyInput{graph: g, stored: stored, cbd: map[string]bool{"aws_lb.web": true}, changes: append(subnets(),
			applyChange(t, "aws_lb.web", model.Create, ""))})
		checkSchedule(t, ap)
		expectStep(t, ap, "aws_subnet.a[1]:destroy", 0)
		expectStep(t, ap, "aws_lb.web:create", 5, "aws_subnet.a[1]:destroy")
	})
}

func TestApplyDeposedObjectGoesLast(t *testing.T) {
	ap := simulateApply(applyInput{graph: depGraph(), changes: []*model.Change{
		applyChange(t, "aws_instance.web", model.Update, ""),
		func() *model.Change {
			ch := applyChange(t, "aws_instance.web (deposed object 4a2b7c1d)", model.Delete, "")
			ch.Deposed = "4a2b7c1d"
			return ch
		}(),
	}})
	checkSchedule(t, ap)
	expectStep(t, ap, "aws_instance.web:update", 0)
	if st := expectStep(t, ap, "aws_instance.web (deposed object 4a2b7c1d):destroy", 60, "aws_instance.web:update"); !st.Deposed {
		t.Error("deposed flag missing")
	}
}

func TestApplyRespectsParallelism(t *testing.T) {
	var changes []*model.Change
	for i := range 25 {
		changes = append(changes, applyChange(t, fmt.Sprintf("aws_db_instance.n%02d", i), model.Create, ""))
	}
	ap := simulateApply(applyInput{graph: depGraph(), changes: changes})
	checkSchedule(t, ap)
	starts := map[int]int{}
	for _, st := range ap.Steps {
		starts[st.Start]++
	}
	if fmt.Sprint(starts) != "map[0:10 600:10 1200:5]" || ap.Duration != 1800 {
		t.Errorf("starts = %v, duration %d", starts, ap.Duration)
	}
	// Queueing, not any one resource, explains the time.
	r := &model.Report{Changes: changes}
	if got := applyNarrative(ap, r)[0].Text; got != "The apply should take about 30 minutes, mostly because Terraform runs at most 10 operations at once." {
		t.Errorf("narrative = %q", got)
	}
}

func TestApplyStartsLongestChainFirst(t *testing.T) {
	changes := []*model.Change{
		applyChange(t, "aws_vpc.z", model.Create, ""),
		applyChange(t, "aws_eks_cluster.z", model.Create, ""),
	}
	for i := range 10 {
		changes = append(changes, applyChange(t, fmt.Sprintf("aws_s3_bucket.b%d", i), model.Create, ""))
	}
	ap := simulateApply(applyInput{graph: depGraph([2]string{"aws_vpc.z", "aws_eks_cluster.z"}), changes: changes})
	checkSchedule(t, ap)
	// Eleven steps are ready and ten can run: the VPC sorts last but has
	// the cluster behind it, so a bucket waits instead.
	expectStep(t, ap, "aws_vpc.z:create", 0)
	expectStep(t, ap, "aws_s3_bucket.b8:create", 0)
	expectStep(t, ap, "aws_s3_bucket.b9:create", 5)
	if ap.Duration != 725 {
		t.Errorf("duration = %d", ap.Duration)
	}
}

func TestApplyReadsDataAfterItsDependencies(t *testing.T) {
	g := depGraph([2]string{"local_file.policy", "data.local_file.policy"}, [2]string{"data.local_file.policy", "aws_iam_policy.app"})
	ap := simulateApply(applyInput{graph: g, changes: []*model.Change{
		applyChange(t, "aws_iam_policy.app", model.Update, ""),
		applyChange(t, "data.local_file.policy", model.Read, ""),
		applyChange(t, "local_file.policy", model.Replace, "delete-first"),
	}})
	checkSchedule(t, ap)
	read := expectStep(t, ap, "data.local_file.policy:read", 10, "local_file.policy:create")
	if read.Duration != 2 || read.Known {
		t.Errorf("read = %+v", read)
	}
	expectStep(t, ap, "aws_iam_policy.app:update", 12, "data.local_file.policy:read")
}

func TestApplyKeepsModuleInstancesApart(t *testing.T) {
	// State records dependencies per resource, so each instance's
	// recorded dependency names the subnet in every instance of the module.
	stored := map[string][]string{
		`module.app["a"].aws_instance.web`: {"module.app.aws_subnet.main"},
		`module.app["b"].aws_instance.web`: {"module.app.aws_subnet.main"},
		`module.app["a"].aws_subnet.main`:  nil,
		`module.app["b"].aws_subnet.main`:  nil,
	}
	var changes []*model.Change
	for _, key := range []string{"a", "b"} {
		changes = append(changes,
			applyChange(t, `module.app["`+key+`"].aws_instance.web`, model.Delete, ""),
			applyChange(t, `module.app["`+key+`"].aws_subnet.main`, model.Delete, ""))
	}
	ap := simulateApply(applyInput{graph: depGraph(), stored: stored, changes: changes})
	checkSchedule(t, ap)
	expectStep(t, ap, `module.app["a"].aws_subnet.main:destroy`, 60, `module.app["a"].aws_instance.web:destroy`)
	expectStep(t, ap, `module.app["b"].aws_subnet.main:destroy`, 60, `module.app["b"].aws_instance.web:destroy`)
}

// An instance waits for every instance of a resource it refers to. Rather
// than one wait for each pair, they all wait for a join, as in Terraform's
// graph, so the waits grow with the number of instances, not their product.
func TestApplyJoinsInstances(t *testing.T) {
	g := depGraph([2]string{"aws_subnet.b", "aws_instance.a"}, [2]string{"aws_subnet.b", "aws_route_table.c"})
	changes := []*model.Change{applyChange(t, "aws_route_table.c", model.Create, "")}
	for i := range 3 {
		changes = append(changes,
			applyChange(t, fmt.Sprintf("aws_subnet.b[%d]", i), model.Update, ""),
			applyChange(t, fmt.Sprintf("aws_instance.a[%d]", i), model.Update, ""))
	}
	ap := simulateApply(applyInput{graph: g, changes: changes})
	checkSchedule(t, ap)
	for _, id := range []string{"aws_instance.a[0]:update", "aws_instance.a[2]:update", "aws_route_table.c:create"} {
		expectStep(t, ap, id, 5, "aws_subnet.b[*]:update")
	}
	want := `[{"id":"aws_subnet.b[*]:update","resource":"aws_subnet.b","op":"update","after":["aws_subnet.b[0]:update","aws_subnet.b[1]:update","aws_subnet.b[2]:update"]}]`
	if got := string(must(json.Marshal(ap.Joins))); got != want {
		t.Errorf("joins = %s", got)
	}

	// 300 instances reading 300 others: 601 waits, not 90,300.
	changes = changes[:1]
	for i := range 300 {
		changes = append(changes,
			applyChange(t, fmt.Sprintf("aws_subnet.b[%d]", i), model.Update, ""),
			applyChange(t, fmt.Sprintf("aws_instance.a[%d]", i), model.Update, ""))
	}
	ap = simulateApply(applyInput{graph: g, changes: changes})
	checkSchedule(t, ap)
	waits := 0
	for _, st := range ap.Steps {
		waits += len(st.After)
	}
	for _, j := range ap.Joins {
		waits += len(j.After)
	}
	if waits != 601 || len(ap.Joins) != 1 {
		t.Errorf("%d waits through %d joins", waits, len(ap.Joins))
	}
}

// Destroys in one instance of a module are ordered against the same
// instance only, so each instance has its own join; a step outside the
// module waits for all of them through a join of those.
func TestApplyJoinsModuleInstances(t *testing.T) {
	g := depGraph([2]string{"module.app.aws_subnet.main", "aws_route_table.rt"})
	stored := map[string][]string{}
	changes := []*model.Change{applyChange(t, "aws_route_table.rt", model.Create, "")}
	for _, key := range []string{"a", "b"} {
		for i := range 2 {
			web := fmt.Sprintf(`module.app[%q].aws_instance.web[%d]`, key, i)
			subnet := fmt.Sprintf(`module.app[%q].aws_subnet.main[%d]`, key, i)
			stored[web], stored[subnet] = []string{"module.app.aws_subnet.main"}, nil
			changes = append(changes, applyChange(t, web, model.Delete, ""), applyChange(t, subnet, model.Delete, ""))
		}
	}
	ap := simulateApply(applyInput{graph: g, stored: stored, changes: changes})
	checkSchedule(t, ap)
	for _, key := range []string{"a", "b"} {
		expectStep(t, ap, fmt.Sprintf(`module.app[%q].aws_subnet.main[1]:destroy`, key), 60,
			fmt.Sprintf(`module.app[%q].aws_instance.web[*]:destroy`, key))
	}
	expectStep(t, ap, "aws_route_table.rt:create", 65, "module.app[*].aws_subnet.main[*]:destroy")
	for _, j := range ap.Joins {
		if j.ID == "module.app[*].aws_subnet.main[*]:destroy" &&
			!slices.Equal(j.After, []string{`module.app["a"].aws_subnet.main[*]:destroy`, `module.app["b"].aws_subnet.main[*]:destroy`}) {
			t.Errorf("join of every module instance = %v", j.After)
		}
	}
}

// Instances of one resource can record different dependencies, as when
// some were applied before a reference was added. What waits for those
// that depend on an object waits for a join of just those.
func TestApplyJoinsPartOfAResource(t *testing.T) {
	g := depGraph([2]string{"aws_vpc.a", "aws_subnet.b"})
	stored := map[string][]string{"aws_vpc.a": nil, "aws_subnet.b[0]": {"aws_vpc.a"}, "aws_subnet.b[1]": {"aws_vpc.a"}, "aws_subnet.b[2]": nil}
	changes := []*model.Change{applyChange(t, "aws_vpc.a", model.Replace, "create-first")}
	for i := range 3 {
		changes = append(changes, applyChange(t, fmt.Sprintf("aws_subnet.b[%d]", i), model.Update, ""))
	}
	ap := simulateApply(applyInput{graph: g, stored: stored, changes: changes})
	checkSchedule(t, ap)
	// The old VPC goes once the subnets that recorded it have moved on.
	expectStep(t, ap, "aws_vpc.a:destroy", 10, "aws_subnet.b[*]:update")
	if len(ap.Joins) != 1 || !slices.Equal(ap.Joins[0].After, []string{"aws_subnet.b[0]:update", "aws_subnet.b[1]:update"}) {
		t.Errorf("joins = %s", must(json.Marshal(ap.Joins)))
	}
}

// provide makes res's provider configuration read deps.
func provide(g *graph.Graph, res string, deps ...string) {
	if g.Nodes[res] == nil {
		a, _ := plan.ParseAddress(res)
		g.Nodes[res] = &graph.Node{ID: res, Module: a.ModuleString(), Mode: a.Mode, Type: a.Type, Name: a.Name}
	}
	g.Nodes[res].Provider = deps
}

// A provider is configured before any step that uses it, destroys
// included, so those steps wait for what its configuration reads: the
// kubernetes provider for an EKS cluster's endpoint, say.
func TestApplyWaitsForProvider(t *testing.T) {
	g := depGraph()
	provide(g, "random_pet.p")
	provide(g, "tls_private_key.new", "random_pet.p")
	changes := []*model.Change{
		applyChange(t, "random_pet.p", model.Replace, "delete-first"),
		applyChange(t, "tls_private_key.gone", model.Delete, ""),
		applyChange(t, "tls_private_key.new", model.Create, ""),
	}
	// The destroyed key is gone from the configuration; state says it
	// depended on the pet, through the provider.
	in := applyInput{graph: g, changes: changes, stored: map[string][]string{"random_pet.p": nil, "tls_private_key.gone": {"random_pet.p"}},
		provided: map[string][]string{"tls_private_key.gone": {"random_pet.p"}}}
	ap := simulateApply(in)
	checkSchedule(t, ap)
	expectStep(t, ap, "random_pet.p:destroy", 0)
	expectStep(t, ap, "random_pet.p:create", 5, "random_pet.p:destroy")
	expectStep(t, ap, "tls_private_key.new:create", 10, "random_pet.p:create")
	// Destroying the key first, as state would have it, waits for a provider
	// configured from the new pet, which waits for the old one to go: a
	// cycle. Terraform drops the destroy edges between the providers, and
	// so does the simulation, keeping the wait for the provider.
	expectStep(t, ap, "tls_private_key.gone:destroy", 10, "random_pet.p:create")
}

// Every removed resource of a provider configured from a replaced one
// closes a cycle of its own, and every one is broken the same way.
func TestApplyBreaksEveryCycle(t *testing.T) {
	g := depGraph()
	provide(g, "aws_eks_cluster.c")
	changes := []*model.Change{applyChange(t, "aws_eks_cluster.c", model.Replace, "delete-first")}
	in := applyInput{graph: g, stored: map[string][]string{"aws_eks_cluster.c": nil}, provided: map[string][]string{}}
	for i := range 300 {
		m := fmt.Sprintf("kubernetes_config_map.m%03d", i)
		changes = append(changes, applyChange(t, m, model.Delete, ""))
		in.stored[m] = []string{"aws_eks_cluster.c"}
		in.provided[m] = []string{"aws_eks_cluster.c"}
	}
	in.changes = changes
	ap := simulateApply(in)
	checkSchedule(t, ap)
	expectStep(t, ap, "aws_eks_cluster.c:destroy", 0)
	create := expectStep(t, ap, "aws_eks_cluster.c:create", 480, "aws_eks_cluster.c:destroy")
	for i := range 300 {
		if st := stepsByID(ap)[fmt.Sprintf("kubernetes_config_map.m%03d:destroy", i)]; !slices.Equal(st.After, []string{create.ID}) {
			t.Fatalf("%s waits for %v", st.ID, st.After)
		}
	}
}

// A new object depends on everything upstream of it, through resources
// that do not change too, so it waits for what is destroyed up there, and
// a destroy held back waits for it. A chain of them stays cheap.
func TestApplyNewObjectsDependOnEverythingUpstream(t *testing.T) {
	// The subnet's route tables refer to each other, as only an
	// approximate graph can.
	edges := [][2]string{{"aws_subnet.z", "aws_route_table.p"}, {"aws_route_table.p", "aws_route_table.q"},
		{"aws_route_table.q", "aws_route_table.p"}, {"aws_route_table.q", "aws_instance.x0000"}}
	changes := []*model.Change{
		applyChange(t, "aws_subnet.z[0]", model.NoOp, ""),
		applyChange(t, "aws_subnet.z[1]", model.Delete, ""),
		applyChange(t, "aws_route_table.p", model.NoOp, ""),
		applyChange(t, "aws_route_table.q", model.NoOp, ""),
		applyChange(t, "aws_instance.x0000", model.Create, ""),
	}
	const n = 3000
	for i := 1; i < n; i++ {
		edges = append(edges, [2]string{fmt.Sprintf("aws_instance.x%04d", i-1), fmt.Sprintf("aws_instance.x%04d", i)})
		changes = append(changes, applyChange(t, fmt.Sprintf("aws_instance.x%04d", i), model.Create, ""))
	}
	g := depGraph(edges...)
	stored := map[string][]string{"aws_subnet.z[1]": nil}
	ap := simulateApply(applyInput{graph: g, stored: stored, changes: changes})
	checkSchedule(t, ap)
	expectStep(t, ap, "aws_subnet.z[1]:destroy", 0)
	expectStep(t, ap, "aws_instance.x0000:create", 5, "aws_subnet.z[1]:destroy")
	expectStep(t, ap, fmt.Sprintf("aws_instance.x%04d:create", n-1), 60*n-55, fmt.Sprintf("aws_instance.x%04d:create", n-2))

	// With create_before_destroy, the subnet goes once everything that
	// depends on it is in place.
	ap = simulateApply(applyInput{graph: g, stored: stored, changes: changes, cbd: map[string]bool{"aws_subnet.z": true}})
	checkSchedule(t, ap)
	expectStep(t, ap, "aws_instance.x0000:create", 0)
	expectStep(t, ap, "aws_subnet.z[1]:destroy", 60*n, fmt.Sprintf("aws_instance.x%04d:create", n-1))
}

// What is destroyed upstream of a resource, worked out a resource at a time
// and shared, is what is destroyed among everything upstream of it, cycles
// and all.
func TestDestroyedUpstream(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 20 {
		var edges [][2]string
		name := func(i int) string { return fmt.Sprintf("aws_subnet.s%02d", i) }
		for i := range 60 {
			for range r.IntN(3) {
				edges = append(edges, [2]string{name(r.IntN(60)), name(i)})
			}
		}
		s := &applySim{in: applyInput{graph: depGraph(edges...)}, destroyed: map[string]bool{},
			upstream: map[string][]string{}, gone: map[string][]string{}}
		for i := range 60 {
			s.destroyed[name(i)] = r.IntN(3) == 0
		}
		for _, i := range r.Perm(60) {
			want := slices.DeleteFunc(slices.Clone(s.upstreamOf(name(i))), func(res string) bool { return !s.destroyed[res] })
			if got := s.destroyedUpstream(name(i)); !slices.Equal(got, want) {
				t.Fatalf("destroyed upstream of %s = %v, want %v", name(i), got, want)
			}
		}
	}
}

// Instances of one resource whose objects depend on the same resources are
// treated alike, whether state recorded that or the object is new.
func TestApplyGroupsNewAndRecordedObjects(t *testing.T) {
	g := depGraph([2]string{"aws_subnet.s", "aws_instance.web"}, [2]string{"aws_vpc.v", "aws_instance.web"})
	// The new instance comes first, then last.
	for _, keys := range [][2]string{{"0", "1"}, {"1", "0"}} {
		fresh, kept := "aws_instance.web["+keys[0]+"]", "aws_instance.web["+keys[1]+"]"
		stored := map[string][]string{
			"aws_subnet.s":        nil,
			kept:                  {"aws_subnet.s", "aws_vpc.v"},
			"aws_instance.web[2]": {"aws_vpc.v"}, // applied before it referred to the subnet
		}
		ap := simulateApply(applyInput{graph: g, stored: stored, changes: []*model.Change{
			applyChange(t, "aws_subnet.s", model.Replace, "create-first"),
			applyChange(t, fresh, model.Create, ""),
			applyChange(t, kept, model.Replace, "delete-first"),
			applyChange(t, "aws_instance.web[2]", model.Replace, "delete-first"),
			applyChange(t, "aws_vpc.v", model.NoOp, ""),
		}})
		checkSchedule(t, ap)
		// The old subnet goes once the two instances that depend on it are
		// created: the new one, and the one state says depended on it.
		expectStep(t, ap, "aws_subnet.s:destroy", 125, "aws_instance.web[*]:create")
		for _, j := range ap.Joins {
			if j.ID == "aws_instance.web[*]:create" && !slices.Equal(j.After, []string{"aws_instance.web[0]:create", "aws_instance.web[1]:create"}) {
				t.Errorf("join = %v", j.After)
			}
		}
	}
}

// Referring to a module as a whole waits for everything in it, not only
// what its outputs read.
func TestApplyWaitsForWholeModule(t *testing.T) {
	g := depGraph([2]string{"module.m.terraform_data.shown", "terraform_data.reader"})
	g.Nodes["terraform_data.reader"].Modules = []string{"module.m.terraform_data.hidden", "module.m.terraform_data.shown"}
	ap := simulateApply(applyInput{graph: g, changes: []*model.Change{
		applyChange(t, "module.m.terraform_data.hidden", model.Update, ""),
		applyChange(t, "terraform_data.reader", model.Update, ""),
	}})
	checkSchedule(t, ap)
	expectStep(t, ap, "terraform_data.reader:update", 5, "module.m.terraform_data.hidden:update")
}

// Steps list only what they wait for directly however large the plan is;
// past what reduceMemory allows at once, the work is done in passes.
func TestApplyReducesLargePlans(t *testing.T) {
	var edges [][2]string
	var changes []*model.Change
	for i := range 100 {
		// c refers to a and to b, which refers to a, so its wait for a is
		// implied by its wait for b.
		a, b, c := fmt.Sprintf("aws_vpc.a%03d", i), fmt.Sprintf("aws_subnet.b%03d", i), fmt.Sprintf("aws_instance.c%03d", i)
		edges = append(edges, [2]string{a, b}, [2]string{b, c}, [2]string{a, c})
		changes = append(changes, applyChange(t, a, model.Update, ""), applyChange(t, b, model.Create, ""), applyChange(t, c, model.Create, ""))
	}
	for i := range 10000 {
		changes = append(changes, applyChange(t, fmt.Sprintf("aws_s3_bucket.s%05d", i), model.Create, ""))
	}
	g := depGraph(edges...)
	check := func(ap *model.Apply) {
		t.Helper()
		steps := stepsByID(ap)
		for i := range 100 {
			if st := steps[fmt.Sprintf("aws_instance.c%03d:create", i)]; !slices.Equal(st.After, []string{fmt.Sprintf("aws_subnet.b%03d:create", i)}) {
				t.Fatalf("%s waits for %v", st.ID, st.After)
			}
		}
	}
	whole := simulateApply(applyInput{graph: g, changes: changes})
	check(whole)
	defer func(m int) { reduceMemory = m }(reduceMemory)
	reduceMemory = 1 // 64 bits a pass, so the 200 waits that could be implied take four
	passes := simulateApply(applyInput{graph: g, changes: changes})
	check(passes)
	if a, b := must(json.Marshal(whole)), must(json.Marshal(passes)); string(a) != string(b) {
		t.Error("the result depends on how many passes it took")
	}
}

func TestApplySurvivesACycle(t *testing.T) {
	// A destroy-first resource with a create_before_destroy dependent:
	// Terraform would force create_before_destroy on the first, so a real
	// plan never has this, but the simulation must still finish.
	g := depGraph([2]string{"aws_security_group.a", "aws_instance.b"})
	ap := simulateApply(applyInput{graph: g, changes: []*model.Change{
		applyChange(t, "aws_security_group.a", model.Replace, "delete-first"),
		applyChange(t, "aws_instance.b", model.Replace, "create-first"),
	}})
	if ap == nil || len(ap.Steps) != 4 {
		t.Fatalf("apply = %+v", ap)
	}
	checkSchedule(t, ap)
}

func TestApplyNothingToApply(t *testing.T) {
	moved := applyChange(t, "aws_s3_bucket.new", model.NoOp, "")
	moved.Moved = true
	imported := applyChange(t, "aws_s3_bucket.imported", model.NoOp, "")
	imported.Importing = true
	ap := simulateApply(applyInput{graph: depGraph(), changes: []*model.Change{
		moved, imported,
		applyChange(t, "aws_s3_bucket.same", model.NoOp, ""),
		applyChange(t, "random_uuid.legacy", model.Forget, ""),
	}})
	if ap != nil {
		t.Errorf("expected no simulation, got %d steps", len(ap.Steps))
	}
}

func TestApplyDestroyPlan(t *testing.T) {
	stored := map[string][]string{"aws_db_instance.main": {"aws_db_subnet_group.main"}, "aws_db_subnet_group.main": nil}
	changes := []*model.Change{
		applyChange(t, "aws_db_instance.main", model.Delete, ""),
		applyChange(t, "aws_db_subnet_group.main", model.Delete, ""),
		applyChange(t, "data.aws_ami.ubuntu", model.Read, ""),
	}
	ap := simulateApply(applyInput{graph: depGraph(), stored: stored, changes: changes, destroy: true})
	checkSchedule(t, ap)
	if len(ap.Steps) != 2 {
		t.Errorf("a destroy plan only destroys: %d steps", len(ap.Steps))
	}
	expectStep(t, ap, "aws_db_subnet_group.main:destroy", 300, "aws_db_instance.main:destroy")
	r := &model.Report{Changes: changes, Plan: model.PlanInfo{Destroy: true}}
	if got := applyNarrative(ap, r)[0].Text; got != "The destroy should take about 5 minutes, most of it destroying `aws_db_instance.main`." {
		t.Errorf("narrative = %q", got)
	}
}

func TestApplyNarrative(t *testing.T) {
	sg := applyChange(t, "aws_security_group.web", model.Replace, "delete-first")
	sg.Category = "firewall"
	g := depGraph([2]string{"aws_security_group.web", "aws_instance.a"}, [2]string{"aws_security_group.web", "aws_instance.b"})
	changes := []*model.Change{sg, applyChange(t, "aws_instance.a", model.Update, ""), applyChange(t, "aws_instance.b", model.NoOp, "")}

	// The instance update has a known duration, so times are given.
	ap := simulateApply(applyInput{graph: g, changes: changes})
	got := applyNarrative(ap, &model.Report{Changes: changes})
	if len(got) != 2 || got[0].Text != "The apply should take about 1 minute, most of it updating `aws_instance.a`." ||
		got[1].Text != "`aws_security_group.web` will not exist for under a minute while `aws_instance.a` and `aws_instance.b` depend on it." {
		t.Errorf("narrative = %+v", got)
	}

	// Without a known duration, nothing is said about time.
	changes[1].Cosmetic = "tags"
	ap = simulateApply(applyInput{graph: g, changes: changes})
	got = applyNarrative(ap, &model.Report{Changes: changes})
	if len(got) != 1 || got[0].Text != "`aws_security_group.web` will not exist while it is replaced, and `aws_instance.a` and `aws_instance.b` depend on it." {
		t.Errorf("narrative = %+v", got)
	}
}

func TestRoughly(t *testing.T) {
	for seconds, want := range map[int]string{
		5:    "under a minute",
		50:   "under a minute",
		59:   "under a minute",
		60:   "about 1 minute",
		89:   "about 1 minute",
		90:   "about 2 minutes",
		930:  "about 16 minutes",
		3569: "about 59 minutes",
		3570: "about 1 hour",
		3900: "about 1 hour 5 minutes",
		4200: "about 1 hour 10 minutes",
		7400: "about 2 hours 5 minutes",
	} {
		if got := roughly(seconds); got != want {
			t.Errorf("roughly(%d) = %q, want %q", seconds, got, want)
		}
	}
}

func TestApplyAWSWebapp(t *testing.T) {
	for _, file := range []string{"aws-webapp.terraform.json", "aws-webapp.tofu.json"} {
		t.Run(file, func(t *testing.T) {
			r := analyzeFixture(t, file, "../../testdata/scenarios/aws-webapp/v2")
			ap := r.Apply
			if ap == nil {
				t.Fatal("no apply simulation")
			}
			checkSchedule(t, ap)
			steps := stepsByID(ap)
			destroy, create := steps["aws_db_instance.orders:destroy"], steps["aws_db_instance.orders:create"]
			if destroy == nil || create == nil || destroy.Start != 0 || create.Start < end(destroy) {
				t.Fatalf("the database is destroyed, then created: %+v %+v", destroy, create)
			}
			for _, id := range []string{"aws_ssm_parameter.db_endpoint:update", "aws_cloudwatch_metric_alarm.db_cpu:create"} {
				if st := steps[id]; st == nil || !slices.Contains(st.After, create.ID) {
					t.Errorf("%s should wait for the new database: %+v", id, st)
				}
			}
			// The old instance is gone from the configuration; state says the
			// target group attachment still points at it.
			if st := steps["aws_instance.web:destroy"]; st == nil || !slices.Equal(st.After, []string{"aws_lb_target_group_attachment.web:destroy"}) {
				t.Errorf("aws_instance.web:destroy = %+v", st)
			}
			// 39 changes, 3 of them no-ops, and 5 replacements take two steps.
			if steps["aws_kms_alias.data:update"] != nil || len(ap.Steps) != 41 {
				t.Errorf("no-ops take no steps; %d steps", len(ap.Steps))
			}
			if ap.Critical[0] != destroy.ID || !slices.Contains(ap.Critical, create.ID) {
				t.Errorf("critical = %v", ap.Critical)
			}
			var gap *model.ApplyGap
			for _, g := range ap.Gaps {
				if g.Address == "aws_db_instance.orders" {
					gap = g
				}
			}
			if gap == nil || gap.From != 0 || gap.To != end(create) || !slices.Equal(gap.Dependents, []string{"aws_ssm_parameter.db_endpoint"}) {
				t.Errorf("database gap = %+v", gap)
			}
			if len(ap.Gaps) != 5 {
				t.Errorf("gaps = %d, want one per destroy-first replacement", len(ap.Gaps))
			}
			var text []string
			for _, s := range ap.Narrative {
				text = append(text, s.Text)
			}
			want := []string{
				// The database still has deletion protection on, and the
				// network's update waits for its destroy, so replacements
				// further on are destroyed but never recreated.
				"The apply should fail at `aws_db_instance.orders`, where deletion protection is on. The 22 resources that wait for it are skipped, which leaves `module.network.aws_subnet.public[1]`, `module.network.aws_route_table_association.public[1]` and `aws_lb_target_group_attachment.web` destroyed with nothing in their place.",
				"Once that is fixed, the apply should take about 16 minutes, most of it replacing `aws_db_instance.orders`.",
				"`aws_db_instance.orders` will not exist for about 15 minutes while `aws_ssm_parameter.db_endpoint` depends on it.",
				// The database is destroyed first and recreated last, so a
				// failure anywhere in between strands it.
				"If any of the 16 steps that recreating `aws_db_instance.orders` waits for fails, it is left destroyed with nothing in its place.",
			}
			if !slices.Equal(text, want) {
				t.Errorf("narrative = %q", text)
			}
		})
	}
}

func TestApplyMechanics(t *testing.T) {
	for _, file := range []string{"mechanics.terraform.json", "mechanics.tofu.json"} {
		t.Run(file, func(t *testing.T) {
			r := analyzeFixture(t, file, "../../testdata/scenarios/mechanics/v2")
			ap := r.Apply
			if ap == nil {
				t.Fatal("no apply simulation")
			}
			checkSchedule(t, ap)
			steps := stepsByID(ap)
			create, old := steps["random_shuffle.az:create"], steps["random_shuffle.az:destroy"]
			if create == nil || old == nil || !old.Deposed || !slices.Contains(old.After, create.ID) {
				t.Errorf("create_before_destroy: %+v then %+v", create, old)
			}
			if st := steps["data.local_file.policy_read:read"]; st == nil || !slices.Equal(st.After, []string{"local_file.policy:create"}) || st.Duration != 2 {
				t.Errorf("data source read = %+v", st)
			}
			// replace_triggered_by is a reference; the plan's configuration
			// leaves it out, the source has it.
			if st := steps["terraform_data.migrations:create"]; st == nil || !slices.Contains(st.After, "terraform_data.deploy:update") {
				t.Errorf("migrations create = %+v", st)
			}
			for _, addr := range []string{"random_string.session_key", "random_uuid.legacy_id", "random_pet.cluster", "null_resource.queue[\"emails\"]"} {
				for id := range steps {
					if strings.HasPrefix(id, addr+":") {
						t.Errorf("%s needs no step", id)
					}
				}
			}
			// Nothing here has a known duration, and every gap is a utility
			// resource, so there is nothing to say.
			if len(ap.Narrative) != 0 {
				t.Errorf("narrative = %+v", ap.Narrative)
			}
		})
	}
}

// The order of resource_changes and Go's map order must not leak into the
// simulation.
func TestApplyIsDeterministic(t *testing.T) {
	p, err := plan.ReadFile("../../testdata/plans/aws-webapp.terraform.json")
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{SourceDir: "../../testdata/scenarios/aws-webapp/v2"}
	first := must(json.Marshal(Analyze(p, opts).Apply))
	for range 5 {
		slices.Reverse(p.ResourceChanges)
		if again := must(json.Marshal(Analyze(p, opts).Apply)); string(again) != string(first) {
			t.Fatalf("the simulation changed between runs:\n%s\n%s", first, again)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// Only steps the create waits for and the destroy does not can strand a
// resource: if both wait for a step that fails, neither runs and the old
// object survives.
func TestStrandSentence(t *testing.T) {
	db := &model.Change{Address: "aws_db_instance.main", Action: model.Replace, Stateful: true}
	byAddr := map[string]*model.Change{db.Address: db}
	ap := &model.Apply{
		Steps: []*model.ApplyStep{
			{ID: "aws_kms_key.a:update"},
			{ID: "aws_subnet.b:create"},
			{ID: "aws_db_instance.main:destroy", After: []string{"aws_kms_key.a:update"}},
			{ID: "aws_db_instance.main:create", After: []string{"aws_db_instance.main:destroy", "aws_subnet.b:create"}},
		},
		Gaps: []*model.ApplyGap{{Address: "aws_db_instance.main"}},
	}
	s := strandSentence(ap, byAddr)
	if s == nil || s.Text != "If the one step that recreating `aws_db_instance.main` waits for fails, it is left destroyed with nothing in its place." {
		t.Fatalf("sentence = %+v", s)
	}
	// Both wait for the subnet: a failure there stops the destroy too.
	ap.Steps[2].After = append(ap.Steps[2].After, "aws_subnet.b:create")
	if s := strandSentence(ap, byAddr); s != nil {
		t.Errorf("nothing can strand it, got %q", s.Text)
	}
	// Nothing that holds data, nothing to say.
	db.Stateful = false
	ap.Steps[2].After = []string{"aws_kms_key.a:update"}
	if s := strandSentence(ap, byAddr); s != nil {
		t.Errorf("not stateful, got %q", s.Text)
	}
}

func TestFailSentence(t *testing.T) {
	ap := &model.Apply{Steps: []*model.ApplyStep{
		{ID: "aws_security_group.web:create", Address: "aws_security_group.web", Op: "create", Fails: "name-taken:aws_security_group.web"},
		{ID: "aws_instance.app:update", Address: "aws_instance.app", Op: "update", After: []string{"aws_security_group.web:create"}},
		{ID: "aws_security_group.web:destroy", Address: "aws_security_group.web", Op: "destroy", Deposed: true, After: []string{"aws_instance.app:update"}},
		{ID: "aws_s3_bucket.logs:create", Address: "aws_s3_bucket.logs", Op: "create"},
	}}
	findings := []*model.Finding{{ID: "name-taken:aws_security_group.web", Title: "Apply will fail: the name is taken", Addresses: []string{"aws_security_group.web"}, Fails: "create"}}
	s, _ := failSentence(ap, findings)
	want := "The apply should fail at `aws_security_group.web`, where the name is taken, so `aws_instance.app`, which waits for it, is left as it is; the rest still applies."
	if s == nil || s.Text != want {
		t.Fatalf("got %+v\nwant %q", s, want)
	}
	ap.Steps[1].Fails, ap.Steps[0].Fails = "", ""
	if s, _ := failSentence(ap, findings); s != nil {
		t.Errorf("no failing step, but got %q", s.Text)
	}
}

// What waits for a failed step through a join is skipped too; the join is
// not a resource.
func TestFailSentenceThroughJoins(t *testing.T) {
	ap := &model.Apply{
		Steps: []*model.ApplyStep{
			{ID: "aws_subnet.b[0]:update", Address: "aws_subnet.b[0]", Op: "update", Fails: "f"},
			{ID: "aws_subnet.b[1]:update", Address: "aws_subnet.b[1]", Op: "update"},
			{ID: "aws_instance.a[0]:update", Address: "aws_instance.a[0]", Op: "update", After: []string{"aws_subnet.b[*]:update"}},
			{ID: "aws_instance.a[1]:update", Address: "aws_instance.a[1]", Op: "update", After: []string{"aws_subnet.b[*]:update"}},
		},
		Joins: []*model.ApplyJoin{{ID: "aws_subnet.b[*]:update", Resource: "aws_subnet.b", Op: "update",
			After: []string{"aws_subnet.b[0]:update", "aws_subnet.b[1]:update"}}},
	}
	s, _ := failSentence(ap, []*model.Finding{{ID: "f", Addresses: []string{"aws_subnet.b[0]"}, Fails: "update"}})
	want := "The apply should fail at `aws_subnet.b[0]`, so `aws_instance.a[0]` and `aws_instance.a[1]`, which wait for it, are left as they are; the rest still applies."
	if s == nil || s.Text != want {
		t.Fatalf("got %+v\nwant %q", s, want)
	}
}

// A replacement that destroys first and then fails to create is left
// destroyed itself, as is any that waits for it.
func TestFailSentenceFailedCreate(t *testing.T) {
	secret := "aws_secretsmanager_secret.db"
	findings := []*model.Finding{{ID: "name-taken", Title: "Apply will fail: the name is taken", Addresses: []string{secret}, Fails: "create"}}
	ap := &model.Apply{Steps: []*model.ApplyStep{
		{ID: secret + ":destroy", Address: secret, Op: "destroy"},
		{ID: secret + ":create", Address: secret, Op: "create", After: []string{secret + ":destroy"}, Fails: "name-taken"},
	}}
	s, n := failSentence(ap, findings)
	want := "The apply should fail at `aws_secretsmanager_secret.db`, where the name is taken. Nothing else waits for it, so the rest still applies, but `aws_secretsmanager_secret.db` is left destroyed with nothing in its place."
	if s == nil || s.Text != want || n != 1 {
		t.Fatalf("got %+v, %d failures\nwant %q", s, n, want)
	}
	version := "aws_secretsmanager_secret_version.db"
	ap.Steps = append([]*model.ApplyStep{{ID: version + ":destroy", Address: version, Op: "destroy"}}, ap.Steps...)
	ap.Steps = append(ap.Steps, &model.ApplyStep{ID: version + ":create", Address: version, Op: "create", After: []string{secret + ":create"}})
	s, _ = failSentence(ap, findings)
	want = "The apply should fail at `aws_secretsmanager_secret.db`, where the name is taken. The one resource that waits for it is skipped, which leaves `aws_secretsmanager_secret.db` and `aws_secretsmanager_secret_version.db` destroyed with nothing in their place."
	if s == nil || s.Text != want || !slices.Equal(s.Refs, []string{secret, version}) {
		t.Fatalf("got %+v\nwant %q", s, want)
	}
}

// Only a step that runs can fail: one that waits for an earlier failure is
// skipped, and fails once that one is fixed.
func TestFailSentenceChainedFailures(t *testing.T) {
	findings := []*model.Finding{
		{ID: "protected", Title: "Apply will fail: deletion protection is on", Addresses: []string{"aws_db_instance.a"}, Fails: "destroy"},
		{ID: "shrink", Title: "Apply will fail: storage cannot shrink", Addresses: []string{"aws_db_instance.b"}, Fails: "update"},
	}
	ap := &model.Apply{Steps: []*model.ApplyStep{
		{ID: "aws_db_instance.a:destroy", Address: "aws_db_instance.a", Op: "destroy", Fails: "protected"},
		{ID: "aws_db_instance.a:create", Address: "aws_db_instance.a", Op: "create", After: []string{"aws_db_instance.a:destroy"}},
		{ID: "aws_db_instance.b:update", Address: "aws_db_instance.b", Op: "update", After: []string{"aws_db_instance.a:create"}, Fails: "shrink"},
	}}
	s, n := failSentence(ap, findings)
	want := "The apply should fail at `aws_db_instance.a`, where deletion protection is on, so `aws_db_instance.b`, which waits for it, is left as it is; the rest still applies. " +
		"Fixing that is not enough: `aws_db_instance.b` should fail as well, where storage cannot shrink."
	if s == nil || s.Text != want || n != 2 {
		t.Fatalf("got %+v, %d failures\nwant %q", s, n, want)
	}
	// Failures that do not wait for each other both happen.
	ap.Steps[2].After = nil
	s, n = failSentence(ap, findings)
	want = "The apply should fail at `aws_db_instance.a` and `aws_db_instance.b`; nothing else waits for them, so the rest still applies."
	if s == nil || s.Text != want || n != 2 {
		t.Fatalf("got %+v, %d failures\nwant %q", s, n, want)
	}
}

// A replacement that creates first and whose old object's destroy is
// skipped is left with both objects, not as it was.
func TestFailSentenceLeavesBothObjects(t *testing.T) {
	ap := &model.Apply{Steps: []*model.ApplyStep{
		{ID: "aws_iam_role_policy.p:update", Address: "aws_iam_role_policy.p", Op: "update", Fails: "f"},
		{ID: "aws_launch_template.t:create", Address: "aws_launch_template.t", Op: "create"},
		{ID: "aws_autoscaling_group.a:update", Address: "aws_autoscaling_group.a", Op: "update",
			After: []string{"aws_iam_role_policy.p:update", "aws_launch_template.t:create"}},
		{ID: "aws_launch_template.t:destroy", Address: "aws_launch_template.t", Op: "destroy", Deposed: true,
			After: []string{"aws_autoscaling_group.a:update"}},
	}}
	s, _ := failSentence(ap, []*model.Finding{{ID: "f", Addresses: []string{"aws_iam_role_policy.p"}, Fails: "update"}})
	want := "The apply should fail at `aws_iam_role_policy.p`. The 2 resources that wait for it are skipped, which leaves `aws_launch_template.t` with its old object as well as the new one."
	if s == nil || s.Text != want {
		t.Fatalf("got %+v\nwant %q", s, want)
	}
}

// Joins are not steps, so they do not count among what a replacement waits
// for.
func TestStrandSentenceThroughJoins(t *testing.T) {
	db := &model.Change{Address: "aws_db_instance.main", Action: model.Replace, Stateful: true}
	ap := &model.Apply{
		Steps: []*model.ApplyStep{
			{ID: "aws_subnet.b[0]:create"},
			{ID: "aws_subnet.b[1]:create"},
			{ID: "aws_db_instance.main:destroy"},
			{ID: "aws_db_instance.main:create", After: []string{"aws_db_instance.main:destroy", "aws_subnet.b[*]:create"}},
		},
		Joins: []*model.ApplyJoin{{ID: "aws_subnet.b[*]:create", After: []string{"aws_subnet.b[0]:create", "aws_subnet.b[1]:create"}}},
		Gaps:  []*model.ApplyGap{{Address: "aws_db_instance.main"}},
	}
	s := strandSentence(ap, map[string]*model.Change{db.Address: db})
	if s == nil || !strings.HasPrefix(s.Text, "If any of the 2 steps that recreating") {
		t.Fatalf("sentence = %+v", s)
	}
}

func TestSkipApply(t *testing.T) {
	p, err := plan.ReadFile("../../testdata/plans/aws-webapp.terraform.json")
	if err != nil {
		t.Fatal(err)
	}
	if r := Analyze(p, Options{SkipApply: true}); r.Apply != nil {
		t.Error("the simulation should be left out")
	}
	if r := Analyze(p, Options{}); r.Apply == nil {
		t.Error("the simulation is missing")
	}
}

// A resource gone from the configuration is destroyed with the provider
// recorded in state; the plan names only its source, so its default
// configuration stands in, and the destroy waits for what that reads.
func TestApplyProviderOfRemovedResource(t *testing.T) {
	p, err := plan.Parse([]byte(`{"format_version":"1.2","terraform_version":"1.16.6","planned_values":{},
	  "resource_changes":[
	    {"address":"random_pet.p","mode":"managed","type":"random_pet","name":"p","provider_name":"registry.terraform.io/hashicorp/random",
	      "change":{"actions":["delete","create"],"before":{"id":"a"},"after":{},"after_unknown":{"id":true}}},
	    {"address":"tls_private_key.gone","mode":"managed","type":"tls_private_key","name":"gone","provider_name":"registry.terraform.io/hashicorp/tls",
	      "action_reason":"delete_because_no_resource_config","change":{"actions":["delete"],"before":{"id":"b"},"after":null}}
	  ],
	  "prior_state":{"values":{"root_module":{"resources":[
	    {"address":"random_pet.p","mode":"managed","type":"random_pet","name":"p","provider_name":"registry.terraform.io/hashicorp/random","values":{}},
	    {"address":"tls_private_key.gone","mode":"managed","type":"tls_private_key","name":"gone","provider_name":"registry.terraform.io/hashicorp/tls",
	      "values":{},"depends_on":["random_pet.p"]}
	  ]}}},
	  "configuration":{
	    "provider_config":{
	      "random":{"name":"random","full_name":"registry.terraform.io/hashicorp/random"},
	      "tls":{"name":"tls","full_name":"registry.terraform.io/hashicorp/tls",
	        "expressions":{"proxy":[{"url":{"references":["random_pet.p.id","random_pet.p"]}}]}}
	    },
	    "root_module":{"resources":[{"address":"random_pet.p","mode":"managed","type":"random_pet","name":"p","provider_config_key":"random"}]}
	  }}`))
	if err != nil {
		t.Fatal(err)
	}
	ap := Analyze(p, Options{}).Apply
	checkSchedule(t, ap)
	expectStep(t, ap, "tls_private_key.gone:destroy", 10, "random_pet.p:create")
	expectStep(t, ap, "random_pet.p:destroy", 0)
}
