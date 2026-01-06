.PHONY: help test fmt tidy serve openapi

# Local Go build cache stays in repo to avoid permission issues.
GOCACHE ?= $(CURDIR)/.cache/go-build

# Auto-load .env if present (not committed).
ifneq (,$(wildcard .env))
include .env
export $(shell sed -n 's/^\([^#][^=]*\)=.*/\1/p' .env)
endif

help:
	@echo "Available targets:"
	@echo "  test    - run go test ./... with local cache"
	@echo "  fmt     - gofmt Go sources"
	@echo "  tidy    - go mod tidy"
	@echo "  serve   - start API server (requires JWT_SECRET)"

test:
	GOCACHE=$(GOCACHE) go test ./...

fmt:
	gofmt -w cmd internal sdk

tidy:
	GOCACHE=$(GOCACHE) go mod tidy

serve:
	@[ -n "$$JWT_SECRET" ] || (echo "JWT_SECRET is required" && exit 1)
	GOCACHE=$(GOCACHE) go run ./cmd/pl serve --addr 127.0.0.1:8080 --base-path /v0
