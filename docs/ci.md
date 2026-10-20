# interragate in CI

`interragate check` is built for pipelines:

- exit code **0**: no finding at or above `--fail-on`
- exit code **2**: at least one finding at or above `--fail-on` (default `high`)
- exit code **1**: interragate itself failed (bad input, terraform error)

Outputs:

- `-f text` (default): readable logs, no colour when not on a terminal
- `-f markdown`: a pull request comment, kept under GitHub's 65,536-character limit
- `-f json`: the full report for other tools (`"schema": 1`, see [report-format.md](report-format.md))
- `-f github`: GitHub Actions annotations, shown inline on the `.tf` lines in the diff
- `-f sarif`: code scanning results, each anchored to its resource's block
- `-f mermaid`: a diagram of what changes, which GitHub and GitLab draw in Markdown (the PR comment carries one)
- `--markdown FILE`, `--json FILE`, `--html FILE`: write more formats in the same run
- `interragate report PLAN -o report.html`: the interactive report as a build artifact

## GitHub Actions

The repository is an action. It builds interragate from source and sets up Go for you. The example follows `main`; pin a full commit SHA for a reviewed, fixed version.

```yaml
name: plan
on: pull_request

permissions:
  contents: read
  pull-requests: write     # only needed for comment: true
  security-events: write   # only needed for sarif: true

jobs:
  plan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0   # so interragate can tell your edits from everything else
      - uses: hashicorp/setup-terraform@v3
        with:
          terraform_wrapper: false
      - run: terraform init -input=false
        working-directory: infra
      - run: terraform plan -input=false -out tfplan
        working-directory: infra
      - uses: Jeecabs/interragate@main
        with:
          plan: tfplan
          working-directory: infra
          fail-on: high
          comment: true
```

Inputs:

| Input | Default | |
|---|---|---|
| `plan` | | Plan file or plan JSON, relative to `working-directory` |
| `working-directory` | `.` | The Terraform directory |
| `fail-on` | `high` | Lowest severity that fails the step; `none` never fails |
| `comment` | `false` | Create or update one PR comment with the summary |
| `report-artifact` | `interragate-report` | Artifact name for the HTML report; empty to skip |
| `github-token` | `${{ github.token }}` | Token used for the comment |
| `scan` | | Scanners to run as well, comma-separated: `trivy`, `checkov`, `tflint` (install them first); interragate keeps what concerns this plan |
| `sarif` | `false` | Upload the findings to code scanning, for the Security tab and the pull request's files |
| `base` | the PR's base branch | Git ref to tell your edits from everything else; needs the checkout's history (`fetch-depth: 0`) |

The action always writes the Markdown summary to the job summary, so it shows on the run page even without a comment. Findings also appear as annotations on the `.tf` lines in the pull request's diff (when the configuration sits next to the plan, so interragate knows where each resource is defined).

Comments and SARIF uploads are skipped for pull requests from forks and Dependabot, whose tokens normally have read-only permissions. Annotations, the job summary and report artifacts still work. Keep cloud credentials out of workflows that run untrusted configurations. See [SECURITY.md](../SECURITY.md).

Using OpenTofu? Swap the setup step for `opentofu/setup-opentofu` and interragate finds `tofu` itself, or set `INTERRAGATE_TF_BIN: tofu`.

## Anywhere else

```sh
terraform plan -input=false -out tfplan
interragate check tfplan -f markdown -o interragate.md --fail-on high; status=$?
interragate report tfplan -o interragate-report.html
# post interragate.md with your platform's API, keep interragate-report.html as an artifact
exit $status
```

With Atlantis, add `interragate check $PLANFILE -f markdown` as a custom workflow step after `plan`. Its output is posted with the plan comment.
