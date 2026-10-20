# Security

Please report vulnerabilities privately through
[GitHub's vulnerability reporting form](https://github.com/Jeecabs/interragate/security/advisories/new).
If the form is unavailable, open an issue asking the maintainer for a
private reporting channel, without including vulnerability details.

Include the affected version or commit, a small synthetic reproduction,
the impact, and any proposed fix. Never send live credentials, state
files or production plans. The latest release and `main` are the first
places we investigate; fixes are published in a new release.

## Handling plans and reports

interragate analyses plans locally. Its standalone HTML reports embed
their assets and make no network requests. Watch mode serves reports on
loopback behind a random token.

Terraform and OpenTofu plan JSON can contain plaintext secrets. interragate
masks values marked sensitive and recognisable secrets in source edits,
but unmarked values, resource names, account IDs, endpoints and source
paths can still appear. Review every report before sharing it. Restrict
access to CI artifacts and PR comments containing infrastructure data.

Planning uses your credentials, may initialise providers and modules,
takes a state lock, and can run external data sources. Run untrusted
configurations only in an isolated environment with suitable credentials.
In CI, keep untrusted fork code away from cloud credentials and write
tokens. Use `pull_request` for fork checks; do not run that code under
`pull_request_target`.

A clean report means the configured rules found no issue at the selected
threshold. It is not a guarantee that an apply will succeed or preserve
data. Review the underlying plan and maintain backups.
