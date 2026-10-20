package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	htmltext "html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/delta"
	"github.com/Jeecabs/interragate/internal/live"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
	"github.com/Jeecabs/interragate/internal/render/html"
	"github.com/Jeecabs/interragate/internal/render/text"
	"github.com/Jeecabs/interragate/internal/source"
	"github.com/Jeecabs/interragate/internal/tfexec"
)

// watchCmd plans, serves the report to a browser on this machine, and
// plans again whenever the configuration changes, until interrupted. The
// page follows along and shows what each edit changed; so does the
// terminal.
func watchCmd(ctx context.Context, o *options, stdout, stderr io.Writer) error {
	if len(o.args) > 0 {
		return fmt.Errorf("interragate watch takes no arguments; to watch another directory use interragate watch -C %s", o.args[0])
	}
	dir := o.dir
	if dir == "" {
		dir = "."
	}
	if !hasConfigFiles(dir) {
		return noConfigError(dir)
	}
	runner, err := newRunner(ctx, o, dir, stderr)
	if err != nil {
		return err
	}
	srv, err := live.Start("127.0.0.1:0", displayDir(dir))
	if err != nil {
		return err
	}
	defer srv.Close()
	fmt.Fprintf(stderr, "Watching %s. The report is at %s\nIt plans again whenever you save a change here; Ctrl-C stops.\n\n", displayDir(dir), srv.URL())
	if !o.noOpen {
		done, err := openLive(srv.URL())
		defer done()
		if err != nil {
			fmt.Fprintf(stderr, "Could not open a browser (%v); open the address above yourself.\n\n", err)
		}
	}

	color := !o.noColor && os.Getenv("NO_COLOR") == "" && isTerminal(stdout)
	topts := text.Options{Color: color, Width: termWidth(stdout), All: o.all, Diffs: o.diffs}
	w := newWatcher(dir, o)
	pl := &planner{o: o, runner: runner, dir: dir, stderr: stderr, progress: isTerminal(stderr) && !o.verbose}
	var prev *model.Report
	var changed []string
	for {
		srv.Planning(changed)
		start := time.Now()
		r, p, err := planTemp(ctx, pl)
		if ctx.Err() != nil {
			return nil
		}
		took := time.Since(start)
		lead := time.Now().Format("15:04:05") + "  "
		if len(changed) > 0 {
			lead += fileList(changed) + " changed. "
		}
		if err != nil {
			// The page has its own heading for this, so it gets Terraform's
			// words alone.
			msg := err.Error()
			var ce *tfexec.CommandError
			if errors.As(err, &ce) && ce.Output != "" {
				msg = ce.Output
				if h := ce.Hint(); h != "" {
					msg = strings.TrimRight(msg, "\n") + "\n\n" + h
				}
			}
			srv.Failed(msg)
			fmt.Fprintf(stderr, "%sThe plan failed; the report keeps the last one that worked.\n%s\n\n", lead, indent(msg, "  "))
		} else {
			var d *delta.Delta
			if prev != nil {
				d = delta.Compare(prev, r)
			}
			var page bytes.Buffer
			if err := html.RenderWith(&page, r, html.Options{Live: &html.Live{
				Version: srv.Next(),
				Dir:     displayDir(dir),
				Changed: changed,
				Seconds: took.Seconds(),
				Delta:   d,
			}}); err != nil {
				return err
			}
			srv.Publish(page.Bytes(), html.Archived(page.Bytes()), live.Entry{Risk: r.Risk.Level.String(), Headline: r.Risk.Headline, Changed: changed})
			if d == nil {
				if err := text.Render(stdout, r, topts); err != nil {
					return err
				}
				fmt.Fprintln(stdout)
			} else {
				lead += "Planned in " + took.Round(100*time.Millisecond).String() + "."
				if err := text.RenderDelta(stdout, r, d, lead, topts); err != nil {
					return err
				}
				fmt.Fprintln(stdout)
			}
			if title, body, ok := riseWorthANotice(prev, r, d); ok && !o.noNotify {
				notify(title, body)
			}
			keepReport(dir, r)
			prev = r
			w.follow(p)
		}
		if changed, err = w.wait(ctx); err != nil {
			return nil
		}
	}
}

// planTemp plans into a temporary file and removes it afterwards: watch
// only needs the report.
func planTemp(ctx context.Context, pl *planner) (*model.Report, *plan.Plan, error) {
	f, err := os.CreateTemp("", "interragate-*.tfplan")
	if err != nil {
		return nil, nil, err
	}
	f.Close()
	defer os.Remove(f.Name())
	return pl.plan(ctx, f.Name())
}

func termWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if wd, _, err := term.GetSize(int(f.Fd())); err == nil && wd > 40 {
			return min(wd, 120)
		}
	}
	return 100
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+prefix)
}

func fileList(files []string) string {
	switch len(files) {
	case 0:
		return ""
	case 1:
		return files[0]
	case 2:
		return files[0] + " and " + files[1]
	}
	return fmt.Sprintf("%s and %d more files", files[0], len(files)-1)
}

// openLive opens the live report through a file that redirects to it. The
// address carries the server's secret token, and an opener's command line
// can be read by every user of the machine (with ps); the file can only be
// read by this user. done removes it, when watch stops.
func openLive(url string) (done func(), err error) {
	f, err := os.CreateTemp("", "interragate-watch-*.html") // mode 0600
	if err != nil {
		return func() {}, err
	}
	done = func() { os.Remove(f.Name()) }
	u := htmltext.EscapeString(url)
	_, err = fmt.Fprintf(f, `<!doctype html>
<meta charset="utf-8">
<meta http-equiv="refresh" content="0; url=%s">
<title>interragate watch</title>
<p>Opening the live report: <a href="%s">%s</a></p>
`, u, u, u)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return done, err
	}
	return done, openBrowser(f.Name())
}

// watcher notices when the files that make up a configuration change. It
// polls sizes and modification times: simple, portable, and cheap for the
// few dozen files a configuration has.
type watcher struct {
	root  string
	dirs  []string // the root module's directory and local modules'
	files []string // other inputs: settings and -var-file arguments
	last  map[string]stamp
	// Poll is how often to look; Quiet is how long changes must stop
	// before planning, so an editor's save (often several writes) starts
	// one plan.
	poll, quiet time.Duration
}

type stamp struct {
	size int64
	mod  time.Time
}

func newWatcher(dir string, o *options) *watcher {
	w := &watcher{root: dir, dirs: []string{dir}, poll: 250 * time.Millisecond, quiet: 350 * time.Millisecond}
	if path := o.config; path != "" {
		w.files = append(w.files, path)
	} else if path, ok := config.Find(dir); ok {
		w.files = append(w.files, path)
	}
	varFile := func(v string) {
		// Terraform reads a relative path from the configuration's directory.
		if !filepath.IsAbs(v) {
			v = filepath.Join(dir, v)
		}
		w.files = append(w.files, v)
	}
	for i, a := range o.extra {
		if v, ok := strings.CutPrefix(a, "-var-file="); ok {
			varFile(v)
		} else if a == "-var-file" && i+1 < len(o.extra) {
			varFile(o.extra[i+1])
		}
	}
	w.last = w.scan()
	return w
}

// follow adds the local modules a plan's configuration calls, so editing
// one plans again too. Modules init downloaded are not watched.
func (w *watcher) follow(p *plan.Plan) {
	if p == nil {
		return
	}
	seen := map[string]bool{}
	for _, d := range w.dirs {
		seen[d] = true
	}
	for _, rel := range source.ModuleDirs(w.root, p.Configuration) {
		if rel == "." || strings.HasPrefix(rel, ".terraform") {
			continue
		}
		d := filepath.Join(w.root, rel)
		if !seen[d] {
			seen[d] = true
			w.dirs = append(w.dirs, d)
		}
	}
	w.last = mergeStamps(w.last, w.scan())
}

func mergeStamps(old, cur map[string]stamp) map[string]stamp {
	for k, v := range cur {
		if _, ok := old[k]; !ok {
			old[k] = v
		}
	}
	return old
}

// ignored matches the names Terraform itself skips: hidden files and
// editor leftovers such as .main.tf.swp, #main.tf# and main.tf~.
func ignored(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "#") || strings.HasSuffix(name, "#") || strings.HasSuffix(name, "~")
}

func (w *watcher) scan() map[string]stamp {
	out := map[string]stamp{}
	add := func(path string) {
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			out[path] = stamp{fi.Size(), fi.ModTime()}
		}
	}
	for _, d := range w.dirs {
		for _, pattern := range []string{"*.tf", "*.tofu", "*.tf.json", "*.tofu.json", "*.tfvars", "*.tfvars.json"} {
			matches, _ := filepath.Glob(filepath.Join(d, pattern))
			for _, m := range matches {
				if !ignored(filepath.Base(m)) {
					add(m)
				}
			}
		}
	}
	for _, f := range w.files {
		add(f)
	}
	return out
}

// wait blocks until files change and then stay unchanged for a moment,
// and returns them relative to the root, or returns ctx's error.
func (w *watcher) wait(ctx context.Context) ([]string, error) {
	tick := time.NewTicker(w.poll)
	defer tick.Stop()
	changed := map[string]bool{}
	var lastChange time.Time
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
		cur := w.scan()
		for path, st := range cur {
			if old, ok := w.last[path]; !ok || old != st {
				changed[path] = true
				lastChange = time.Now()
			}
		}
		for path := range w.last {
			if _, ok := cur[path]; !ok {
				changed[path] = true
				lastChange = time.Now()
			}
		}
		w.last = cur
		if len(changed) > 0 && time.Since(lastChange) >= w.quiet {
			var out []string
			for path := range changed {
				// A file outside the directory, such as a var file given
				// by its full path, keeps that path.
				if rel, err := filepath.Rel(w.root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					path = rel
				}
				out = append(out, filepath.ToSlash(path))
			}
			sort.Strings(out)
			return out, nil
		}
	}
}
