# interragate in CI

`interragate check PLAN` is built for pipelines. It exits 0 when no finding reaches `--fail-on` (default `high`), 2 when one does, and 1 when interragate itself failed.

Formats, with `-f`:

- `markdown`: a pull request comment, kept under GitHub's 65,536-character limit
- `json`: the full report for other tools
- `github`: GitHub Actions annotations on the `.tf` lines in the diff
- `sarif`: code scanning results, each on its resource's block
- `mermaid`: a diagram of what changes, which GitHub and GitLab draw

`--markdown FILE`, `--json FILE` and `--html FILE` write more formats in the same run. `interragate report PLAN -o report.html` writes the interactive report, to keep as a build artifact.

## GitHub Actions

The interragate repository is an action:

```yaml
on: pull_request

permissions:
  contents: read
  pull-requests: write     # for comment: true
  security-events: write   # for sarif: true

jobs:
  plan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0   # so interragate can tell the branch's edits from everything else
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

Other inputs: `report-artifact` (artifact name for the HTML report, empty to skip), `scan` (`trivy`, `checkov`, `tflint`, installed first), `sarif: true` (upload to code scanning) and `base` (the git ref to compare with; the pull request's base branch by default). The action writes the summary to the job summary, and sets the outputs `risk` and `findings`.

Comments and SARIF uploads are skipped for fork pull requests and Dependabot. Keep cloud credentials out of workflows that run untrusted configurations. For a fixed action version, pin a full commit SHA instead of `main`.

For OpenTofu, set up `opentofu/setup-opentofu` instead: interragate finds `tofu` itself, or set `INTERRAGATE_TF_BIN: tofu`.

## Anywhere else

```sh
terraform plan -input=false -out tfplan
interragate check tfplan -f markdown -o interragate.md --fail-on high; status=$?
interragate report tfplan -o interragate-report.html
# post interragate.md with the platform's API, and keep interragate-report.html as an artifact
exit $status
```

With Atlantis, add `interragate check $PLANFILE -f markdown` as a custom workflow step after `plan`.
