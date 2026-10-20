# The JSON report

Any interragate command writes the report with `-f json` (or `--json FILE` next to another format). Check that `schema` is 1 before reading anything else.

## Contents

- Top level
- changes[]
- findings[]
- apply
- git and edit
- jq recipes

## Top level

- `risk`: `{level, headline, counts}`. Levels are `none`, `info`, `low`, `medium`, `high` and `critical`.
- `summary`: counts by action (`create`, `update`, `replace`, `delete`, `read`, `forget`, `move`, `import`, `no_op`), plus `cosmetic`, `indirect` (changes that only follow from others), `drifted` and `outputs`.
- `narrative`: the summary's "What matters", as `{severity, text, refs}`. Text puts backticks around addresses and attribute names.
- `plan.destroy` is true for a `terraform plan -destroy`.
- `changes`, `findings`, `apply` and `git` are below; `drift`, `outputs` and `suppressed` hold what their names say.

## changes[]

One per resource instance. The fields a review needs:

| Field | |
|---|---|
| `address`, `action` | `action` is `create`, `update`, `replace`, `delete`, `read`, `forget` or `no-op` |
| `why` | A sentence on why it changes, with Terraform's reason spelled out |
| `forced_by` | The attributes that force a replacement |
| `order` | For a replacement: `delete-first` or `create-first` |
| `stateful` | Destroying it loses data |
| `severity` | Its highest finding |
| `cosmetic` | `tags` or `formatting`, when that is all that changes |
| `indirect`, `causes` | It only changes because something upstream does; each cause names the upstream resource |
| `origin` | On a branch: `edit`, `follows`, or absent when no edit explains it |
| `edit` | The edit behind it (below) |
| `drifted` | It was changed outside Terraform in a way that matters |
| `access` | For IAM policies and firewalls: what it newly allows (`op` `+`) or stops allowing (`-`), as a sentence in `text` |
| `deploys` | Versions it ships, as `{what, from, to}` |
| `location` | `{file, line}` of its block |
| `findings` | IDs of the findings about it |

## findings[]

`{id, rule, severity, title, message, addresses, remediation, snippet, fails, consumers}`. `snippet` is HCL to paste, such as a `moved` block. `fails` is set when interragate expects the apply to stop at that finding. `consumers` names the services outside Terraform a `consumer-*` finding is about. `suppressed[]` holds the findings an `ignore` entry matched, with its `reason`.

## consumers[]

Only when `.interragate.yaml` declares services outside Terraform: `{name, platform, owners, notify, link, checked, effects, hidden, missing}` for each. `effects[]` are `{kind, severity, text, update, finding, refs}`, worst first: `kind` is `loses`, `value`, `reach` or `outage`, and `text` says it from the service's side. An empty `effects` means the plan leaves the service alone, unless `hidden` counts effects that `disable` or `ignore` left out. `checked` is false when the plan has nothing the service is declared to use, and `missing` lists those uses, often a typo or a renamed resource.

## apply

interragate's simulation of the apply, in Terraform's order. `narrative` gives the sentences: where the apply should fail, what that skips and leaves destroyed, and how long it takes. `steps[]` are `{id, address, op, start, duration, fails}` in the order they start. `duration` is the estimated total in seconds. `gaps[]` are replacements that destroy before they create, with the resources that read them meanwhile.

## git and edit

In a git repository, `git` says what the configuration was compared with: `base`, `branch`, and `files`, the configuration files that differ. A change's `edit` is `{file, kind, input, lines}`. `kind` is one of:

- `edited`, `added` or `removed`: its block
- `module`: the `module` call above it
- `input`: a variable or local it reads, named in `input`
- `provider`: only its tags change, from the provider's `default_tags`

## jq recipes

```bash
interragate show PLAN -f json > report.json

# The verdict
jq -r '"\(.risk.level): \(.risk.headline)"' report.json

# High and critical findings
jq -r '.findings[] | select(.severity == "critical" or .severity == "high")
  | "\(.severity) \(.rule) \(.addresses | join(", ")): \(.message)"' report.json

# What destroys data
jq -r '.changes[] | select(.stateful and (.action == "delete" or .action == "replace")) | .address' report.json

# Changes no edit on the branch explains
jq -r 'select(.git.files | length > 0) | .changes[]
  | select(.action != "no-op" and (.origin // "") == "") | .address' report.json

# How the apply unfolds
jq -r '.apply.narrative[]?.text' report.json
```
