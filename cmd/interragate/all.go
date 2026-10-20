package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/render/html"
	"github.com/Jeecabs/interragate/internal/render/text"
	"github.com/Jeecabs/interragate/internal/tfexec"
)

// allCmd plans every Terraform root module below a directory, one after
// another, and lists them worst first: a line per root in the terminal,
// and a report per root with an index linking them.
func allCmd(ctx context.Context, o *options, stdout, stderr io.Writer) error {
	if len(o.args) > 1 {
		return fmt.Errorf("interragate all takes one directory at most")
	}
	top := o.dir
	if len(o.args) == 1 {
		top = o.args[0]
	}
	if top == "" {
		top = "."
	}
	dirs := rootDirs(top)
	if len(dirs) == 0 {
		return fmt.Errorf("no Terraform configuration below %s", displayDir(top))
	}
	threshold, err := failThreshold(o)
	if err != nil {
		return err
	}
	out, err := os.MkdirTemp("", "interragate-all-")
	if err != nil {
		return err
	}
	var pages []html.Root
	var lines []text.Root
	tool, failing, broken := "", 0, 0
	for i, d := range dirs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Fprintf(stderr, "%s (%d of %d)\n", d, i+1, len(dirs))
		each := *o
		each.dir = filepath.Join(top, d)
		r, err := planHere(ctx, &each, stderr)
		page := html.Root{Name: d}
		line := text.Root{Name: d}
		if err != nil {
			page.Error, line.Error = planError(err), planError(err)
			broken++
		} else {
			name := slugify(d) + ".html"
			if _, err := writeHTML(r, filepath.Join(out, name)); err != nil {
				return err
			}
			page.Href, page.Report, line.Report = name, r, r
			keepReport(each.dir, r)
			tool = r.Plan.ToolName() + " " + r.Plan.Version
			failing += countFailing(r, threshold)
		}
		pages = append(pages, page)
		lines = append(lines, line)
	}
	fmt.Fprintln(stderr)
	color := !o.noColor && os.Getenv("NO_COLOR") == "" && isTerminal(stdout)
	if err := text.RenderRoots(stdout, lines, text.Options{Color: color, Width: termWidth(stdout)}); err != nil {
		return err
	}
	index := filepath.Join(out, "index.html")
	f, err := os.Create(index)
	if err != nil {
		return err
	}
	facts := strings.Join(nonEmpty(displayDir(top), tool, "planned "+time.Now().Format("Jan 2, 2006, 3:04 PM")), " · ")
	if err := html.RenderRoots(f, displayDir(top), facts, pages); err != nil {
		f.Close()
		return err
	}
	f.Close()
	fmt.Fprintf(stderr, "\nReports for each root: %s\n", index)
	if o.open {
		if err := openBrowser(index); err != nil {
			fmt.Fprintf(stderr, "Could not open a browser: %v\n", err)
		}
	}
	// A root that did not plan was not checked, which is worse than one
	// with findings.
	if broken > 0 {
		return exitError(1)
	}
	if failing > 0 {
		return exitError(2)
	}
	return nil
}

func countFailing(r *model.Report, threshold model.Severity) int { return failing(r, threshold) }

// planError is what Terraform said when a plan failed, from its first
// error onwards, rather than the line interragate wraps it in.
func planError(err error) string {
	var ce *tfexec.CommandError
	if errors.As(err, &ce) && strings.TrimSpace(ce.Output) != "" {
		out := strings.TrimSpace(ce.Output)
		if i := strings.Index(out, "Error:"); i >= 0 {
			out = out[i:]
		}
		if h := ce.Hint(); h != "" {
			out += "\n" + h
		}
		return out
	}
	return err.Error()
}

// localSource matches a module call's source when it is a local path.
var localSource = regexp.MustCompile(`(?m)^\s*source\s*=\s*"(\.{1,2}/[^"]*)"`)

// rootDirs lists the directories below top, top included, that hold
// Terraform configuration and are not a module another of them calls by a
// local path: the roots a plan starts from.
func rootDirs(top string) []string {
	var found []string
	if hasConfigFiles(top) {
		found = append(found, ".")
	}
	found = append(found, configDirs(top, 4, 200)...)
	called := map[string]bool{}
	for _, d := range found {
		dir := filepath.Join(top, d)
		files, _ := filepath.Glob(filepath.Join(dir, "*.tf"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			for _, m := range localSource.FindAllSubmatch(b, -1) {
				abs, err := filepath.Abs(filepath.Join(dir, string(m[1])))
				if err == nil {
					called[abs] = true
				}
			}
		}
	}
	var roots []string
	for _, d := range found {
		abs, err := filepath.Abs(filepath.Join(top, d))
		if err == nil && !called[abs] {
			roots = append(roots, d)
		}
	}
	sort.Strings(roots)
	return roots
}

// slugify turns a directory into a file name: envs/prod becomes envs-prod.
func slugify(dir string) string {
	if dir == "." {
		return "root"
	}
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, dir)
	return strings.Trim(s, "-")
}

func nonEmpty(parts ...string) []string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
