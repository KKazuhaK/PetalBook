# Ignore any go.work in a parent directory; this module builds on its own.
export GOWORK := off

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
SPACE   ?= ./space

# The cores of this machine, and shares of them for lanes that run side by side
# (scripts/lanes.sh): a lane given SHARE=n uses n cores' worth of workers, for Go
# and for the web tests.
NCPU    := $(shell nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 2)
HALF    := $(shell n=$(NCPU); echo $$(( (n + 1) / 2 )))
THIRD   := $(shell n=$(NCPU); echo $$(( (n + 2) / 3 > 2 ? (n + 2) / 3 : 2 )))
QUARTER := $(shell n=$(NCPU); echo $$(( n / 4 > 2 ? n / 4 : 2 )))
ifneq ($(SHARE),)
export GOMAXPROCS := $(SHARE)
export VITEST_MAX_WORKERS := $(SHARE)
endif

# Every Go package with tests but the two that "test-index" and "test-space" run.
REST_PKGS = $(shell go list ./cmd/... ./internal/... | awk '!/\/internal\/(index|space)$$/')

.PHONY: build web release test test-full test-index test-space test-rest test-postgres test-casefold vet run clean \
	contract contract-postgres fmt-check tidy-check actionlint vuln check-go check-go-rest check-web check-web-static check check-full \
	cross-compile web-typecheck web-lint web-test

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

# Under the race detector a few tests use smaller sizes (see raceDetector in
# internal/index and internal/space): this is the run for pull requests.
test:
	go test -race ./cmd/... ./internal/...

# The same tests without the race detector, so that those tests run at their
# full size. It is quick, and is what main runs beside "test".
test-full:
	go test ./cmd/... ./internal/...

# "test" in three parts that together are all of it, so that CI runs them on
# machines of their own: the two packages that take longest, and the rest.
test-index:
	go test -race ./internal/index

test-space:
	go test -race ./internal/space

test-rest:
	go test -race $(REST_PKGS)

# The tests of the packages that use the state database (the ones that import
# internal/store/storetest), run on SQLite and on PostgreSQL in a throwaway
# cluster (needs initdb, pg_ctl and psql on PATH). The other packages never
# read the database, so running them again would prove nothing.
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

# The accounts-mode half of the contract tests, with PostgreSQL as the state
# database: a throwaway cluster (needs initdb, pg_ctl and psql on PATH), or the
# server in PETALBOOK_TEST_POSTGRES with a fresh database made for the run.
contract-postgres: build web/node_modules/.package-lock.json
	bash scripts/contract-postgres.sh ./petalbook

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
	@command -v shellcheck >/dev/null || echo "warning: shellcheck is not installed, so actionlint does not check the run: scripts; CI does" >&2
	go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

# Known vulnerabilities in the standard library and in modules the code calls.
# Needs network access to the vulnerability database.
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 ./...

# All the Go checks that need nothing but a Go toolchain (SQLite only).
check-go: fmt-check tidy-check vet test

# The same without the two slowest packages, which CI runs on machines of their
# own (test-index, test-space): the Go checks of the third machine.
check-go-rest: fmt-check tidy-check vet test-rest

# The web app's typecheck, lint and tests (CI runs the first two and the tests
# on machines of their own).
web-typecheck: web/node_modules/.package-lock.json
	npm --prefix web run typecheck

web-lint: web/node_modules/.package-lock.json
	npm --prefix web run lint

web-test: web/node_modules/.package-lock.json
	npm --prefix web test

check-web: web-typecheck web-lint web-test

# The typecheck and lint, without the tests.
check-web-static: web-typecheck web-lint

# The presubmit: what a pull request must pass, and what to run before
# pushing. No PostgreSQL, no root, no Docker. The Go and web checks run side by
# side (scripts/lanes.sh), each target's output comes whole when it ends, and a
# table of how long each took closes the run. "make check-go check-web vuln
# actionlint" runs the same checks one after the other.
check:
	@bash scripts/lanes.sh go:$(HALF)='fmt-check tidy-check vet test' web:$(HALF)='web-typecheck web-lint web-test' vuln actionlint

# The tests on a case-folding tmpfs, the way macOS and Windows treat file
# names (Linux only, needs a kernel with tmpfs casefold and user namespaces).
test-casefold:
	bash scripts/test-casefold.sh

# The server builds without cgo for every release platform. The web app is
# not embedded here: this only proves the Go code compiles for each target.
# Each platform compiles the whole dependency tree again, so the work is the
# same for all six and CPU bound: CI runs one machine per OS
# ("make cross-compile OSES=darwin"), and a bare "make cross-compile" does all.
OSES ?= linux darwin windows
cross-compile:
	@dir="$$(mktemp -d)"; trap 'rm -rf "$$dir"' EXIT; \
	for os in $(OSES); do \
	  for arch in amd64 arm64; do \
	    echo "== $$os/$$arch"; \
	    CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -o "$$dir/petalbook" ./cmd/petalbook || exit 1; \
	  done; \
	done

# The postsubmit: everything main runs after a merge. Needs the PostgreSQL
# tools (initdb, pg_ctl, psql) and a Linux kernel with tmpfs casefold. Like
# "check" it runs in lanes: first the checks, the full-size tests, PostgreSQL and
# the case-folding file system side by side, then the release build (which puts
# the web app into internal/webui, so it must not run beside the Go tests) with the
# contract tests that use its binary.
check-full:
	@bash scripts/lanes.sh \
	  go:$(THIRD)='fmt-check tidy-check vet test test-full' web:$(QUARTER)='web-typecheck web-lint web-test' static='vuln actionlint cross-compile' \
	  postgres:$(QUARTER)=test-postgres casefold:$(THIRD)=test-casefold \
	  -- release='release contract contract-postgres'

