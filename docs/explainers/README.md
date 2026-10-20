# Site and README images

From the repository root, run:

```sh
node docs/explainers/render.mjs
```

Requires Go, Node 24+ and `/usr/bin/chromium`; no npm dependencies. One command
regenerates every README figure, the eight report captures and `site/og.png`.
It also builds `_site/` and checks it.

`CLI_PATH` at the top of `render.mjs` is the CLI's source path; `site/build.go`
builds the same command for the site. The renderer captures the real AWS demo
as JSON and terminal text, and never edits the CLI's output, so the report
headers are exactly what the CLI prints. No cloud account
is used.

Everything is served on `127.0.0.1`, using an ephemeral port. Only headless
Chromium is launched. Its profile lives under `/tmp` (a worktree path can exceed
Chromium's socket path limit) and is removed afterwards. Generated sample data,
render evidence and verification screenshots are ignored by Git.

Each HTML source accepts `?theme=light` or `?theme=dark`. The ten PNGs are written
to `docs/images/explain-*.png`, at 1600 pixels wide for an 800 CSS pixel canvas.
Panels have a 16px radius and transparent outer corners. The renderer checks
fonts, the 13px minimum text size, overflow, image dimensions and page colours.

`landing.html` and `landing.css` draw the 1200 × 630 social card, `site/og.png`.
The report screenshots are captured from the built `demo/` at 1280 × 800 with
device scale 2, in light and dark; the social card is light. The landing page
draws everything else itself, from the sample, in `site/`.

`capture.mjs` shares the renderer's browser. It checks the built landing page at
1440 and 390 pixels wide in both themes, loads all images, follows every local
link and fragment, exercises every tab and copy button, checks keyboard tab
navigation and that each of the seven scenes has its steps, ends on its result
and stays inside its frame, and verifies the rules filter's matching and empty
states. Browser errors fail the render. Evidence goes to
`render-checks.json`; full-page captures go to `checks/`.

- `hero.html`: the verdict, one square per resource and the first four terminal
  “What matters” items in their original order.
- `meaning.html`: unwrapped Terraform excerpts followed by their consequences.
  Values come from the sample plan JSON; narrative comes from the real report.
- `branch.html`: changes grouped by origin, including cosmetic updates, with
  the database edit, following parameter update and unrelated firewall drift.
- `apply.html`: six rows showing the database failure, skipped dependent work
  and three replacements left destroyed. Start times and durations come from
  the report. Short operations have an 18px minimum width; dashed outlines are
  skipped, and hatching shows the report's “does not exist” intervals. Typical
  durations do not predict when AWS returns an error. The stranded list and
  skipped resource count are derived through dependencies, including joins.
- `pipeline.html`: plan JSON through diff, graph, analysis and rules to the
  five output formats, with `.tf` files and git feeding analysis.
