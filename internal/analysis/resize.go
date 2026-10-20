package analysis

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/rules"
)

// sizeAttrs are the arguments that set how big a resource is.
var sizeAttrs = map[string]bool{
	"instance_type": true, "instance_class": true, "db_cluster_instance_class": true,
	"node_type": true, "host_instance_type": true, "machine_type": true,
}

// armFamily matches EC2 families on Arm (Graviton) processors: a1, and a
// generation number followed by g, as in t4g, m6gd and c7gn.
var armFamily = regexp.MustCompile(`^(a1|[a-z]+\d+g[a-z]*)$`)

// awsSizes and googleSizes give each named size in units of the next, and
// 2xlarge and up (or a Google vCPU count) follow on. Within a family each
// step doubles the vCPUs or memory, and the on-demand price with them.
var (
	awsSizes    = map[string]float64{"nano": 1.0 / 16, "micro": 1.0 / 8, "small": 1.0 / 4, "medium": 1.0 / 2, "large": 1, "xlarge": 2}
	googleSizes = map[string]float64{"micro": 1.0 / 4, "small": 1.0 / 2, "medium": 1}
)

// instanceSize splits an instance type into its family and its size, in
// units that compare within the family: t3.micro, db.r6g.2xlarge,
// cache.m5.large, e2-medium, n2-standard-8. ok is false for names it
// cannot read, such as db.serverless, m5.metal or a custom machine type.
func instanceSize(s string) (family string, size float64, ok bool) {
	for _, p := range []string{"db.", "cache.", "mq."} {
		s = strings.TrimPrefix(s, p)
	}
	if strings.Contains(s, "custom") {
		return "", 0, false
	}
	if fam, sz, found := strings.Cut(s, "."); found {
		if v, ok := awsSizes[sz]; ok {
			return fam, v, true
		}
		if n, err := strconv.Atoi(strings.TrimSuffix(sz, "xlarge")); err == nil && n > 0 && strings.HasSuffix(sz, "xlarge") {
			return fam, 2 * float64(n), true
		}
		return "", 0, false
	}
	// Google names the size last, often as a number of vCPUs.
	if i := strings.LastIndex(s, "-"); i > 0 {
		fam, sz := s[:i], s[i+1:]
		if v, ok := googleSizes[sz]; ok {
			return fam, v, true
		}
		if n, err := strconv.Atoi(sz); err == nil && n > 0 {
			return fam, float64(n), true
		}
	}
	return "", 0, false
}

// resizeSentence says which way an update resizes a resource and, within a
// family, by how much. An EC2 instance that moves between x86 and Arm keeps
// its image, which only boots on the architecture it was built for.
func resizeSentence(ch *model.Change, root *diff.Node) string {
	if root == nil {
		return ""
	}
	for _, n := range root.Children {
		if !sizeAttrs[n.Key] || n.Kind != diff.Changed {
			continue
		}
		before, ok1 := n.Before.(string)
		after, ok2 := n.After.(string)
		if !ok1 || !ok2 {
			continue
		}
		fb, sb, okb := instanceSize(before)
		fa, sa, oka := instanceSize(after)
		if !okb || !oka {
			return ""
		}
		from := " from " + rules.Code(before) + " to " + rules.Code(after)
		var s string
		switch {
		case fa != fb && sa == sb:
			s = "It changes family at the same size," + from + "."
		case fa != fb:
			s = "It " + grows(sa > sb) + from + ", and changes family."
		case sa == sb:
			return ""
		default:
			s = "It " + grows(sa > sb) + from
			if r := ratio(sa / sb); r != "" {
				s += ": " + r + " the size and the on-demand price."
			} else {
				s += ", within its family."
			}
		}
		if ch.Type == "aws_instance" || ch.Type == "aws_launch_template" {
			if arm := armFamily.MatchString(fa); arm != armFamily.MatchString(fb) {
				arch, cpu := "x86_64", "x86"
				if arm {
					arch, cpu = "arm64", "Arm (Graviton)"
				}
				s += " " + fa + " runs on " + cpu + " processors, so this only works if the image is built for " + arch + "."
			}
		}
		return s
	}
	return ""
}

func grows(larger bool) string {
	if larger {
		return "grows"
	}
	return "shrinks"
}

// ratio says how many times bigger, or what fraction of, a size is, when
// that is a whole number or its reciprocal.
func ratio(r float64) string {
	words := []string{"", "", "twice", "three times", "four times", "five times", "six times", "seven times", "eight times", "nine times", "ten times"}
	parts := []string{"", "", "half", "a third of", "a quarter of", "a fifth of", "a sixth of", "a seventh of", "an eighth of", "a ninth of", "a tenth of"}
	whole := func(x float64) (int, bool) {
		n := int(x + 0.5)
		return n, n >= 2 && x-float64(n) < 1e-9 && float64(n)-x < 1e-9
	}
	if n, ok := whole(r); ok {
		if n < len(words) {
			return words[n]
		}
		return strconv.Itoa(n) + " times"
	}
	if n, ok := whole(1 / r); ok {
		if n < len(parts) {
			return parts[n]
		}
		return "1/" + strconv.Itoa(n) + " of"
	}
	return ""
}
