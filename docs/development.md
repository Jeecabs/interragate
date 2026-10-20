# Development

The [contribution guide](../CONTRIBUTING.md) covers bug reports, pull requests
and licensing. Development needs Go 1.26 or later, Node.js 24 or later and
make; plugin checks also need uv and jq. Python 3.12 or later is needed for
release packaging. No cloud account is needed for the tests or sample reports.

```sh
mise install        # Go, Terraform and OpenTofu, pinned in mise.toml
make test           # unit and golden tests against real plans
make check          # tests, generated docs, module metadata, plugin and website
make build          # ./interragate
make fixtures       # regenerate testdata/plans (uses moto for the AWS scenario)
make docs           # regenerate docs/rules.md
make site           # the website GitHub Pages serves, into _site/ (serve it with any static server)
make plugin         # regenerate the agent plugin's manifests and catalogs from its apm.yml files
```

The fixtures are real plans. `testdata/scenarios/*` holds a v1 configuration that gets applied, the v2 change and any out-of-band drift. `scripts/gen-fixture.sh` applies v1, makes the drift, plans v2 and saves the JSON. The AWS scenario runs against [moto](https://github.com/getmoto/moto), so no cloud account is needed.

## The agent plugin

`plugins/interragate` packages the `reviewing-terraform-plans` [Agent Skill](https://agentskills.io) for Claude Code, Codex, Copilot, Cursor and other agents. [APM](https://github.com/microsoft/apm) packages it from two files, the only ones to edit by hand: `plugins/interragate/apm.yml` describes the plugin, and `apm.yml` at the top of the repository the catalog that lists it. `make plugin` writes everything else from them, and all of it is committed:

- the plugin's manifests, in `plugins/interragate`: `.claude-plugin/plugin.json` (Codex reads it too), `.github/plugin/plugin.json` (Copilot) and `.cursor-plugin/plugin.json`, with `apm.lock.yaml`;
- the catalogs, at the top: `.claude-plugin/marketplace.json`, `.agents/plugins/marketplace.json` (Codex), `.github/plugin/marketplace.json` (Copilot) and `.cursor-plugin/marketplace.json`.

The script also copies the root `LICENSE` and `COPYRIGHT` into the plugin
and the skill directory. Installing the skill alone or packing it as a
plugin retains its licence notices.

APM writes nothing for Cursor, so `scripts/plugin.sh` makes its manifest and catalog from the Claude Code ones. The script pins APM's version and runs it with uv; it also needs jq. `make plugin-check` fails when a file differs from what the `apm.yml` files make, and CI runs it.

To ship a change to the skill, bump `version` in `plugins/interragate/apm.yml` and run `make plugin`: Claude Code and Codex users only get a new version of the plugin when its version changes.

For agents that read [Agent Plugins](https://agent-plugins.org), APM builds a portable bundle into `plugins/interragate/build/`:

```sh
cd plugins/interragate && uvx --from apm-cli==0.34.0 apm pack --format agent-plugin
```

Check the skill against the spec, and the catalog and the plugin against Claude Code's validator:

```sh
uvx --from skills-ref==0.1.1 agentskills validate plugins/interragate/skills/reviewing-terraform-plans
claude plugin validate .
claude plugin validate plugins/interragate
```

`plugins/interragate/evals` holds evals for [`claude plugin eval`](https://code.claude.com/docs/en/plugin-evals): reviewing a plan, saying whether it is safe to apply, writing a policy, and leaving unrelated work alone, each scored with the skill and without it. They need interragate on `PATH` somewhere outside `/tmp`, which the eval sandbox hides:

```sh
claude plugin eval plugins/interragate --scaffold --no-publish --allow-tools Bash Write Edit
```

## The README's pictures

The explainers in `docs/images/explain-*.png` are drawn from the sample plan's real report. `node docs/explainers/render.mjs` draws them again, in light and dark; see [docs/explainers/README.md](explainers/README.md).

## Roadmap

- Planning the base branch as well, for an exact "what does this branch change" next to the edit-by-edit account.
- Cost deltas from Infracost on each changed resource.
- Schema-aware diffs (`terraform providers schema -json`) for sets and nested blocks.
- An optional LLM-written narrative, off by default.

## Release checks

CI checks Go 1.26 and 1.27 on Linux, and Go 1.27 on macOS. A separate
security workflow scans reachable Go vulnerabilities, Git history for
secrets, and workflow syntax. Dependencies and actions have weekly
Dependabot updates; external actions are pinned to full commit SHAs.

The [release guide](releasing.md) covers public repository setup, packaging
and publishing a reviewed draft release.
