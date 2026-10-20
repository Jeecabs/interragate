package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Jeecabs/interragate/internal/model"
)

// lastReportPath is where interragate keeps the last report it planned for a
// directory, so interragate open can show it without planning again: a plan
// of real infrastructure can take minutes. The report is redacted like every
// other, and only this user can read it.
func lastReportPath(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(cache, "interragate", "last", hex.EncodeToString(sum[:8])+".json.gz"), nil
}

// keepReport saves a directory's report for interragate open. Failing to is
// not worth stopping for.
func keepReport(dir string, r *model.Report) {
	path, err := lastReportPath(dir)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".report-*")
	if err != nil {
		return
	}
	zw := gzip.NewWriter(tmp)
	err = json.NewEncoder(zw).Encode(r)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return
	}
	os.Rename(tmp.Name(), path)
}

// openCmd opens the report of the last plan interragate made in a directory,
// without planning again.
func openCmd(o *options, stdout, stderr io.Writer) error {
	if len(o.args) > 1 {
		return errors.New("interragate open takes one directory at most")
	}
	dir := o.dir
	if len(o.args) == 1 {
		dir = o.args[0]
	}
	if dir == "" {
		dir = "."
	}
	path, err := lastReportPath(dir)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no plan of %s yet: run interragate there first, and interragate open shows its report without planning again", displayDir(dir))
	}
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	var r model.Report
	if err := json.NewDecoder(zr).Decode(&r); err != nil {
		return fmt.Errorf("the saved report for %s cannot be read (%v): run interragate there again", displayDir(dir), err)
	}
	out, err := writeHTML(&r, "")
	if err != nil {
		return err
	}
	if t, err := time.Parse(time.RFC3339, r.GeneratedAt); err == nil {
		fmt.Fprintf(stderr, "The plan of %s from %s.\n", displayDir(dir), ago(time.Since(t)))
	}
	if o.noOpen {
		fmt.Fprintln(stdout, out)
		return nil
	}
	if err := openBrowser(out); err != nil {
		fmt.Fprintf(stderr, "Report written to %s (could not open a browser: %v)\n", out, err)
	}
	return nil
}

// ago says how long ago something was, roughly: "4 minutes ago".
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < 2*time.Minute:
		return "a minute ago"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 2*time.Hour:
		return "an hour ago"
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
