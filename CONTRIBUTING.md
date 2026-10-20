# Contributing

Bug reports, documentation fixes and pull requests are welcome. For a
larger change, open an issue first so we can agree on its behaviour and
scope. Keep discussion respectful and focused on the work.

## Report a problem

Include the interragate version, operating system, Terraform or OpenTofu
version, the command you ran, and what you expected to happen. A small
reproduction is more useful than a full infrastructure checkout.

Plans and reports can contain credentials and infrastructure details.
Remove them before posting. Use synthetic resources where possible.
Report security problems privately using [SECURITY.md](SECURITY.md).

## Develop a change

You need Go 1.26 or later, Node.js 24 or later, and make. The versions in
`mise.toml` are the development defaults. Plugin work also needs uv and jq;
release packaging needs Python 3.12 or later.

```sh
git clone https://github.com/Jeecabs/interragate.git
cd interragate
make test
make build
./interragate demo --format text
```

`make test` checks Go formatting, vet, tests, and the report and website
JavaScript. Run `go test -race ./...` for changes involving concurrency.
Add a regression test when fixing behaviour, and keep changes focused.

Run `make docs` when changing rules and `make plugin` when changing the
plugin manifests. Before sending a pull request, run `make check` and
check `git diff --check`. The [development guide](docs/development.md)
covers fixtures, screenshots and plugin validation.

Use UK English for documentation and user-facing text. Follow the
surrounding Go and JavaScript style. Describe the problem, the resulting
behaviour and how you checked it in your pull request.

## Licence

By submitting a contribution, you agree to license it under
`AGPL-3.0-only`, as described in [LICENSING.md](LICENSING.md). You retain
your copyright. There is no contributor licence agreement or copyright
assignment. Keep third-party copyright and licence notices intact.
