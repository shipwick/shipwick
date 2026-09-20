VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/shipwick/shipwick/pkg/version.Version=$(VERSION)

.PHONY: dev build test test-race test-race-docker test-integration test-dashboard lint clean help

# A bare `make` builds. It must never be `dev`, which starts the stack and does
# not return: tools that build a repository by running `make` — CodeQL's Go
# autobuilder is one — would wait for it until their time limit.
.DEFAULT_GOAL := build

## dev: run the full development stack (dashboard :3000, agent :9000, proxy :8080/:8443)
dev:
	docker compose up --build

## build: compile the binaries into ./bin
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/shipwick-agent ./agent/cmd/shipwick-agent
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/shipwick ./cli/cmd/shipwick

## test: unit tests (no Docker required)
test:
	go test ./...

## test-race: unit tests under the race detector (requires cgo)
test-race:
	CGO_ENABLED=1 go test -race ./...

## test-race-docker: the same, in a Linux container — for machines without cgo (Windows)
test-race-docker:
	docker run --rm -v "$(CURDIR):/src:ro" -v shipwick-gomod:/go/pkg/mod -v shipwick-gocache:/root/.cache/go-build \
		-w /src -e GOFLAGS=-buildvcs=false golang:1.27 go test -race -count=1 ./...

## test-integration: tests against a real Docker daemon
test-integration:
	go test -tags integration -count=1 -run Integration ./agent/internal/docker/

## test-dashboard: typecheck, unit tests and production build of the dashboard
test-dashboard:
	cd dashboard && npm ci && npm run typecheck && npm test && npm run build

## lint: formatting and static checks
lint:
	@test -z "$$(gofmt -l agent cli pkg 2>/dev/null)" || { echo "gofmt needed:"; gofmt -l agent cli pkg; exit 1; }
	go vet ./...
	go vet -tags integration ./agent/internal/docker/

clean:
	rm -rf bin

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'
