#!/usr/bin/env bash
# Regenerate a plan fixture from a scenario under testdata/scenarios/<name>.
#
# A scenario holds v1/ (the config that gets applied) and v2/ (the proposed
# change). Optional files:
#   between.sh  runs in the work dir after v1 is applied (taint, drift, ...)
#   plan-args   extra arguments for `plan`, one per line
#   env.sh      sourced before anything runs (endpoints, credentials); may
#               define scenario_cleanup, which runs on exit
#
# Usage: scripts/gen-fixture.sh <scenario> [terraform|tofu]
set -euo pipefail

scenario=${1:?usage: gen-fixture.sh <scenario> [terraform|tofu]}
bin=${2:-terraform}
root=$(cd "$(dirname "$0")/.." && pwd)
src=$root/testdata/scenarios/$scenario
out=$root/testdata/plans
work=$(mktemp -d)
# The work dir is outside the repo, so resolve mise shims to real paths first.
cd "$root"
tf=$(mise which "$bin" 2>/dev/null || command -v "$bin")
cleanup() {
  declare -F scenario_cleanup >/dev/null && scenario_cleanup
  rm -rf "$work"
}
trap cleanup EXIT

[[ -f $src/env.sh ]] && source "$src/env.sh"
export TF_IN_AUTOMATION=1 TF_INPUT=0

cp -r "$src/v1/." "$work/"
cd "$work"
"$tf" init -no-color >/dev/null
"$tf" apply -auto-approve -no-color >/dev/null

if [[ -f $src/between.sh ]]; then
  TF="$tf" bash "$src/between.sh"
fi

find . -maxdepth 1 -name '*.tf' -delete
rm -rf modules
cp -r "$src/v2/." "$work/"
"$tf" init -no-color >/dev/null

args=()
[[ -f $src/plan-args ]] && mapfile -t args < "$src/plan-args"
"$tf" plan -no-color -out=plan.out "${args[@]}" > "$out/$scenario.$bin.txt"
"$tf" show -json plan.out | python3 -m json.tool --indent 1 > "$out/$scenario.$bin.json"
echo "wrote $out/$scenario.$bin.json"
