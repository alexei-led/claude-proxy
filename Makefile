SHELL := /bin/bash
export GOTOOLCHAIN := local

.PHONY: fmt fmt-check lint test race vet build check lint-version

lint-version:
	@golangci-lint version | grep -Eq 'version 2\.15\.0( |$$)' || { echo 'golangci-lint 2.15.0 required; no tools are installed by make' >&2; exit 1; }

fmt: lint-version
	golangci-lint fmt ./...

fmt-check: lint-version
	@files="$$(gofmt -l cmd internal)" && test -z "$$files" || { printf '%s\n' "$$files"; exit 1; }
	@diff="$$(golangci-lint fmt --diff ./...)" && test -z "$$diff" || { printf '%s\n' "$$diff"; exit 1; }

lint: lint-version
	golangci-lint config verify
	golangci-lint run ./...

vet:
	go vet ./...

test:
	go test -timeout=60s ./...

race:
	go test -race -timeout=120s ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/claude-proxy ./cmd/claude-proxy

check: fmt-check lint vet test race build
	test "$$(go list -m all)" = github.com/alexei-led/claude-proxy
