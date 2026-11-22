# Ignore any go.work in a parent directory; this module builds on its own.
export GOWORK := off

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
SPACE   ?= ./space

.PHONY: build web release test test-postgres test-casefold vet run clean contract \
	fmt-check tidy-check actionlint vuln check-go check-web check check-full cross-compile

# Go binary only, no Node needed: it serves the API, and the web app is run
# with its dev server. "make release" builds the one-program version.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o petalbook ./cmd/petalbook

# The built web app, in web/dist.
web:
	npm --prefix web ci
	npm --prefix web run build

# The single binary with the web app embedded. The copy below is what
# internal/webui embeds; git ignores it, and "make clean" removes it again.
# Old files go first so a renamed asset does not linger in the binary.
release: web
	find internal/webui/dist -mindepth 1 ! -name placeholder.txt -delete
	cp -R web/dist/. internal/webui/dist/
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o petalbook ./cmd/petalbook

test:
	go test -race ./cmd/... ./internal/...

# The same tests, with the state-database ones also run on PostgreSQL in a
# throwaway cluster (needs initdb, pg_ctl and psql on PATH).
test-postgres:
	bash scripts/test-postgres.sh

vet:
	go vet ./...

# Serve $(SPACE) on loopback: make run SPACE=~/notes
# The Vite dev server on 5173 proxies to this server, so its origins are allowed.
run: build
	./petalbook serve --space $(SPACE) --allow-origin http://localhost:5173 --allow-origin http://127.0.0.1:5173

clean:
	rm -f petalbook
	find internal/webui/dist -mindepth 1 ! -name placeholder.txt -delete

# The web app's API contract tests, run against the real server instead of
# the mock they use by default. Installs the web dependencies if missing.
contract: build web/node_modules/.package-lock.json
	bash scripts/contract.sh ./petalbook

# Every check is defined here and nowhere else: CI only calls these targets,
# so what passes on a laptop is what passes on the runner.
#
#   make check        before every push (the pull request checks)
#   make check-full   before a risky change (what main runs after a merge)

# The web dependencies, installed only when missing so local runs stay fast.
# After package-lock.json changes, run "make web" or "npm --prefix web ci".
# npm writes this file on every install: it is older than the lock file after
# a pull that changed dependencies, and then they are installed again, as CI does.
web/node_modules/.package-lock.json: web/package-lock.json
	npm --prefix web ci

# Fails and lists the files that gofmt would change. spikes/ is left out on
# purpose: it is old exploratory code that is not built or maintained.
fmt-check:
	@out="$$(gofmt -l cmd internal)"; \
	if [ -n "$$out" ]; then echo "not gofmt-formatted:" >&2; echo "$$out" >&2; exit 1; fi

# go.mod and go.sum must already be what "go mod tidy" writes. This compares
# with the index, so stage a deliberate go.mod change before running it.
tidy-check:
	go mod tidy
	git diff --exit-code go.mod go.sum

# Lints the workflow files (expressions, job wiring, inputs).
actionlint:
	go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

# Known vulnerabilities in the standard library and in modules the code calls.
# Needs network access to the vulnerability database.
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

# All the Go checks that need nothing but a Go toolchain (SQLite only).
check-go: fmt-check tidy-check vet
	go test -race ./cmd/... ./internal/...

# The web app's typecheck, lint and tests.
check-web: web/node_modules/.package-lock.json
	npm --prefix web run typecheck
	npm --prefix web run lint
	npm --prefix web test

# The presubmit: what a pull request must pass, and what to run before
# pushing. No PostgreSQL, no root, no Docker.
check: check-go vuln check-web actionlint

# The tests on a case-folding tmpfs, the way macOS and Windows treat file
# names (Linux only, needs a kernel with tmpfs casefold and user namespaces).
test-casefold:
	bash scripts/test-casefold.sh

# The server builds without cgo for every release platform. The web app is
# not embedded here: this only proves the Go code compiles for each target.
cross-compile:
	@dir="$$(mktemp -d)"; trap 'rm -rf "$$dir"' EXIT; \
	for os in linux darwin windows; do \
	  for arch in amd64 arm64; do \
	    echo "== $$os/$$arch"; \
	    CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -o "$$dir/petalbook" ./cmd/petalbook || exit 1; \
	  done; \
	done

# The postsubmit: everything main runs after a merge. Needs the PostgreSQL
# tools (initdb, pg_ctl, psql) and a Linux kernel with tmpfs casefold.
check-full: check test-postgres test-casefold release contract cross-compile
