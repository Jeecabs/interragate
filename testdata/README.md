# Test fixtures

The plans in `plans/` are synthetic fixtures generated from `scenarios/`.
The mechanics scenario uses Terraform's built-in resources and the random,
local and null providers. The AWS web app runs against a local Moto server,
with fake credentials and a fake account; it does not need a cloud account.
See [the development guide](../docs/development.md) for regeneration.

`redact.json` and the Go regression tests deliberately contain obvious fake
passwords so tests can prove that reports remove them. The mechanics plans
also contain random strings named `api_key`; these have never authenticated
to a service. `.gitleaks.toml` permits these specific test patterns only in
their fixture or regression-test paths. It keeps the default secret rules
enabled for the rest of the repository.

Never add production plan, state or variable files here. Use a small
synthetic reproduction and review its contents before committing it.
