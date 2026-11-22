# Ignore any go.work in a parent directory; this module builds on its own.
export GOWORK := off

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
SPACE   ?= ./space

.PHONY: build web release test test-postgres vet run clean contract

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
# the mock they use by default. Needs web dependencies installed.
contract: build
	bash scripts/contract.sh ./petalbook
