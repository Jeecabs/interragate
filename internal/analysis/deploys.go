package analysis

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
)

// deploys finds the changes that ship a new version of something that
// runs: a container image in a task definition, a Kubernetes workload, a
// Cloud Run service or a container app; a Helm chart; a function's code.
// In Terraform's words a deploy is often just "container_definitions forces
// replacement".
func (a *analyzer) deploys(r *model.Report) {
	for _, ch := range r.Changes {
		rc := a.raw[ch]
		if rc == nil || ch.Mode == "data" || (ch.Action != model.Update && ch.Action != model.Replace) {
			continue
		}
		before, _ := rc.Change.Before.(map[string]any)
		after, _ := rc.Change.After.(map[string]any)
		unknown := func(path ...any) bool { return diff.Flag(rc.Change.AfterUnknown, path...) }
		ch.Deploys = deploysOf(ch.Type, before, after, unknown)
	}
}

func deploysOf(typ string, before, after map[string]any, unknown func(path ...any) bool) []model.Deploy {
	switch {
	case typ == "aws_ecs_task_definition":
		if unknown("container_definitions") {
			return []model.Deploy{{What: "containers", Unknown: true}}
		}
		return imageDeploys(ecsImages(before), ecsImages(after))
	case strings.HasPrefix(typ, "kubernetes_deployment") || strings.HasPrefix(typ, "kubernetes_stateful_set") ||
		strings.HasPrefix(typ, "kubernetes_daemonset") || strings.HasPrefix(typ, "kubernetes_daemon_set") ||
		strings.HasPrefix(typ, "kubernetes_cron_job") || strings.HasPrefix(typ, "kubernetes_job"):
		return imageDeploys(nestedImages(before, "container", "init_container"), nestedImages(after, "container", "init_container"))
	case typ == "google_cloud_run_v2_service" || typ == "google_cloud_run_service" || typ == "google_cloud_run_v2_job":
		return imageDeploys(nestedImages(before, "containers"), nestedImages(after, "containers"))
	case typ == "azurerm_container_app":
		return imageDeploys(nestedImages(before, "container", "init_container"), nestedImages(after, "container", "init_container"))
	case typ == "helm_release":
		from, to := str(before["version"]), str(after["version"])
		if unknown("version") {
			return []model.Deploy{{What: "chart " + str(after["chart"]), From: from, Unknown: true}}
		}
		if from != to && to != "" {
			return []model.Deploy{{What: "chart " + str(after["chart"]), From: from, To: to}}
		}
	case typ == "aws_lambda_function":
		if b, a := str(before["image_uri"]), str(after["image_uri"]); a != b && (a != "" || unknown("image_uri")) {
			return imageDeploys(map[string]string{"function": b}, map[string]string{"function": a})
		}
		for _, k := range []string{"source_code_hash", "s3_key", "s3_object_version", "filename"} {
			if unknown(k) || !diff.Equal(before[k], after[k]) && after[k] != nil {
				return []model.Deploy{{What: "code"}}
			}
		}
	}
	return nil
}

// imageDeploys compares images by container name.
func imageDeploys(before, after map[string]string) []model.Deploy {
	names := make([]string, 0, len(after))
	for n := range after {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []model.Deploy
	for _, n := range names {
		from, to := before[n], after[n]
		if from == to || to == "" && from == "" {
			continue
		}
		if to == "" {
			out = append(out, model.Deploy{What: n, From: from, Unknown: true})
			continue
		}
		fr, ft := splitImage(from)
		tr, tt := splitImage(to)
		if from != "" && fr == tr {
			// The same repository: the tag (or digest) is the news.
			out = append(out, model.Deploy{What: shortRepo(tr), From: ft, To: tt})
			continue
		}
		out = append(out, model.Deploy{What: n, From: from, To: to})
	}
	return out
}

// ecsImages reads each container's image from a task definition's JSON.
func ecsImages(obj map[string]any) map[string]string {
	out := map[string]string{}
	s, _ := obj["container_definitions"].(string)
	var defs []map[string]any
	if json.Unmarshal([]byte(s), &defs) != nil {
		return out
	}
	for _, d := range defs {
		if name, img := str(d["name"]), str(d["image"]); name != "" && img != "" {
			out[name] = img
		}
	}
	return out
}

// nestedImages finds container blocks under the given keys at any depth,
// as Kubernetes, Cloud Run and container apps nest them.
func nestedImages(obj map[string]any, keys ...string) map[string]string {
	out := map[string]string{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				for _, want := range keys {
					if k != want {
						continue
					}
					for i, c := range asObjects(e) {
						name := str(c["name"])
						if name == "" {
							name = fmt.Sprintf("container %d", i+1)
						}
						if img := str(c["image"]); img != "" {
							out[name] = img
						}
					}
				}
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(obj)
	return out
}

func asObjects(v any) []map[string]any {
	var out []map[string]any
	list, _ := v.([]any)
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// splitImage splits an image reference into its repository and its tag or
// digest; a reference with neither means "latest".
func splitImage(ref string) (repo, version string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		digest := ref[i+1:]
		if len(digest) > 19 {
			digest = digest[:19] // sha256: and 12 hex digits
		}
		return ref[:i], digest
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

func shortRepo(repo string) string {
	return repo[strings.LastIndex(repo, "/")+1:]
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
