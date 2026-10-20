# The JSON report

`interragate show PLAN -f json` (or `--json FILE` on any command) writes the report that every renderer uses. `schema` is bumped on incompatible changes, so check it before reading anything else.

```jsonc
{
  "schema": 1,
  "generator": "interragate v0.1.0",
  "generated_at": "2026-10-19T07:00:00Z",
  "plan": {
    "tool": "terraform",          // or "opentofu"
    "version": "1.16.6",
    "format_version": "1.2",
    "source": "tfplan",
    "dir": "infra",
    "destroy": false,             // a terraform plan -destroy
    "source_read": true,          // .tf files were read for locations, locals, lifecycle
    "workspace": "prod",          // when interragate ran the plan itself; omitted otherwise
    "providers": { "aws": "registry.terraform.io/hashicorp/aws" }  // short names to source addresses
  },
  "risk": { "level": "critical", "headline": "Destroys an S3 bucket, ...", "counts": { "critical": 3, "high": 6 } },
  "summary": { "create": 3, "update": 23, "replace": 5, "delete": 5, "move": 0, "import": 0,
               "forget": 0, "read": 0, "no_op": 3, "cosmetic": 13, "indirect": 5, "drifted": 2, "outputs": 2 },
  "narrative": [ { "severity": "critical", "text": "Destroying `aws_s3_bucket.logs` ...", "refs": ["aws_s3_bucket.logs"] } ],
  "changes": [ ... ],
  "findings": [ ... ],
  "rule_docs": { "stateful-destroy": "Flags destroying databases, buckets, ..." },  // what each rule with a finding looks for, and why
  "suppressed": [ ... ],          // findings matched by an ignore entry, with "reason"
  "drift": [ ... ],
  "patterns": [ ... ],
  "outputs": [ ... ],
  "checks": [ ... ],
  "graph": { "nodes": [ ... ], "edges": [ ["from", "to"], ... ] },
  "apply": { "steps": [ ... ], "joins": [ ... ], "duration": 930, "critical": [ ... ], "gaps": [ ... ], "parallelism": 10, "narrative": [ ... ] },
  "git": { "base": "origin/main", "commit": "3f2c1ab", "branch": "add-cache",   // only in a git repository
           "files": ["main.tf", "modules/cache/main.tf"], "dirty": true,
           "providers": [ { "source": "registry.terraform.io/hashicorp/aws", "from": "5.62.0", "to": "6.0.0" } ],
           "codeowners": ".github/CODEOWNERS" },  // when the repository has one
  "scans": [ { "tool": "trivy", "issues": 24, "shown": 3 } ],  // only with --scan
  "consumers": [ ... ]            // only when .interragate.yaml declares services outside Terraform
}
```

Severities are `none`, `info`, `low`, `medium`, `high` and `critical`. Text fields use backticks around addresses and attribute names, the way Markdown does.

## changes[]

One per resource instance in the plan.

| Field | |
|---|---|
| `address`, `resource` | Instance address, and its configuration address without instance keys (the graph node) |
| `action` | `create`, `update`, `replace`, `delete`, `read`, `forget` or `no-op` |
| `order` | For replacements: `delete-first` or `create-first` |
| `moved`, `prev_address` | Moved from another address |
| `importing`, `import_id` | Being imported |
| `reason` | Terraform's `action_reason`, e.g. `replace_because_tainted` |
| `why`, `summary` | A sentence explaining the action, and a few words for lists |
| `forced_by` | Attribute paths that force replacement |
| `noun`, `category`, `stateful` | What the resource is, its kind (network, database, ...) and whether destroying it loses data |
| `severity` | The highest finding on it, or a floor for destroys and replaces |
| `cosmetic` | `tags` or `formatting` when that is all that changes |
| `indirect`, `causes[]` | It only changes because of upstream changes; each cause names the attribute, the upstream resource and action, and the attribute it reads (`via`) |
| `changed`, `unknown` | Counts of changed attributes and attributes known only after apply |
| `diff` | The structural diff (below) |
| `findings` | IDs of findings about it |
| `location` | `file`, `line`, `end_line`, when the source was read |
| `edit` | How its configuration differs from the git base (below), when it does |
| `origin` | With a git base and edits to configuration files: `edit` when it has an `edit`, `follows` when it only changes because of such a change (a data source read during the apply follows from what it waits for), empty otherwise. A destroy plan has neither: `-destroy` explains every change. |
| `deploys` | New versions the change ships, as `{what, from, to, unknown}`: an image's repository with its old and new tag or digest (`api`, `1.3.2`, `1.4.0`), a container's name with whole image references when the repository changes, `chart NAME` with chart versions, or `code` for a function's new code |
| `owners` | With a git repository that has a CODEOWNERS file: the owners of the file its block is in (or was removed from) |
| `access` | For an IAM policy, trust policy or firewall: what the change allows that was not allowed before (`op` `+`) and what it stops allowing (`-`), as `{op, kind, text, wide}`. `kind` is `iam` or `network`; `text` is a sentence such as "Inbound TCP 22 (SSH) from \`0.0.0.0/0\` (anywhere)"; `wide` marks letting in anyone, every action or the whole internet. Statements that are only reordered or split allow nothing new and give no line. |
| `drifted` | It was changed outside Terraform in a way that matters |
| `dependents` | How many resources depend on it, for destroys and replaces |

## diff

A tree of nodes: `{ key, kind, type, before, after, unknown, sensitive, forces, encoding, trivial, lines, children, hidden }`.

- `kind` is `added`, `removed`, `changed` or `unchanged`. Leaves carry `before` and `after`; composites carry `children`.
- `unknown`: the new value is known only after apply.
- `sensitive`: the value is masked. Neither side appears anywhere in the report.
- `forces`: this attribute forces replacement.
- `encoding: "json"`: a string attribute holding JSON, diffed as structure.
- `lines`: a line diff of a multi-line string, `{ op: " " | "-" | "+" | "skip", text, skip }`.
- `hidden`: how many unchanged children were left out.
- `trivial`: null versus empty, or JSON that only changed formatting.

## git and edit

When the Terraform directory is in a git repository, interragate compares its configuration (and the local modules it calls from elsewhere in the repository) with a base commit, working tree and untracked `.tf` files included. `git` says what was compared:

- `base`: as you would name it. By default it is the default branch (`origin/main`, say), meaning the commit where the current branch left it, so a branch's own commits count as well as uncommitted work; it is `HEAD` when that is the current commit, as on the default branch itself. `--base REF` compares with where the current branch left `REF` (for a tag or a commit such as `HEAD~2`, that commit), and `--base none` leaves `git` and every `edit` out.
- `commit`: the commit compared with, abbreviated. `branch`: the current branch, absent when HEAD is detached.
- `files`: the configuration files that differ, relative to the Terraform directory. `dirty`: uncommitted changes are included.

A change's `edit` is `{ file, kind, module, input, lines, omitted }`:

- `kind` is `edited` (lines of its block changed), `added` (the whole block is new), `removed` (the block was deleted; for a block renamed without a `moved` block, the lines show the rename) `module` (its own block is unchanged, but the `module` call it sits under, named in `module`, was edited or removed, so it follows from the module's inputs), `input` (it reads a variable or local, named in `input` as `var.azs` or `local.name`, whose block, definition or value in a `.tfvars` file was edited: through its arguments, `count` or `for_each`, other locals, or the arguments, `count` and `for_each` of the module calls above it; a `.tfvars` file counts, wherever it is, when the value it sets is the one the plan used, and a variable's default only when the plan used it) or `provider` (only its tags change, and its provider's block, named in `input` as `provider.aws`, was edited: `default_tags`; this comes before what else it reads).
- `file` is relative to the Terraform directory. Every instance of a block with `count` or `for_each` has the same edit.
- `lines` are the changed lines in order, `{ op: "-" | "+", line, text }`, with the comment, `moved` or `import` block just above the block when they changed with it. `line` is the line number in the file now for `+`, and in the base for `-` (absent when the block moved there from another file). Only the first 40 are kept; `omitted` counts the rest.
- Sensitive values are masked in `text`, as everywhere else in the report, and so are the lines that hold a secret, by what the configuration and the plan say of them: the lines of an argument the plan marks sensitive (a heredoc's included), every line that sets a variable declared sensitive (in its block or a `.tfvars` file), a module argument for one, and values written out for names that sound secret (`password`, `token`, ...). A masked line keeps the argument's name: `password = (sensitive value)`.

## scans[]

What each scanner interragate ran (`--scan`, or `scan:` in `.interragate.yaml`) reported: `{ tool, issues, shown, error }`. `issues` counts everything it reported in the configuration; `shown` counts those on resources the plan creates or replaces, or on lines an edit changed, which became findings under the rule named after the scanner (`trivy`, `checkov`, `tflint`). `error` says why it could not run.

## findings[]

`{ id, rule, severity, category, title, short, message, addresses, paths, remediation, snippet, fails, consumers }`. `snippet` is HCL you can paste, such as a `moved` or `removed` block. `fails` is set on findings that expect the apply to stop with an error: the operation on the first address that fails (`create`, `update` or `destroy`). `consumers` names the services outside Terraform a `consumer-*` finding is about. `interragate rules --format json` lists every rule. Findings from the policies in `.interragate.yaml` have the category `policy` and the policy's `id` as their `rule`.

## consumers[]

One per service outside Terraform that `.interragate.yaml` declares, in its order: `{ name, platform, owners, notify, link, checked, effects, hidden, missing }`. `platform` is written for people (`Vercel`, `iOS`, `partner`). `checked` is set when the plan has something the service is declared to use, so interragate could check it; `missing` lists what it is declared to use that the plan does not have. `hidden` counts what the plan does to the service that `disable` or `ignore` leaves out.

`effects` are what the plan does to what the service uses, worst first, and empty when it leaves the service alone: `{ kind, severity, text, update, finding, refs }`.

- `kind` is `loses` (what it uses is destroyed or removed), `value` (a value it holds changes, or is known only after apply), `reach` (a way in it uses closes) or `outage` (what it uses, or what serves a host it calls, is missing for a while during the apply).
- `text` says it from the service's side, with backticks around addresses.
- `update` is how a changed value reaches the service: `live`, `redeploy`, `rebuild`, `ota`, `store` or `manual`.
- `finding` is the ID of the finding that reports it, from `consumer-loses`, `consumer-value`, `consumer-reach` or `consumer-outage`. The apply's `narrative` says outages too.

A service is affected when it has effects. Without any, it is left out by `disable` or `ignore` when `hidden` is above zero, not affected when `checked` is set, and otherwise not checked at all.

## drift[]

`{ address, action, summary, reverted, kept, noise, diff }`. `noise` is true when the only differences are provider-side (re-attributed default tags, timestamps, null versus empty). `reverted` lists what this plan undoes; `kept` lists outside changes the plan leaves alone.

## graph

`nodes[]` holds one entry per configuration resource: `{ id, module, mode, type, name, noun, category, action, actions, instances, severity, location }`. `actions` counts its instances by action. `edges` are `[dependency, dependent]` pairs, resolved through module inputs and outputs (and locals when the source was read).

## apply

interragate's simulation of the apply: the order Terraform works through the changes in, roughly how long it takes, and when a replaced resource does not exist. It is left out when the plan failed or applies nothing. Times are seconds from the start of the apply.

| Field | |
|---|---|
| `steps` | One per operation, in the order they start: `{ id, address, op, deposed, after, start, duration, known, fails }`. `after` holds the IDs of steps and joins. `fails` is the ID of the finding that expects the step to fail |
| `joins` | `{ id, resource, op, after }`, sorted by ID: each stands for several steps of one operation on a resource's instances (below) |
| `duration` | Estimated seconds until the last step ends |
| `critical` | Step IDs on the chain of waits that ends last, in order. Joins on the chain are left out |
| `gaps` | `{ address, from, to, dependents }` for each replacement that destroys before it creates: from its destroy starting to its replacement being created, with the resources that read it and still exist meanwhile |
| `parallelism` | How many steps run at once: Terraform's default, 10 |
| `narrative` | Sentences like the top-level `narrative`: where the apply should fail and what that leaves undone, how long it should take, and the longest gap |

```jsonc
{
  "id": "aws_db_instance.orders:create",
  "address": "aws_db_instance.orders",   // as in changes[]
  "op": "create",                        // create, update, destroy or read
  "after": ["aws_db_subnet_group.main:update", "aws_kms_key.data:update", "aws_security_group.db:update"],
  "start": 325,
  "duration": 600,
  "known": true                          // from interragate's table of slow types
}
```

```jsonc
// joins[]: module.network.aws_vpc.this:update has this in its after
{
  "id": "module.network.aws_subnet.public[*]:destroy",
  "resource": "module.network.aws_subnet.public",   // as in changes[]
  "op": "destroy",
  "after": ["module.network.aws_subnet.public[1]:destroy", "module.network.aws_subnet.public[2]:destroy"]
}
```

- A replacement is a `destroy` step and a `create` step in the plan's order. With `create_before_destroy` the destroy comes last and is marked `deposed`. No-ops, moves, imports without other changes and forgets have no steps.
- The order follows Terraform's apply graph. A change waits for what it refers to, and for everything in a module it refers to as a whole (`module.network` rather than one of its outputs). Every step, destroys included, waits for what its provider's configuration refers to, since the provider is configured first: resources using a kubernetes provider wait for the EKS cluster it reads. Destroys follow the dependencies recorded in state, which still cover resources removed from the configuration: what depends on an object is destroyed before it, and an object is created or updated only after related objects are destroyed. A `create_before_destroy` destroy waits for those instead. Terraform forces `create_before_destroy` onto everything a resource with it depends on, so a removed object such a resource depends on, or a removed instance of one, is destroyed the same way: a removed subnet that a load balancer replaced create-first used goes after the old load balancer. Where destroying a provider's resources first would close a cycle, as when the resource its configuration reads is replaced, Terraform drops that wait and so does interragate.
- `after` lists only what a step waits for directly, not what that already waits for.
- An instance waits for every instance of a resource it refers to. Where a step waits for several steps of one operation on a resource, `after` names a join rather than each step, as Terraform's graph does, so the report grows with the number of instances rather than with their product. A join's `id` is the steps' address and operation with `[*]` where their addresses differ, such as `aws_subnet.private[*]:update` or `module.app[*].aws_instance.web:create` (`#2` follows when two joins would share one). Its `after` lists the steps it stands for or, across several instances of a module, one join per instance. A join takes no time and is done when everything in its `after` is. To follow the waits, as for what a failed step leaves undone, treat steps and joins alike: a step waits for everything reachable through `after`, and a join is never itself a step that runs or is skipped.
- When the configuration's `.tf` files were not read (`plan.source_read` is false), references through `local` values and `replace_triggered_by` are unknown, so a step that waits only through them may start too early.
- At most `parallelism` steps run at once, longest chain first. Terraform starts whichever is ready first, so treat the result as a sketch.
- Durations come from a table of typical times for slow types such as databases, clusters, CDNs and gateways; `known` marks those. Everything else, and every update that only changes tags or formatting, is taken to need a few seconds. `narrative` only gives times when some step is `known`.
- A gap's `dependents` are configuration resources that read it directly, looking through data sources. Resources that are new or are destroyed first themselves are left out, since they do not exist while it is missing.
- A plain destroy is not a gap: nothing comes back, so there is no window to bound.
- When a step is expected to fail, Terraform skips every step that waits for it and carries on with the rest. A step expected to fail that waits for another is skipped too, and fails only once that one is fixed. A replacement whose destroy runs but whose create is skipped, or fails, is left destroyed; one replaced create-first whose old object's destroy is skipped is left with both objects. The narrative names them, and counts what is skipped in resources.
