#!/usr/bin/env bash
# Copies the sample plan (the one interragate demo explains) into the run's
# workspace as plan.json. Cases name this as their scaffold_script.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here" && git rev-parse --show-toplevel)
cp "$root/testdata/plans/aws-webapp.terraform.json" plan.json
