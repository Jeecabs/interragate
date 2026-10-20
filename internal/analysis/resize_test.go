package analysis

import (
	"testing"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
)

func TestInstanceSize(t *testing.T) {
	cases := []struct {
		in     string
		family string
		ok     bool
	}{
		{"t3.micro", "t3", true},
		{"db.r6g.2xlarge", "r6g", true},
		{"cache.m5.large", "m5", true},
		{"e2-medium", "e2", true},
		{"n2-standard-8", "n2-standard", true},
		{"Standard_D2s_v3", "", false},
		{"db.serverless", "", false},
		{"n2-custom-4-16384", "", false},
		{"m5.metal", "", false},
	}
	for _, c := range cases {
		fam, _, ok := instanceSize(c.in)
		if fam != c.family || ok != c.ok {
			t.Errorf("instanceSize(%q) = %q, %v", c.in, fam, ok)
		}
	}
	order := []string{"t3.nano", "t3.micro", "t3.small", "t3.medium", "t3.large", "t3.xlarge", "t3.2xlarge", "m5.12xlarge"}
	for i := 1; i < len(order); i++ {
		_, a, _ := instanceSize(order[i-1])
		_, b, _ := instanceSize(order[i])
		if a >= b {
			t.Errorf("%s (%v) should be smaller than %s (%v)", order[i-1], a, order[i], b)
		}
	}
	for fam, arm := range map[string]bool{"t4g": true, "m6gd": true, "c7gn": true, "a1": true, "g5g": true, "hpc7g": true, "im4gn": true, "m5": false, "g4dn": false, "c5n": false, "t3a": false, "m7i": false} {
		if armFamily.MatchString(fam) != arm {
			t.Errorf("armFamily(%q) = %v, want %v", fam, !arm, arm)
		}
	}
}

func TestResizeSentence(t *testing.T) {
	cases := []struct{ typ, attr, before, after, want string }{
		{"aws_instance", "instance_type", "t3.micro", "t3.large", "It grows from `t3.micro` to `t3.large`: eight times the size and the on-demand price."},
		{"aws_db_instance", "instance_class", "db.r6g.2xlarge", "db.r6g.large", "It shrinks from `db.r6g.2xlarge` to `db.r6g.large`: a quarter of the size and the on-demand price."},
		{"aws_instance", "instance_type", "m5.2xlarge", "m5.3xlarge", "It grows from `m5.2xlarge` to `m5.3xlarge`, within its family."},
		{"aws_elasticache_cluster", "node_type", "cache.m5.large", "cache.m5.24xlarge", "It grows from `cache.m5.large` to `cache.m5.24xlarge`: 48 times the size and the on-demand price."},
		{"aws_db_instance", "instance_class", "db.r6g.xlarge", "db.r5.large", "It shrinks from `db.r6g.xlarge` to `db.r5.large`, and changes family."},
		{"aws_instance", "instance_type", "m5.large", "m6g.large", "It changes family at the same size, from `m5.large` to `m6g.large`. m6g runs on Arm (Graviton) processors, so this only works if the image is built for arm64."},
		{"aws_instance", "instance_type", "t4g.small", "t3.medium", "It grows from `t4g.small` to `t3.medium`, and changes family. t3 runs on x86 processors, so this only works if the image is built for x86_64."},
		{"aws_db_instance", "instance_class", "db.m5.large", "db.m6g.large", "It changes family at the same size, from `db.m5.large` to `db.m6g.large`."},
		{"google_compute_instance", "machine_type", "n2-standard-2", "n2-standard-8", "It grows from `n2-standard-2` to `n2-standard-8`: four times the size and the on-demand price."},
		{"google_compute_instance", "machine_type", "e2-medium", "e2-standard-2", "It grows from `e2-medium` to `e2-standard-2`, and changes family."},
		{"aws_db_instance", "instance_class", "db.t3.micro", "db.serverless", ""},
		{"aws_instance", "ami", "ami-1", "ami-2", ""},
	}
	for _, c := range cases {
		ch := &model.Change{Type: c.typ}
		root := &diff.Node{Children: []*diff.Node{{Key: c.attr, Kind: diff.Changed, Before: c.before, After: c.after}}}
		if got := resizeSentence(ch, root); got != c.want {
			t.Errorf("%s %s → %s:\n got %q\nwant %q", c.typ, c.before, c.after, got, c.want)
		}
	}
}
