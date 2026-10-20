---
name: reviewing-terraform-plans
license: AGPL-3.0-only
description: Reviews Terraform and OpenTofu plans with the interragate CLI before they are applied. Explains what each change means, what destroys data, what only changes because something else does, where the apply will fail and which changes no edit on the branch explains, and flags risky changes with plan-aware rules. Use when asked to review, explain, summarize or check a terraform or tofu plan, a plan file or plan JSON, an infrastructure pull request, or whether an apply is safe. Also use for writing .interragate.yaml policies or adding interragate to CI.
compatibility: Needs the interragate CLI (installed with Go). Planning also needs terraform or tofu and the configuration's credentials; a saved plan JSON needs neither.
---

# Reviewing Terraform plans with interragate

interragate reads a plan and says what it means: it traces each change to its cause, simulates the apply in Terraform's order, and knows how cloud resources fail. Base the review on its output rather than on reading plan JSON yourself.

## Setup

Check that interragate is installed with `interragate version`. If it is not, install it with Go:

```bash
go install github.com/Jeecabs/interragate/cmd/interragate@latest
```

That puts it in `$(go env GOPATH)/bin`, which must be on `PATH`.

## Get the plan

Use the first row that applies:

| The user has | Run |
|---|---|
| Plan JSON (`terraform show -json` output) | `interragate show plan.json --no-color` |
| A plan file (`terraform plan -out tfplan`) | `interragate show tfplan --no-color` (needs terraform or tofu, and the directory it was planned in, initialised) |
| Neither | `interragate --no-color --save-plan tfplan` in the Terraform directory; plan arguments go after `--`, as in `interragate --no-color -- -var-file=prod.tfvars` |

Planning changes no infrastructure, but it uses the user's credentials, takes the state lock, and runs `init` when the directory needs it (`--no-init` stops that). `--save-plan` keeps the plan, so the user can apply exactly what you reviewed.

Add `-f json` to any of these for the full report as JSON. Its fields and jq recipes are in [references/json-report.md](references/json-report.md).

## Review

1. Read the summary. It leads with the verdict ("Critical risk. Destroys …"), then what matters, how the apply unfolds, every change and every finding.
2. For each high or critical finding, and anything destroyed or replaced that holds data, run `interragate explain PLAN ADDRESS --no-color`. It gives why the resource changes, what it follows from, the edit behind it, its findings with fixes, and its diff.
3. On a git branch, find what no edit explains. The summary says how much of the plan the edits account for ("… and 1 other change is not tied to an edit"), and the `untied-destroy` rule flags destroys among the rest. These are drift, someone else's change or a provider upgrade, and they are what a reviewer most needs to see.
4. To answer "is it safe to apply?", run `interragate check PLAN --fail-on high`. Exit code 0 means no finding at or above that severity, 2 means at least one, and 1 means interragate itself failed.

## Report

Lead with interragate's verdict, then only what needs a decision. Quote addresses exactly, and keep interragate's wording for findings and fixes. For the sample plan, a review reads:

```markdown
**Critical risk.** Destroys an S3 bucket, replaces an RDS database instance and opens SSH to the internet.

- **Critical**: replacing `aws_db_instance.orders` destroys it and all its data. Fix: revert the change to `kms_key_id` and `storage_encrypted`, or back the database up first.
- **High**: the apply will fail at `aws_db_instance.orders`, because deletion protection is still on. Fix: turn it off in a plan of its own first.
- …

**When you apply:** it should fail at `aws_db_instance.orders`. The 22 resources that wait for it are skipped, leaving three destroyed with nothing in their place.

**Not from your edits:** `aws_security_group.alb` was changed outside Terraform, and this plan reverts it.

13 updates only change tags or formatting.
```

Leave out a section with nothing in it. When the risk is low and nothing is flagged, say so in a sentence.

## Rules

- Never apply. Do not run `terraform apply`, `tofu apply` or `interragate apply`, and never pass `--auto-approve`, unless the user explicitly asks. `interragate apply` asks the user to confirm; leave that to them.
- Do not edit the configuration to make a finding go away. Offer interragate's fix (findings carry remediations and pasteable `moved`, `removed` or `lifecycle` blocks) and let the user decide.
- Sensitive values are masked in all of interragate's output. Leave them masked.
- `--open`, `interragate watch` and `interragate demo` open a browser. To hand over the interactive report, write it to a file instead: `interragate report PLAN -o interragate-report.html`.

## More

- The JSON report and jq recipes: [references/json-report.md](references/json-report.md)
- `.interragate.yaml`, from severities and ignores to the team's own policies: [references/configuration.md](references/configuration.md)
- interragate in CI, with exit codes, the GitHub Action, pull request comments and SARIF: [references/ci.md](references/ci.md)
- What a rule looks for, and why: `interragate rules --format json`
- What changed between two plans of the same configuration: `interragate diff BEFORE AFTER`
