# The landscape

What exists for understanding Terraform plans, surveyed in October 2026, and where interragate fits. Stars and activity are from GitHub at the time of writing.

## Plan and graph visualisers

| Tool | What it does | State |
|---|---|---|
| [Rover](https://github.com/im2nguyen/rover) | Browser view of a plan: overview, resource map, graph | ★3.3k; last release v0.3.3 in July 2022 |
| [Blast Radius](https://github.com/28mm/blast-radius) | d3 view of `terraform graph` | ★2.2k; last push August 2024 |
| [Pluralith](https://github.com/Pluralith/pluralith-cli) | State and plan diagrams, docs generation | ★860; last push July 2023 |
| [terraform-visual](https://github.com/hieven/terraform-visual) | Interactive plan view | ★674; dependencies refreshed in August 2026 after years idle |
| [inframap](https://github.com/cycloidio/inframap) | Provider-aware diagrams from state or HCL (not plans) | ★2.1k; active |
| [terravision](https://github.com/patrickchugh/terravision) | Architecture diagrams from code | ★1.7k; active, diagram-focused |
| [tf-blast-radius-modern](https://github.com/GuitaristForEver/tf-blast-radius-modern) | React rewrite of Blast Radius | ★1; February 2026 |

Graph visualisers draw every resource and every edge, which turns into a hairball at a few hundred resources. They show *what depends on what* but not *what this change means*.

## Terminal UIs

| Tool | What it does |
|---|---|
| [tfplantui](https://github.com/omarismail/terraform-plan-tui) | Navigable dependency-column TUI with expandable attribute diffs (★121, July 2026) |
| [terraform-ui / tfui](https://github.com/lmarqs/terraform-ui) | k9s-style TUI: plan, risk badges, blast radius, apply (★5, September 2026) |
| [tf](https://github.com/jdforsythe/tf) | Interactive wrapper over plan and apply output (★6) |

## Summaries and pull request comments

| Tool | What it does |
|---|---|
| [tf-summarize](https://github.com/dineshba/tf-summarize) | Tree and table summaries of a plan (★741) |
| [tfplan2md](https://github.com/oocx/tfplan2md) | Plan to Markdown for PRs; can embed scanner output (★174) |
| [tfcmt](https://github.com/suzuki-shunsuke/tfcmt) | Posts plan results as PR comments (★552) |
| prettyplan, scenery | Plan prettifiers; archived or idle since 2019–2021 |

## Risk analysis

| Tool | What it does |
|---|---|
| [readtheplan](https://github.com/readtheplan/readtheplan) | Python CLI: classifies changes into four risk tiers and maps them to compliance controls (October 2026) |
| [Overmind](https://github.com/overmindtech/cli) | Blast radius and risk from live cloud data; SaaS, needs an account and read access to your cloud |
| Harness IaCM blast radius agent | LLM-assisted 1–10 risk score in Harness pipelines; beta |
| Spacelift AI review | AI explanation of runs inside Spacelift |
| ThothCTL `check blast-radius` | ITIL-style risk report from a plan JSON |

## Linters, scanners and helpers

[tflint](https://github.com/terraform-linters/tflint), [checkov](https://github.com/bridgecrewio/checkov), Trivy (formerly tfsec), KICS and Terrascan check *configuration* against rules. Checkov can read plan JSON, but it reports the posture of every resource rather than what the change introduces. [tfautomv](https://github.com/busser/tfautomv) writes `moved` blocks by matching creates against deletes. [Infracost](https://github.com/infracost/infracost) adds cost.

## The gap

Each tool covers one slice: a picture, a summary, a risk tier or a policy check. None does all three things a reviewer needs at once:

1. **Explain** the plan in sentences: why each resource changes, which changes are only consequences of others, and what was changed by hand and is about to be undone.
2. **Flag what the change introduces**, including failure modes that only exist at plan time: the deletion-protection trap, `count` index shifts, renames without `moved`, imports that replace what they import, reverted hotfixes.
3. **Show it** as a map you can query (what does this depend on, what breaks if it goes), with diffs that decode JSON policies and fold provider noise.

It should also run locally on the `terraform` or `tofu` you already have, with no account, no cloud credentials and no LLM call.

That is interragate. The pieces that are new, as far as this survey found:

- Cause tracing: "only changes because X is replaced", followed through module inputs, outputs and locals.
- Drift classification: provider noise is separated from real outside changes, and interragate says whether the plan reverts each one.
- Plan-time failure rules: `protected-delete`, and `count-index-shift` with a generated `for_each` migration.
- Diff-aware security rules that ignore what was already there.
- Repeated changes collapsed into patterns, such as a default tag changing on 19 resources.
- A self-contained, offline HTML report with a lane-and-layer dependency map, blast-radius highlighting and structured, JSON-aware diffs.
