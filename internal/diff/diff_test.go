package diff

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	if s == "" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return v
}

func find(n *Node, path string) *Node {
	if n.PathString() == path {
		return n
	}
	for _, c := range n.Children {
		if f := find(c, path); f != nil {
			return f
		}
	}
	return nil
}

func TestUpdateScalarsAndUnknown(t *testing.T) {
	n := Compute(Input{
		Before:       decode(t, `{"id":"i-1","instance_type":"t3.small","arn":"arn:1","tags":{"a":"1"}}`),
		After:        decode(t, `{"id":"i-1","instance_type":"t3.large","tags":{"a":"1"}}`),
		AfterUnknown: decode(t, `{"arn":true}`),
		HasBefore:    true, HasAfter: true,
	})
	if n.Kind != Changed {
		t.Fatalf("root kind = %s", n.Kind)
	}
	it := find(n, "instance_type")
	if it.Kind != Changed || it.Before != "t3.small" || it.After != "t3.large" {
		t.Errorf("instance_type = %+v", it)
	}
	arn := find(n, "arn")
	if arn.Kind != Changed || !arn.Unknown || arn.Before != "arn:1" {
		t.Errorf("arn = %+v", arn)
	}
	if find(n, "tags").Kind != Unchanged || find(n, "id").Kind != Unchanged {
		t.Error("unchanged attributes reported as changed")
	}
}

func TestListInsertionDoesNotShift(t *testing.T) {
	n := Compute(Input{
		Before:    decode(t, `{"ingress":[{"port":80,"cidr":"0.0.0.0/0"},{"port":443,"cidr":"0.0.0.0/0"}]}`),
		After:     decode(t, `{"ingress":[{"port":22,"cidr":"0.0.0.0/0"},{"port":80,"cidr":"0.0.0.0/0"},{"port":443,"cidr":"0.0.0.0/0"}]}`),
		HasBefore: true, HasAfter: true,
	})
	ing := find(n, "ingress")
	var kinds []string
	for _, c := range ing.Children {
		kinds = append(kinds, string(c.Kind))
	}
	if got := strings.Join(kinds, ","); got != "added,unchanged,unchanged" {
		t.Fatalf("element kinds = %s", got)
	}
	if find(n, "ingress[0].port").After.(json.Number) != "22" {
		t.Error("added element should be at after index 0")
	}
}

func TestListModifiedObjectPairs(t *testing.T) {
	n := Compute(Input{
		Before:    decode(t, `{"rule":[{"port":22,"cidr":"10.0.0.0/8","proto":"tcp"}]}`),
		After:     decode(t, `{"rule":[{"port":22,"cidr":"0.0.0.0/0","proto":"tcp"}]}`),
		HasBefore: true, HasAfter: true,
	})
	rule := find(n, "rule")
	if len(rule.Children) != 1 || rule.Children[0].Kind != Changed {
		t.Fatalf("expected one modified element, got %d children", len(rule.Children))
	}
	cidr := find(n, "rule[0].cidr")
	if cidr == nil || cidr.Before != "10.0.0.0/8" || cidr.After != "0.0.0.0/0" {
		t.Fatalf("cidr = %+v", cidr)
	}
}

func TestJSONStringIsDiffedStructurally(t *testing.T) {
	before := `{"Version":"2012-10-17","Statement":[{"Sid":"Read","Effect":"Allow","Action":["s3:GetObject"],"Resource":["*"]}]}`
	after := `{"Version":"2012-10-17","Statement":[{"Sid":"Read","Effect":"Allow","Action":["s3:GetObject"],"Resource":["*"]},{"Sid":"Debug","Effect":"Allow","Action":["s3:*"],"Resource":["*"]}]}`
	n := Compute(Input{
		Before:    map[string]any{"policy": before},
		After:     map[string]any{"policy": after},
		HasBefore: true, HasAfter: true,
	})
	p := find(n, "policy")
	if p.Encoding != "json" || p.Kind != Changed {
		t.Fatalf("policy = %+v", p)
	}
	added := find(n, "policy.Statement[1]")
	if added == nil || added.Kind != Added {
		t.Fatalf("expected an added statement, got %+v", added)
	}
	if sid := find(n, "policy.Statement[1].Sid"); sid == nil || sid.After != "Debug" {
		t.Fatalf("sid = %+v", sid)
	}
	if find(n, "policy.Statement[0]").Kind != Unchanged {
		t.Error("existing statement should be unchanged")
	}
}

func TestJSONReformatIsTrivial(t *testing.T) {
	n := Compute(Input{
		Before:    map[string]any{"doc": `{"a":1,"b":2}`},
		After:     map[string]any{"doc": "{\n  \"b\": 2,\n  \"a\": 1\n}"},
		HasBefore: true, HasAfter: true,
	})
	if d := find(n, "doc"); d.Kind != Changed || !d.Trivial {
		t.Fatalf("doc = %+v", d)
	}
}

func TestSensitiveValuesAreMasked(t *testing.T) {
	n := Compute(Input{
		Before:          decode(t, `{"password":"old","name":"db"}`),
		After:           decode(t, `{"password":"new","name":"db"}`),
		BeforeSensitive: decode(t, `{"password":true}`),
		AfterSensitive:  decode(t, `{"password":true}`),
		HasBefore:       true, HasAfter: true,
	})
	pw := find(n, "password")
	if !pw.Sensitive || pw.Kind != Changed || pw.Before != nil || pw.After != nil {
		t.Fatalf("password = %+v", pw)
	}
	out, _ := json.Marshal(n)
	if strings.Contains(string(out), "old") || strings.Contains(string(out), `"new"`) {
		t.Fatalf("sensitive value leaked: %s", out)
	}
}

func TestSensitiveValuesOnCreateAreMasked(t *testing.T) {
	n := Compute(Input{
		After:          decode(t, `{"password":"hunter2","settings":[{"token":"abc","name":"x"}]}`),
		AfterSensitive: decode(t, `{"password":true,"settings":[{"token":true}]}`),
		HasAfter:       true,
	})
	out, _ := json.Marshal(n)
	if strings.Contains(string(out), "hunter2") || strings.Contains(string(out), "abc") {
		t.Fatalf("sensitive value leaked: %s", out)
	}
	if !find(n, "settings[0].token").Sensitive {
		t.Fatal("nested sensitive value not marked")
	}
}

func TestReplacePathsMarkForces(t *testing.T) {
	n := Compute(Input{
		Before:       decode(t, `{"storage_encrypted":false,"kms_key_id":null,"ingress":[{"cidr":"a","port":1}]}`),
		After:        decode(t, `{"storage_encrypted":true,"kms_key_id":"arn:k","ingress":[{"cidr":"b","port":1}]}`),
		ReplacePaths: [][]any{{"storage_encrypted"}, {"kms_key_id"}, {"ingress", json.Number("0"), "cidr"}},
		HasBefore:    true, HasAfter: true,
	})
	for _, p := range []string{"storage_encrypted", "kms_key_id", "ingress[0].cidr"} {
		if !find(n, p).Forces {
			t.Errorf("%s should force replacement", p)
		}
	}
}

func TestCreateHidesNulls(t *testing.T) {
	n := Compute(Input{
		After:        decode(t, `{"name":"x","description":null,"tags":{}}`),
		AfterUnknown: decode(t, `{"id":true}`),
		HasAfter:     true,
	})
	if n.Kind != Added {
		t.Fatalf("kind = %s", n.Kind)
	}
	if find(n, "description") != nil {
		t.Error("null attribute should be omitted from a create")
	}
	if id := find(n, "id"); id == nil || !id.Unknown {
		t.Error("unknown id should be listed")
	}
}

func TestMultilineStrings(t *testing.T) {
	before := "1\n2\n3\n4\n5\n6\nx\n7\n8\n9\n10\n11\n"
	after := "1\n2\n3\n4\n5\n6\nX\n7\n8\n9\n10\n11\n"
	n := Compute(Input{
		Before:    map[string]any{"user_data": before},
		After:     map[string]any{"user_data": after},
		HasBefore: true, HasAfter: true,
	})
	ud := find(n, "user_data")
	var b strings.Builder
	for _, l := range ud.Lines {
		if l.Op == "skip" {
			b.WriteString("~")
			continue
		}
		b.WriteString(l.Op + l.Text + "|")
	}
	if got, want := b.String(), "~ 4| 5| 6|-x|+X| 7| 8| 9|~"; got != want {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

func TestMyersAgainstNaive(t *testing.T) {
	cases := [][2]string{
		{"", "a\nb\n"},
		{"a\nb\n", ""},
		{"a\nb\nc", "a\nc"},
		{"x\ny\nz", "z\ny\nx"},
		{"same", "same"},
	}
	for _, c := range cases {
		ops, ok := myers(splitLines(c[0]), splitLines(c[1]))
		if !ok {
			t.Fatalf("myers gave up on %q", c)
		}
		var a, b []string
		for _, o := range ops {
			if o.Op != "+" {
				a = append(a, o.Text)
			}
			if o.Op != "-" {
				b = append(b, o.Text)
			}
		}
		if strings.Join(a, "\n") != strings.Join(splitLines(c[0]), "\n") || strings.Join(b, "\n") != strings.Join(splitLines(c[1]), "\n") {
			t.Errorf("script for %q does not reproduce inputs: %+v", c, ops)
		}
	}
}

func TestPathString(t *testing.T) {
	got := PathString([]any{"tags", "kubernetes.io/role", "x", 2, "y"})
	if want := `tags["kubernetes.io/role"].x[2].y`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// Review regressions.

func TestTypeChangeKeepsSecretsMasked(t *testing.T) {
	n := Compute(Input{
		Before:          decode(t, `{"input":{"password":"hunter2-SECRET","user":"app"}}`),
		After:           decode(t, `{"input":["app","rotated"]}`),
		BeforeSensitive: decode(t, `{"input":{"password":true}}`),
		AfterSensitive:  decode(t, `{"input":[false,true]}`),
		HasBefore:       true, HasAfter: true,
	})
	out, _ := json.Marshal(n)
	for _, secret := range []string{"hunter2-SECRET", "rotated"} {
		if strings.Contains(string(out), secret) {
			t.Fatalf("type change leaked %q: %s", secret, out)
		}
	}
	if in := find(n, "input"); in == nil || in.Kind != Changed {
		t.Fatalf("input = %+v", in)
	}
}

func TestJSONArrayStringsAreLists(t *testing.T) {
	n := Compute(Input{
		Before:    map[string]any{"defs": `[{"name":"web","image":"nginx:1"}]`},
		After:     map[string]any{"defs": `[{"name":"web","image":"nginx:2"}]`},
		HasBefore: true, HasAfter: true,
	})
	d := find(n, "defs")
	if d.Type != "list" || d.Encoding != "json" {
		t.Fatalf("defs = %+v", d)
	}
	if img := find(n, "defs[0].image"); img == nil || img.After != "nginx:2" {
		t.Fatalf("image = %+v", img)
	}
}

func TestJSONReformatIsALeaf(t *testing.T) {
	n := Compute(Input{
		Before:    map[string]any{"doc": `{"a":1}`},
		After:     map[string]any{"doc": "{ \"a\": 1 }"},
		HasBefore: true, HasAfter: true,
	})
	leaves := n.Leaves()
	if len(leaves) != 1 || !leaves[0].Trivial {
		t.Fatalf("leaves = %+v", leaves)
	}
}

func TestDecodeJSONRejectsTrailingData(t *testing.T) {
	for _, s := range []string{`{"a":1}}`, `[1]]`, `{"a":1} x`} {
		if _, ok := DecodeJSON(s); ok {
			t.Errorf("DecodeJSON(%q) should fail", s)
		}
	}
	if _, ok := DecodeJSON("  {\"a\":1}\n"); !ok {
		t.Error("surrounding whitespace is fine")
	}
}

func TestLongListInsertion(t *testing.T) {
	var before, after []any
	for i := 0; i < 600; i++ {
		before = append(before, fmt.Sprintf("10.0.%d.0/24", i))
	}
	after = append([]any{"192.168.0.0/24"}, before...)
	n := Compute(Input{Before: map[string]any{"cidrs": before}, After: map[string]any{"cidrs": after}, HasBefore: true, HasAfter: true})
	if leaves := n.Leaves(); len(leaves) != 1 || leaves[0].Kind != Added {
		t.Fatalf("one inserted element should be one change, got %d", len(leaves))
	}
}

func TestTrailingNewlineIsNoted(t *testing.T) {
	n := Compute(Input{
		Before:    map[string]any{"script": "echo a\necho b"},
		After:     map[string]any{"script": "echo a\necho b\n"},
		HasBefore: true, HasAfter: true,
	})
	s := find(n, "script")
	last := s.Lines[len(s.Lines)-1]
	if last.Op != "note" || !strings.Contains(last.Text, "trailing newline") {
		t.Fatalf("lines = %+v", s.Lines)
	}
}

func TestLargeRewriteIsBounded(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&a, "old line %d\n", i)
		fmt.Fprintf(&b, "new line %d\n", i)
	}
	lines := lineDiff(a.String(), b.String())
	if len(lines) != 6000 {
		t.Fatalf("a full rewrite should fall back to - and + blocks, got %d lines", len(lines))
	}
}

func TestLargeIntegersCompareExactly(t *testing.T) {
	if Equal(json.Number("9007199254740993"), json.Number("9007199254740992")) {
		t.Error("integers above 2^53 that differ must not compare equal")
	}
	if !Equal(json.Number("1"), json.Number("1.0")) {
		t.Error("1 and 1.0 are the same number")
	}
}
