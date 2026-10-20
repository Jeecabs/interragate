#!/usr/bin/env bash
# Build the agent plugin's manifests and the catalogs that list it, or check
# them.
#
# Usage: scripts/plugin.sh [check]
#
# APM (https://github.com/microsoft/apm) writes them from two hand-edited
# files: plugins/interragate/apm.yml makes the plugin's manifests, for Claude
# Code (which Codex reads too) and Copilot, and its apm.lock.yaml; apm.yml at
# the top makes the catalogs, for Claude Code, Codex and Copilot. APM writes
# nothing for Cursor, so its manifest and catalog are made here from the
# Claude Code ones. With check, nothing is written, and it exits 1 when a
# file differs from what the apm.yml files make. Needs uv (for uvx) and jq.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
apm=(uvx --from apm-cli==0.34.0 apm)

outputs=(
  plugins/interragate/LICENSE
  plugins/interragate/COPYRIGHT
  plugins/interragate/skills/reviewing-terraform-plans/LICENSE
  plugins/interragate/skills/reviewing-terraform-plans/COPYRIGHT
  plugins/interragate/.claude-plugin/plugin.json
  plugins/interragate/.github/plugin/plugin.json
  plugins/interragate/.cursor-plugin/plugin.json
  plugins/interragate/apm.lock.yaml
  .claude-plugin/marketplace.json
  .agents/plugins/marketplace.json
  .github/plugin/marketplace.json
  .cursor-plugin/marketplace.json
)

# Write every file in outputs, in the checkout at $1.
build() (
  cp "$1/LICENSE" "$1/COPYRIGHT" "$1/plugins/interragate/"
  cp "$1/LICENSE" "$1/COPYRIGHT" "$1/plugins/interragate/skills/reviewing-terraform-plans/"
  cd "$1/plugins/interragate"
  # apm pack leaves a plugin.json that is already there alone, and fills
  # build/ from the first one it finds, so start without them.
  rm -f .claude-plugin/plugin.json .github/plugin/plugin.json .cursor-plugin/plugin.json
  "${apm[@]}" lock
  "${apm[@]}" pack

  cd "$1"
  "${apm[@]}" pack

  # Cursor's schemas (https://github.com/cursor/plugins/tree/main/schemas)
  # allow no field they do not define: its manifest has no author.url, and
  # its catalog no owner.url, keeps its description in metadata, and gives
  # each plugin only a name, a source and a description.
  mkdir -p plugins/interragate/.cursor-plugin .cursor-plugin
  jq 'del(.author.url) + {logo: "./assets/logo.svg"}' \
    plugins/interragate/.claude-plugin/plugin.json > plugins/interragate/.cursor-plugin/plugin.json
  jq '{name, owner: (.owner | del(.url)), metadata: {description},
       plugins: [.plugins[] | {name, source: (.source | ltrimstr("./")), description}]}' \
    .claude-plugin/marketplace.json > .cursor-plugin/marketplace.json
)

if [[ ${1:-} == check ]]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  cp -R "$root/apm.yml" "$root/LICENSE" "$root/COPYRIGHT" "$root/plugins" "$tmp"
  build "$tmp" > /dev/null # APM reports errors on stderr
  stale=0
  for f in "${outputs[@]}"; do
    diff -uN --label "$f" --label "$f, from apm.yml" "$root/$f" "$tmp/$f" >&2 || stale=1
  done
  if ((stale)); then
    echo "The agent plugin's manifests or catalogs are out of date; run make plugin" >&2
    exit 1
  fi
else
  build "$root"
fi
