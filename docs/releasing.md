# Releasing interragate

## First public release

Before changing repository visibility:

1. Run the checks below on the final commit. Review every tracked fixture
   and asset for private infrastructure data as well as credentials.
2. Confirm the project licence is `AGPL-3.0-only` and the release archives
   include copyright notices, third-party notices and corresponding source.
3. Keep any backup of the previous private history outside the repository.
   The public branch should start with the prepared initial commit. A
   history rewrite does not remove cached objects or copies held by others;
   rotate any credential that has ever been exposed.
4. Change visibility only when ready to publish. The Pages workflow skips
   private repositories. GitHub-hosted jobs can also be blocked by the
   account's Actions budget while the repository is private.

Once public, enable private vulnerability reporting in Settings → Security
and confirm the reporting link in `SECURITY.md` works. Enable Dependabot
alerts and secret scanning/push protection where available. Set Settings →
Pages → Source to **GitHub Actions**, then run the Pages workflow and check
the landing page, rules and both sample reports. Set the repository website
to `https://jeecabs.github.io/interragate/` once it is serving.

Protect `main` after the initial history rewrite. Require pull requests,
passing CI, and block force pushes. Select the actual
check names from a successful run. A fresh clone must work without private
credentials; try both `go install` and the sample report.

## Check a version

```sh
make check
go test -race ./...
go mod verify
uvx --from skills-ref==0.1.1 agentskills validate plugins/interragate/skills/reviewing-terraform-plans
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --redact --no-banner --max-decode-depth 3 --max-archive-depth 2
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
git diff --check
```

Do not publish with failed or missing checks. Check GitHub's run results
as well as the local commands. GitHub Actions requires no cloud credentials for this repo's
fixtures or demos.

Commit all changes before packaging. The script exports `HEAD`, vendors
the exact Go dependencies, builds from that source, and includes their
licence notices. It uses no repository history in the source archive.

```sh
make release VERSION=v0.1.0
```

`dist/` contains four binary archives (Linux and macOS, amd64 and arm64),
a source archive with `vendor/`, and `checksums.txt`. Test the native
binary with `version`, `demo --format json` and `check` on a fixture.
Extract the source archive and verify it builds without network access:

```sh
GOPROXY=off GOTOOLCHAIN=local go build -mod=vendor ./cmd/interragate
```

The source archive and its notices must remain available alongside the
binaries. Archives are reproducible for the same commit, Go toolchain
and target; timestamps and ownership are normalised.

## Publish

After GitHub CI passes on the commit being released,
create and push a version tag:

```sh
git tag -a v0.1.0 -m 'interragate v0.1.0'
git push origin v0.1.0
```

The release workflow reruns the checks, packages all targets and creates
a **draft** GitHub release. Review the notes, checksums, source and binary
assets before publishing it. Pin the first action example to the reviewed
commit SHA. Do not move an existing version tag.

For an agent skill update, bump `version` in
`plugins/interragate/apm.yml` and run `make plugin` before tagging.
