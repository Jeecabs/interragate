// Package html renders a report as one self-contained HTML file: no network
// requests and no external assets.
package html

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io"
	"strings"

	"github.com/Jeecabs/interragate"
	"github.com/Jeecabs/interragate/internal/delta"
	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/render/markdown"
)

var (
	//go:embed assets/index.html
	indexHTML string
	//go:embed assets/app.css
	appCSS string
	//go:embed assets/app.js
	appJS string
	//go:embed assets/fonts/geist.woff2
	geistSans []byte
	//go:embed assets/fonts/geist-mono.woff2
	geistMono []byte
	//go:embed assets/fonts/geist-pixel.woff2
	geistPixel []byte
)

var page = template.Must(template.New("index").Parse(indexHTML))

// Live describes the interragate watch session a page is served in. The page
// listens for newer reports and shows what changed since the last one.
type Live struct {
	// Version numbers the reports in the session, from 1.
	Version int `json:"version"`
	// Dir is the directory being watched, for display.
	Dir string `json:"dir"`
	// Changed lists the files whose change led to this plan.
	Changed []string `json:"changed,omitempty"`
	// Seconds is how long the plan took.
	Seconds float64 `json:"seconds"`
	// Delta is what changed since the previous report, if there was one.
	Delta *delta.Delta `json:"delta,omitempty"`
}

// liveOpen starts the block a live page keeps its Live in.
var liveOpen = []byte(`<script id="interragate-live" type="application/json">{`)

// Archived is a live page as interragate watch serves it once a newer report
// exists: the same report, marked as an earlier one, so it neither reloads
// for the newer report nor calls itself current.
func Archived(page []byte) []byte {
	i := bytes.Index(page, liveOpen)
	if i < 0 {
		return page
	}
	at := i + len(liveOpen)
	out := make([]byte, 0, len(page)+20)
	out = append(out, page[:at]...)
	out = append(out, `"historic":true,`...)
	return append(out, page[at:]...)
}

// Options adjust the page.
type Options struct {
	Live *Live
}

// Render writes the report page.
func Render(w io.Writer, r *model.Report) error { return RenderWith(w, r, Options{}) }

// RenderWith writes the report page with options.
func RenderWith(w io.Writer, r *model.Report, opts Options) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var live template.JS
	if opts.Live != nil {
		b, err := json.Marshal(opts.Live)
		if err != nil {
			return err
		}
		live = template.JS(b)
	}
	// The pull request comment interragate would post, so the page can copy
	// it.
	var md bytes.Buffer
	if err := markdown.Render(&md, r, markdown.Options{}); err != nil {
		return err
	}
	comment, err := json.Marshal(md.String())
	if err != nil {
		return err
	}
	title := "interragate"
	if r.Plan.Dir != "" {
		title = r.Plan.Dir + " · interragate"
	}
	var buf bytes.Buffer
	err = page.Execute(&buf, map[string]any{
		"Title":    title,
		"CSS":      template.CSS(appCSS),
		"Licences": interragate.LicenceNotices(),
		// json.Marshal escapes <, > and &, so the data cannot close the
		// script element it sits in.
		"Data":     template.JS(data),
		"Markdown": template.JS(comment),
		"Live":     live,
		"JS":       template.JS(strings.ReplaceAll(appJS, "</script", "<\\/script")),
		// Base64 is safe in a CSS url() and keeps the report offline.
		"GeistSans":  template.CSS(base64.StdEncoding.EncodeToString(geistSans)),
		"GeistMono":  template.CSS(base64.StdEncoding.EncodeToString(geistMono)),
		"GeistPixel": template.CSS(base64.StdEncoding.EncodeToString(geistPixel)),
	})
	if err != nil {
		return err
	}
	_, err = w.Write(buf.Bytes())
	return err
}
