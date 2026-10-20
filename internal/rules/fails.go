package rules

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/kb"
	"github.com/Jeecabs/interragate/internal/model"
)

// failureRules flag an apply that is bound to fail partway, leaving
// whatever ran before the failure applied and the rest not.
func failureRules() []*Rule {
	return []*Rule{
		{
			ID: "name-taken", Title: "Apply will fail: the name is taken", Severity: model.High, Category: Correctness,
			Doc: "Flags a replacement whose new object keeps a name that must be unique. With create_before_destroy the old object " +
				"still holds the name when the new one is created; some services (Secrets Manager, Cloud SQL) keep a deleted object's " +
				"name for a while, so even destroying first does not free it.",
			Check: checkNameTaken,
		},
		{
			ID: "engine-downgrade", Title: "Apply will fail: the engine version goes down", Severity: model.High, Category: Correctness,
			Doc: "Flags an in-place change to an older database or cache engine version, which the service refuses. It usually means " +
				"the service upgraded the minor version itself (RDS and ElastiCache do, with automatic minor version upgrades) and the configuration still pins the old one.",
			Check: checkEngineDowngrade,
		},
		{
			ID: "major-upgrade-not-allowed", Title: "Apply will fail: a major upgrade is not allowed", Severity: model.High, Category: Correctness,
			Doc:   "Flags an RDS or Aurora engine upgrade to a new major version without allow_major_version_upgrade, which RDS rejects.",
			Check: checkMajorUpgrade,
		},
		{
			ID: "storage-shrink", Title: "Apply will fail: storage cannot shrink", Severity: model.High, Category: Correctness,
			Doc: "Flags an in-place update that makes a database's, volume's, broker's or file system's storage smaller. The service refuses: " +
				"storage only grows, and smaller storage means a new resource and moving the data.",
			Check: checkStorageShrink,
		},
		{
			ID: "eks-version-skip", Title: "Apply will fail: EKS upgrades one version at a time", Severity: model.High, Category: Correctness,
			Doc: "Flags an EKS cluster version change that skips a Kubernetes minor version or goes back one. EKS upgrades the control " +
				"plane one minor version at a time and never downgrades it.",
			Check: checkEKSVersion,
		},
	}
}

// storage is an argument that sets how much storage a resource has, and the
// service that refuses to shrink it.
type storage struct{ attr, service string }

var storageAttrs = map[string]storage{
	"aws_db_instance":             {"allocated_storage", "RDS"},
	"aws_rds_cluster":             {"allocated_storage", "RDS"},
	"aws_ebs_volume":              {"size", "EBS"},
	"aws_instance":                {"root_block_device.volume_size", "EBS"},
	"aws_msk_cluster":             {"broker_node_group_info.storage_info.ebs_storage_info.volume_size", "MSK"},
	"aws_fsx_lustre_file_system":  {"storage_capacity", "FSx"},
	"aws_fsx_windows_file_system": {"storage_capacity", "FSx"},
	"aws_fsx_ontap_file_system":   {"storage_capacity", "FSx"},
}

func checkStorageShrink(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Update) {
		st, ok := storageAttrs[ch.Type]
		if !ok {
			continue
		}
		bv, ok1 := Attr(c.Before(ch), st.attr)
		av, ok2 := Attr(c.After(ch), st.attr)
		if !ok1 || !ok2 || bv == nil || av == nil {
			continue
		}
		before, after := atoi(bv), atoi(av)
		if after <= 0 || after >= before {
			continue
		}
		name := st.attr[strings.LastIndex(st.attr, ".")+1:]
		f := finding(ch, "", fmt.Sprintf("The apply will fail: it would shrink %s on %s from %d to %d GiB, and %s cannot make storage smaller.",
			Code(name), Code(ch.Address), before, after, st.service))
		f.Remediation = fmt.Sprintf("Keep %s at %d or more. Smaller storage means a new resource and copying the data across.", name, before)
		if max, ok := c.Before(ch)["max_allocated_storage"]; ok && max != nil && atoi(max) > 0 {
			f.Message += " Storage autoscaling (max_allocated_storage) has probably grown it since it was configured."
			f.Remediation = fmt.Sprintf("Set %s to %d, or add it to lifecycle ignore_changes so autoscaling's growth stops showing up as a change.", name, before)
		}
		f.Short = "will fail on storage that cannot shrink"
		f.Fails = "update"
		f.Paths = []string{st.attr}
		out = append(out, f)
	}
	return out
}

// minorVersion reads a Kubernetes version such as "1.29" as its minor
// number within major 1.
func minorVersion(v string) (int, bool) {
	major, minor, ok := strings.Cut(v, ".")
	if !ok || major != "1" {
		return 0, false
	}
	n, err := strconv.Atoi(minor)
	return n, err == nil
}

func checkEKSVersion(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Update) {
		if ch.Type != "aws_eks_cluster" || c.Unknown(ch, "version") {
			continue
		}
		before, _ := c.Before(ch)["version"].(string)
		after, _ := c.After(ch)["version"].(string)
		b, ok1 := minorVersion(before)
		a, ok2 := minorVersion(after)
		if !ok1 || !ok2 || a == b || a == b+1 {
			continue
		}
		var f *model.Finding
		if a < b {
			f = finding(ch, "", fmt.Sprintf("The apply will fail: it would move %s from Kubernetes %s back to %s, and EKS cannot downgrade a cluster.", Code(ch.Address), before, after))
			f.Remediation = fmt.Sprintf("Set version back to %s. Going back means a new cluster.", Code(before))
		} else {
			f = finding(ch, "", fmt.Sprintf("The apply will fail: it would move %s from Kubernetes %s to %s, and EKS upgrades the control plane one minor version at a time.", Code(ch.Address), before, after))
			f.Remediation = fmt.Sprintf("Upgrade to %s first and apply, then carry on one version at a time, upgrading the node groups in between.", Code(fmt.Sprintf("1.%d", b+1)))
		}
		f.Short = "will fail on an EKS version jump"
		f.Fails = "update"
		f.Paths = []string{"version"}
		out = append(out, f)
	}
	return out
}

func checkNameTaken(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Replace) {
		n, ok := kb.NameOf(ch.Type)
		if !Managed(ch) || !ok {
			continue
		}
		before, after := c.Before(ch), c.After(ch)
		name, ok := before[n.Attr].(string)
		if !ok || name == "" || c.Unknown(ch, n.Attr) || after[n.Attr] != name {
			continue
		}
		same := true
		for _, w := range n.Within {
			if c.Unknown(ch, w) || !diff.Equal(before[w], after[w]) {
				same = false
			}
		}
		if !same {
			continue
		}
		noun := c.Info(ch).Noun
		var f *model.Finding
		switch {
		case ch.Order == "create-first":
			f = finding(ch, "", fmt.Sprintf("The apply will fail when it creates the replacement for %s: create_before_destroy makes the new %s first, "+
				"while the old one still has the name %s, and %s names must be unique %s.", Code(ch.Address), noun, Code(name), noun, n.Scope))
			f.Remediation = "Drop create_before_destroy if a gap is acceptable, or let the name change with each replacement."
			if n.Prefix != "" {
				f.Remediation = fmt.Sprintf("Set %s instead of %s so each replacement gets a fresh name, or drop create_before_destroy if a gap is acceptable.", n.Prefix, n.Attr)
			}
		case n.Reserved != "":
			if days, ok := before["recovery_window_in_days"]; ok && days != nil && atoi(days) == 0 {
				continue
			}
			f = finding(ch, "", fmt.Sprintf("The apply will fail when it recreates %s: the old %s is destroyed first, but its name %s stays taken %s.",
				Code(ch.Address), noun, Code(name), n.Reserved))
			f.Remediation = "Give the new " + noun + " a different name."
			if ch.Type == "aws_secretsmanager_secret" {
				f.Remediation = "Set recovery_window_in_days = 0 and apply that on its own first, so the old secret is deleted at once, or give the new secret a different name."
			}
		default:
			continue
		}
		f.Short = "will fail on a name that is taken"
		f.Fails = "create"
		f.Paths = []string{n.Attr}
		out = append(out, f)
	}
	return out
}

// engines that keep a version in engine_version, and what they call the
// service in a sentence.
var engineServices = map[string]string{
	"aws_db_instance": "RDS", "aws_rds_cluster": "RDS",
	"aws_elasticache_replication_group": "ElastiCache", "aws_elasticache_cluster": "ElastiCache",
	"aws_opensearch_domain": "OpenSearch Service", "aws_elasticsearch_domain": "OpenSearch Service",
}

var versionNumber = regexp.MustCompile(`\d+`)

// engineVersions returns the old and new engine version of an in-place
// update, when both are known.
func engineVersions(c *Context, ch *model.Change) (before, after string, ok bool) {
	if engineServices[ch.Type] == "" || c.Unknown(ch, "engine_version") {
		return "", "", false
	}
	before, ok1 := c.Before(ch)["engine_version"].(string)
	after, ok2 := c.After(ch)["engine_version"].(string)
	return before, after, ok1 && ok2 && before != "" && after != "" && before != after
}

// compareVersions orders versions such as 14.10, 8.0.mysql_aurora.3.04.0
// and OpenSearch_2.11 by their numbers. ok is false when the words between
// the numbers differ, as they do between engines.
func compareVersions(a, b string) (cmp int, ok bool) {
	if versionNumber.ReplaceAllString(a, "") != versionNumber.ReplaceAllString(b, "") {
		return 0, false
	}
	na, nb := versionNumber.FindAllString(a, -1), versionNumber.FindAllString(b, -1)
	for i := 0; i < len(na) && i < len(nb); i++ {
		x, _ := strconv.Atoi(na[i])
		y, _ := strconv.Atoi(nb[i])
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	return len(na) - len(nb), true
}

// engineChangeFails reports whether an update's engine version change is
// one the service refuses, so it never takes effect.
func engineChangeFails(c *Context, ch *model.Change) bool {
	return downgrades(c, ch) || majorNotAllowed(c, ch)
}

func downgrades(c *Context, ch *model.Change) bool {
	before, after, ok := engineVersions(c, ch)
	if !ok {
		return false
	}
	cmp, ok := compareVersions(after, before)
	return ok && cmp < 0
}

func majorNotAllowed(c *Context, ch *model.Change) bool {
	if ch.Type != "aws_db_instance" && ch.Type != "aws_rds_cluster" {
		return false
	}
	before, after, ok := engineVersions(c, ch)
	if !ok {
		return false
	}
	mb, ma := majorVersion(c.Before(ch), before), majorVersion(c.After(ch), after)
	if cmp, ok := compareVersions(after, before); mb == "" || ma == "" || mb == ma || !ok || cmp <= 0 {
		return false
	}
	allowed, _ := c.After(ch)["allow_major_version_upgrade"].(bool)
	return !allowed
}

func checkEngineDowngrade(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Update) {
		if !downgrades(c, ch) {
			continue
		}
		before, after, _ := engineVersions(c, ch)
		service := engineServices[ch.Type]
		msg := fmt.Sprintf("The apply will fail: it would move %s from engine version %s down to %s, and %s cannot downgrade.",
			Code(ch.Address), Code(before), Code(after), service)
		if auto, ok := c.Before(ch)["auto_minor_version_upgrade"].(bool); ok && auto {
			msg += " " + service + " most likely upgraded it itself, because auto_minor_version_upgrade is on, and the configuration still asks for the old version."
		}
		f := finding(ch, "", msg)
		f.Short = "will fail on an engine downgrade"
		f.Fails = "update"
		f.Paths = []string{"engine_version"}
		f.Remediation = fmt.Sprintf("Set engine_version to %s.", Code(before))
		if major := majorVersion(c.Before(ch), before); major != "" && major != before && ch.Type != "aws_opensearch_domain" {
			f.Remediation = fmt.Sprintf("Set engine_version to %s, or to just %s so automatic minor upgrades stop showing up as changes.", Code(before), Code(major))
		}
		out = append(out, f)
	}
	return out
}

// majorVersion is the part of an RDS engine version that names the major
// version: 14 for PostgreSQL 14.10, 8.0 for MySQL 8.0.35. It is empty for
// engines it does not know.
func majorVersion(obj map[string]any, version string) string {
	engine, _ := obj["engine"].(string)
	nums := versionNumber.FindAllString(version, -1)
	switch {
	case len(nums) == 0:
		return ""
	case strings.Contains(engine, "postgres"):
		if n, _ := strconv.Atoi(nums[0]); n >= 10 || len(nums) < 2 {
			return nums[0]
		}
		return nums[0] + "." + nums[1]
	case strings.Contains(engine, "mysql"), strings.Contains(engine, "mariadb"):
		if len(nums) < 2 {
			return ""
		}
		return nums[0] + "." + nums[1]
	}
	return ""
}

func checkMajorUpgrade(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Update) {
		if !majorNotAllowed(c, ch) {
			continue
		}
		before, after, _ := engineVersions(c, ch)
		ma := majorVersion(c.After(ch), after)
		f := finding(ch, "", fmt.Sprintf("The apply will fail: %s upgrades its engine from %s to %s, a new major version, and RDS refuses that unless allow_major_version_upgrade is true.",
			Code(ch.Address), Code(before), Code(after)))
		f.Short = "will fail on a major upgrade that is not allowed"
		f.Fails = "update"
		f.Paths = []string{"engine_version", "allow_major_version_upgrade"}
		f.Remediation = "Set allow_major_version_upgrade = true, and take a snapshot first: a major upgrade cannot be rolled back."
		if pg, _ := c.After(ch)["parameter_group_name"].(string); pg != "" && !strings.HasPrefix(pg, "default.") {
			f.Remediation += fmt.Sprintf(" The parameter group %s must also be one made for %s.", Code(pg), Code(ma))
		}
		out = append(out, f)
	}
	return out
}
