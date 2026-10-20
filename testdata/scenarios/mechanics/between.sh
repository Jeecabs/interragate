# Runs after v1 is applied: simulate a tainted resource and drift.
set -euo pipefail
"$TF" taint -no-color random_integer.port >/dev/null
rm -f out/README.md
