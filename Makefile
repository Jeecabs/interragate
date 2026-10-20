VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test check docs-check fixtures demo-data docs demo site plugin plugin-check release

build:
	go build -ldflags "$(LDFLAGS)" -o interragate ./cmd/interragate

# Build and put interragate on your PATH (see scripts/install.sh); BINDIR
# chooses the directory.
install:
	scripts/install.sh "$(BINDIR)"

# go list skips hidden directories, such as git worktrees kept inside the
# checkout.
test:
	test -z "$$(gofmt -l $$(go list -f '{{.Dir}}' ./...))"
	go vet ./...
	go test ./...
	node --check internal/render/html/assets/app.js
	node scripts/check-js.mjs internal/render/html/assets/app.js
	node --check site/site.js
	node scripts/check-js.mjs site/site.js

check: test docs-check plugin-check site

docs-check:
	go run ./cmd/interragate rules --format markdown | diff -u docs/rules.md -
	go mod tidy -diff

# Regenerate the plan fixtures from testdata/scenarios. The AWS scenario
# starts a throwaway moto server (needs uv); nothing touches a real cloud.
fixtures:
	scripts/gen-fixture.sh mechanics terraform
	scripts/gen-fixture.sh mechanics tofu
	scripts/gen-fixture.sh aws-webapp terraform
	scripts/gen-fixture.sh aws-webapp tofu
	scripts/gen-demo-data.sh

# Copy the fixtures into the binary's samples for interragate demo.
demo-data:
	scripts/gen-demo-data.sh

docs:
	go run ./cmd/interragate rules --format markdown > docs/rules.md

demo:
	go run ./cmd/interragate demo

# Build the website GitHub Pages serves into _site: the landing page, the
# rules and the sample reports, drawn by this checkout's interragate.
site:
	go run site/build.go -o _site

# Regenerate the agent plugin's manifests and catalogs from the apm.yml
# files (needs uv and jq); plugin-check fails when one is out of date.
plugin:
	scripts/plugin.sh

plugin-check:
	scripts/plugin.sh check

# Run from a clean commit. VERSION must be a semantic version such as v0.1.0.
release:
	python3 scripts/release.py "$(VERSION)"
