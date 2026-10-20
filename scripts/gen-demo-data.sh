#!/usr/bin/env bash
# Copy the sample plans, and the configuration each was planned from, into
# internal/demo, which embeds them so `interragate demo` works without
# Terraform, a cloud account or a plan of your own. Run it after
# `make fixtures`; the demo package's tests fail until the copies match the
# fixtures again.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
out=$root/internal/demo/data
rm -rf "$out"
for name in aws-webapp mechanics; do
  mkdir -p "$out/$name/config" "$out/$name/before"
  # -n leaves out the name and timestamp, so the archive only changes when
  # the plan does.
  gzip -9n < "$root/testdata/plans/$name.terraform.json" > "$out/$name/plan.json.gz"
  # moto_override.tf points the AWS provider at the throwaway server the
  # fixture was planned against; it is not part of the example.
  tar -C "$root/testdata/scenarios/$name/v2" \
    --exclude=.terraform --exclude='*.tfstate*' --exclude=moto_override.tf \
    -cf - . | tar -C "$out/$name/config" -xf -
  # The configuration before the change, which the demo commits under it
  # so the report can show the edit behind each change.
  tar -C "$root/testdata/scenarios/$name/v1" \
    --exclude=.terraform --exclude='*.tfstate*' --exclude=moto_override.tf \
    -cf - . | tar -C "$out/$name/before" -xf -
done
echo "wrote $out"
