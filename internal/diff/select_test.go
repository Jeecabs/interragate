package diff

import "testing"

func TestSelect(t *testing.T) {
	obj := map[string]any{
		"endpoint": "db.example.com",
		"ingress":  []any{map[string]any{"from_port": 22.0}, map[string]any{"from_port": 443.0}},
		"policy":   `{"Statement":[{"Action":"s3:*"}]}`,
		"password": "hunter2",
	}
	unknown := map[string]any{"arn": true}
	sensitive := map[string]any{"password": true}

	one := func(steps ...Step) Selection {
		t.Helper()
		got := Select(obj, unknown, sensitive, steps)
		if len(got) != 1 {
			t.Fatalf("Select(%v) = %d values, want 1", steps, len(got))
		}
		return got[0]
	}
	if s := one(Step{Key: "endpoint"}); s.Value != "db.example.com" || s.Opaque() {
		t.Errorf("endpoint = %+v", s)
	}
	if s := one(Step{Key: "arn"}); !s.IsUnknown() || s.Value != nil {
		t.Errorf("arn = %+v, want it known only after apply", s)
	}
	if s := one(Step{Key: "password"}); !s.Opaque() || s.At("password") != "password" {
		t.Errorf("password = %+v, want it sensitive", s)
	}
	if s := one(Step{Key: "policy"}, Step{Key: "Statement"}, Step{Key: "Action"}); s.Value != "s3:*" {
		t.Errorf("JSON in a string = %+v", s)
	}
	if got := Select(obj, nil, nil, []Step{{Key: "ingress"}, {Any: true}, {Key: "from_port"}}); len(got) != 2 || got[1].At("") != "ingress[1].from_port" {
		t.Errorf("ingress.*.from_port = %+v", got)
	}
}

func TestDiffers(t *testing.T) {
	steps := []Step{{Key: "endpoint"}}
	before := Select(map[string]any{"endpoint": "a"}, nil, nil, steps)
	for _, c := range []struct {
		after   map[string]any
		unknown map[string]any
		want    bool
	}{
		{map[string]any{"endpoint": "a"}, nil, false},
		{map[string]any{"endpoint": "b"}, nil, true},
		{map[string]any{}, map[string]any{"endpoint": true}, true}, // known only after apply
	} {
		if _, got := Differs(before, Select(c.after, c.unknown, nil, steps)); got != c.want {
			t.Errorf("Differs(a, %v, unknown %v) = %v, want %v", c.after, c.unknown, got, c.want)
		}
	}
	if !Equivalent(nil, []any{}) || !Equivalent(`{"a": 1}`, `{"a":1}`) || Equivalent("a", "b") {
		t.Error("Equivalent should treat null as empty and reformatted JSON as the same")
	}
}
