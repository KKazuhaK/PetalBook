# Ignore any go.work in a parent directory; this module builds on its own.
export GOWORK := off

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
SPACE   ?= ./space

.PHONY: build test test-postgres vet run clean contract

# Go binary only; the web app is embedded in a later milestone.
build:
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

# The web app's API contract tests, run against the real server instead of
# the mock they use by default. Needs web dependencies installed.
contract: build
	bash scripts/contract.sh ./petalbook
