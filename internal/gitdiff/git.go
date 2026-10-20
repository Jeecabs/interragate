package gitdiff

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Jeecabs/interragate/internal/plan"
	"github.com/Jeecabs/interragate/internal/source"
)

// defaultBases are tried in order for the branch the work started from.
var defaultBases = []string{"origin/HEAD", "origin/main", "origin/master", "main", "master"}

// maxFileSize bounds what is read of one configuration file; anything
// larger is generated, not edited by hand.
const maxFileSize = 8 << 20

// timeout bounds the git commands for one comparison, so a slow file
// system cannot hold up the report.
const timeout = 20 * time.Second

// Load compares the configuration in dir with a git base. It returns nil
// when there is nothing to compare: base is "none", git is missing, dir is
// not in a repository, or the base cannot be resolved.
//
// An empty base means the merge base of HEAD with the default branch, so a
// branch sees its own commits as well as uncommitted work; on the default
// branch that is the uncommitted work alone. A named base is compared from
// where HEAD left it in the same way. The comparison covers dir and the
// local modules cfg calls from elsewhere in the repository.
func Load(dir, base string, cfg *plan.Configuration) *Diff {
	if dir == "" || base == "none" || strings.HasPrefix(base, "-") {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := run(ctx, abs, "rev-parse", "--show-toplevel", "--show-prefix")
	lines := strings.Split(string(out), "\n")
	if err != nil || len(lines) < 2 || lines[0] == "" {
		return nil
	}
	r := worktree{ctx: ctx, top: lines[0]}
	prefix := strings.TrimSuffix(lines[1], "/")
	if prefix == "" {
		prefix = "."
	}
	head, err := r.line("rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return nil
	}
	d := &Diff{Top: lines[0], Prefix: prefix}
	commit := ""
	if d.Base, commit = r.base(base, head); commit == "" {
		return nil
	}
	if d.Commit, err = r.line("rev-parse", "--short", commit); err != nil {
		d.Commit = commit
	}
	d.Branch, _ = r.line("symbolic-ref", "--short", "-q", "HEAD")

	specs := pathspecs(abs, prefix, cfg)
	if d.Files, err = r.changed(commit, specs); err != nil {
		return nil
	}
	d.Providers = r.providers(commit, prefix, abs)
	untracked := r.untracked(prefix, specs)
	d.Files = append(d.Files, untracked...)
	switch {
	case commit == head:
		d.Dirty = len(d.Files) > 0
	case len(untracked) > 0:
		d.Dirty = true
	default:
		out, err := r.git(append([]string{"diff", "--name-only", "-z", "HEAD", "--"}, specs...)...)
		for _, p := range strings.Split(string(out), "\x00") {
			d.Dirty = d.Dirty || err == nil && isConfig(p)
		}
	}
	for _, f := range d.Files {
		f.Path = rel(prefix, f.Path)
		if f.OldPath != "" {
			f.OldPath = rel(prefix, f.OldPath)
		}
	}
	sort.Slice(d.Files, func(i, j int) bool { return d.Files[i].Path < d.Files[j].Path })
	return d
}

// providers compares the dependency lock file with the base's. A lock file
// the base did not have (or one that is not committed) compares with
// nothing, rather than making every provider look new.
func (r worktree) providers(commit, prefix, abs string) []Provider {
	const name = ".terraform.lock.hcl"
	spec := name
	if prefix != "." {
		spec = prefix + "/" + name
	}
	old, err := r.git("show", commit+":"+spec)
	if err != nil {
		return nil
	}
	now, _ := readFile(filepath.Join(abs, name))
	before, after := source.LockedVersionsIn(old), source.LockedVersionsIn(now)
	var out []Provider
	for src, v := range after {
		if before[src] != v {
			out = append(out, Provider{Source: src, From: before[src], To: v})
		}
	}
	for src, v := range before {
		if _, ok := after[src]; !ok {
			out = append(out, Provider{Source: src, From: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// worktree runs git commands at the top of one work tree.
type worktree struct {
	ctx context.Context
	top string
}

func (r worktree) git(args ...string) ([]byte, error) { return run(r.ctx, r.top, args...) }

// line is the first line a command prints.
func (r worktree) line(args ...string) (string, error) {
	out, err := r.git(args...)
	if err != nil {
		return "", err
	}
	s, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(s), nil
}

// base resolves what to compare with: where HEAD left a ref the user named,
// or else the first default branch there is. It returns the name to show
// and the commit, or no commit when there is none.
func (r worktree) base(base, head string) (string, string) {
	if base != "" {
		commit, err := r.line("rev-parse", "--verify", "--quiet", base+"^{commit}")
		if err != nil {
			return "", ""
		}
		// A branch that has moved on since would count its new commits,
		// undone, as edits here. For a commit HEAD descends from, such as a
		// tag or HEAD~2, the merge base is that commit.
		if fork, err := r.line("merge-base", "HEAD", commit); err == nil {
			commit = fork
		}
		return base, commit
	}
	for _, ref := range defaultBases {
		commit, err := r.line("merge-base", "HEAD", ref)
		switch {
		case err != nil:
			continue
		case commit == head:
			return "HEAD", commit
		case ref == "origin/HEAD":
			// Name the branch it points at, as people do.
			if name, err := r.line("rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
				ref = name
			}
		}
		return ref, commit
	}
	return "", ""
}

// changed reads how the configuration files that git tracks differ from
// commit, working tree included, with their content where it can be read.
func (r worktree) changed(commit string, specs []string) ([]*File, error) {
	out, err := r.git(append([]string{"diff", "--unified=0", "--no-color", "--no-ext-diff", "--no-textconv",
		"--src-prefix=a/", "--dst-prefix=b/", "--find-renames", "--histogram", "--indent-heuristic",
		"--inter-hunk-context=0", commit, "--"}, specs...)...)
	if err != nil {
		return nil, err
	}
	var files []*File
	for _, f := range Parse(out) {
		if !isConfig(f.Path) {
			continue
		}
		if f.Deleted {
			f.Rebuild(nil)
		} else if data, ok := readFile(filepath.Join(r.top, filepath.FromSlash(f.Path))); ok {
			f.Rebuild(data)
		}
		files = append(files, f)
	}
	return files, nil
}

// untracked reads the configuration files git does not know about yet,
// which count as wholly added. Hidden directories hold caches, such as the
// .terraform directory where init puts the modules it downloads.
func (r worktree) untracked(prefix string, specs []string) []*File {
	out, err := r.git(append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--"}, specs...)...)
	if err != nil {
		return nil
	}
	var files []*File
	for _, p := range strings.Split(string(out), "\x00") {
		if !isConfig(p) || hidden(rel(prefix, p)) {
			continue
		}
		if data, ok := readFile(filepath.Join(r.top, filepath.FromSlash(p))); ok {
			files = append(files, Untracked(p, data))
		}
	}
	return files
}

// pathspecs are the directories to compare, relative to the top of the
// repository: the Terraform directory, and local modules outside it.
func pathspecs(abs, prefix string, cfg *plan.Configuration) []string {
	specs := []string{prefix}
	seen := map[string]bool{prefix: true}
	for _, dir := range source.ModuleDirs(abs, cfg) {
		dir = filepath.ToSlash(dir)
		if !strings.HasPrefix(dir, "../") {
			continue // inside the Terraform directory
		}
		p := path.Join(prefix, dir)
		if p == ".." || strings.HasPrefix(p, "../") || seen[p] {
			continue // outside the repository
		}
		seen[p] = true
		specs = append(specs, p)
	}
	return specs
}

// run runs git in dir and returns what it printed. Pathspecs are literal,
// so a directory named with * or [ ] means itself.
func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--literal-pathspecs", "-C", dir}, args...)...)
	// interragate only reads: it never prompts, and it does not refresh the
	// index, which would take a lock a git command run at the same time
	// might need.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	return stdout.Bytes(), err
}

func readFile(p string) ([]byte, bool) {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxFileSize {
		return nil, false
	}
	data, err := os.ReadFile(p)
	return data, err == nil
}

// isConfig matches the files Terraform and OpenTofu read configuration from.
func isConfig(p string) bool {
	for _, ext := range []string{".tf", ".tofu", ".tf.json", ".tofu.json", ".tfvars", ".tfvars.json"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

func hidden(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}

// rel makes a path relative to the top of the repository relative to dir,
// which is itself relative to the top.
func rel(dir, p string) string {
	if dir == "." {
		return p
	}
	r, err := filepath.Rel(filepath.FromSlash(dir), filepath.FromSlash(p))
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}
