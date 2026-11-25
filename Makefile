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

.PHONY: build web release test test-live-daily test-postgres test-casefold vet run clean \
	contract contract-postgres fmt-check tidy-check actionlint vuln secrets lint hooks check-go check-web check check-full \
	cross-compile web-typecheck web-lint web-format-check web-test

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

# go test stops a package after 10 minutes by default; internal/api under the
# race detector is close to that on a busy CI machine, so every run gets 30.
GOTEST_TIMEOUT ?= 30m

# All the Go tests, under the race detector. A few stress and model tests use a
# smaller size there (see raceDetector in internal/index and internal/space).
# PKGS narrows the run; CI's go job splits the packages into four groups.
PKGS ?= ./cmd/... ./internal/...
test:
	go test -race -timeout $(GOTEST_TIMEOUT) $(PKGS)

# The full-size stress tests: index and space use a smaller size under -race
# (raceDetector), so this runs them without it. Main runs it in the compat job.
test-fullsize:
	go test -timeout $(GOTEST_TIMEOUT) ./internal/index ./internal/space

# Asks every built-in daily-text provider over the real network (build tag
# live). Not part of check or CI: run it when a built-in provider changes.
test-live-daily:
	go test -tags live -count=1 -run TestLiveProviders -v -timeout 10m ./internal/dailyfetch

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
#   make check        before every push (the pull request jobs: lint, go, web)
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

# Scans the git history for secrets with gitleaks (rules and the few allowed
# test values: .gitleaks.toml). The output is redacted, so a log never shows one.
# The whole history by default; CI passes the commits of the push or pull
# request: make secrets SECRETS_RANGE=main..HEAD
GITLEAKS = github.com/zricethezav/gitleaks/v8@v8.30.1
secrets:
	@range="$(SECRETS_RANGE)"; \
	case "$$range" in 0000000000000000000000000000000000000000..*) range="-1 $${range#*..}";; esac; \
	if [ -n "$$range" ]; then set -- --log-opts="$$range"; else set --; fi; \
	go run $(GITLEAKS) git --no-banner --redact=100 "$$@" .

# Everything that only reads the code: the "lint" job of CI.
lint: fmt-check tidy-check vet web-typecheck web-lint web-format-check actionlint vuln secrets

# Use the hooks in .githooks (gofmt, prettier and eslint --fix on what is staged).
hooks:
	git config core.hooksPath .githooks

# The Go checks and tests that need nothing but a Go toolchain (SQLite only),
# which is also what a release re-runs on every platform.
check-go: fmt-check tidy-check vet test

# The web app's typecheck, lint, format check and tests.
web-typecheck: web/node_modules/.package-lock.json
	npm --prefix web run typecheck

web-lint: web/node_modules/.package-lock.json
	npm --prefix web run lint

web-format-check: web/node_modules/.package-lock.json
	npm --prefix web run format:check

web-test: web/node_modules/.package-lock.json
	npm --prefix web test

check-web: web-typecheck web-lint web-format-check web-test

# The presubmit: what a pull request must pass, and what to run before
# pushing: "make lint", "make test" and "make web-test" in one run. No
# PostgreSQL, no root, no Docker. The lanes run side by side (scripts/lanes.sh),
# each target's output comes whole when it ends, and a table of how long each
# took closes the run.
check:
	@bash scripts/lanes.sh go:$(HALF)='fmt-check tidy-check vet test' web:$(HALF)='web-typecheck web-lint web-format-check web-test' static='vuln actionlint secrets'

# The tests on a case-folding tmpfs, the way macOS and Windows treat file
# names (Linux only, needs a kernel with tmpfs casefold and user namespaces).
test-casefold:
	bash scripts/test-casefold.sh

# The server builds without cgo for every release platform. The web app is
# not embedded here: this only proves the Go code compiles for each target.
# Each target compiles the whole dependency tree again; they are built side by
# side. What differs between targets that matters to Go code is the operating
# system, so by default one target is built per system other than Linux, where
# the tests run; the release workflow builds every release target on tag day.
# Every target: make cross-compile CROSS_TARGETS="linux/amd64 linux/arm64
# darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
CROSS_TARGETS ?= darwin/arm64 windows/amd64
cross-compile:
	@dir="$$(mktemp -d)"; trap 'rm -rf "$$dir"' EXIT; status=0; pids=; \
	for t in $(CROSS_TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  ( CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -o "$$dir/petalbook-$$os-$$arch" ./cmd/petalbook && echo "== $$t ok" ) & \
	  pids="$$pids $$!"; \
	done; \
	for p in $$pids; do wait $$p || status=1; done; \
	exit $$status

# The postsubmit: everything main runs after a merge. Needs the PostgreSQL
# tools (initdb, pg_ctl, psql) and a Linux kernel with tmpfs casefold. Like
# "check" it runs in lanes: first the checks, PostgreSQL, and the case-folding
# file system with the cross-compile side by side, then the release build (which
# puts the web app into internal/webui, so it must not run beside the Go tests)
# with the contract tests that use its binary.
check-full:
	@bash scripts/lanes.sh \
	  go:$(THIRD)='fmt-check tidy-check vet test' web:$(QUARTER)='web-typecheck web-lint web-format-check web-test' static='vuln actionlint secrets' \
	  postgres:$(QUARTER)=test-postgres compat:$(THIRD)='test-casefold cross-compile test-fullsize' \
	  -- release='release contract contract-postgres'
