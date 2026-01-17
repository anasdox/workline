.PHONY: help test fmt tidy serve openapi

# Local Go build cache stays in repo to avoid permission issues.
GOCACHE ?= $(CURDIR)/.cache/go-build
GOMODCACHE ?= $(CURDIR)/.gomodcache

ifneq (,$(WORKLINE_GOCACHE))
GOCACHE := $(WORKLINE_GOCACHE)
endif
ifneq (,$(WORKLINE_GOMODCACHE))
GOMODCACHE := $(WORKLINE_GOMODCACHE)
endif

# Auto-load .env if present (not committed).
ifneq (,$(wildcard .env))
include .env
export $(shell sed -n 's/^\([^#][^=]*\)=.*/\1/p' .env)
endif
ifneq (,$(wildcard .env.automation))
include .env.automation
export $(shell sed -n 's/^\([^#][^=]*\)=.*/\1/p' .env.automation)
endif

help:
	@echo "Available targets:"
	@echo "  test    - run go test ./... with local cache"
	@echo "  fmt     - gofmt Go sources"
	@echo "  tidy    - go mod tidy"
	@echo "  serve   - start API server (requires WORKLINE_JWT_SECRET)"
	@echo "  import-example-config - import workline.example.yml into the DB"
	@echo "  restore-langchain-project - reset project data for the LangChain example"
	@echo "  run-langchain-example - mint dev JWT and run LangChain example"
	@echo "  bootstrap-automation - create planner/executor/reviewer roles + API keys"
	@echo "  bootstrap-automation-env - write .env.automation for agent API keys"

test:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test ./...

fmt:
	gofmt -w cmd internal sdk

tidy:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go mod tidy

serve:
	@[ -n "$$WORKLINE_JWT_SECRET" ] || (echo "WORKLINE_JWT_SECRET is required" && exit 1)
	@[ -n "$$WORKLINE_DEFAULT_PROJECT" ] || (echo "WORKLINE_DEFAULT_PROJECT is required (set with 'wl project use <id>')" && exit 1)
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go run ./cmd/wl serve --addr 127.0.0.1:8080 --base-path /v0 --project "$$WORKLINE_DEFAULT_PROJECT"

import-example-config:
	go run ./cmd/wl project config import --file workline.example.yml

restore-langchain-project:
	@[ -n "$$WORKLINE_PROJECT_ID" ] || (echo "WORKLINE_PROJECT_ID is required (set in .env)" && exit 1)
	@go run ./cmd/wl project delete --project "$$WORKLINE_PROJECT_ID" >/dev/null 2>&1 || true
	@go run ./cmd/wl project create --id "$$WORKLINE_PROJECT_ID"
	@go run ./cmd/wl project config import --file workline.example.yml --project "$$WORKLINE_PROJECT_ID"

run-langchain-example:
	@[ -n "$$WORKLINE_JWT_SECRET" ] || (echo "WORKLINE_JWT_SECRET is required (set in .env)" && exit 1)
	@[ -n "$$WORKLINE_PROJECT_ID" ] || (echo "WORKLINE_PROJECT_ID is required (set in .env)" && exit 1)
	@go run ./cmd/wl project config import --file workline.example.yml --project "$$WORKLINE_PROJECT_ID"
	@go run ./cmd/wl rbac bootstrap \
		--project "$$WORKLINE_PROJECT_ID" \
		--actor "$${DEV_ACTOR_ID:-owner-1}" \
		--role "$${DEV_ROLE:-owner}"
	@command -v uv >/dev/null 2>&1 || (echo "uv is required (https://github.com/astral-sh/uv)" && exit 1)
	@uv venv .venv >/dev/null 2>&1 || true
	@uv pip install -q langchain langchain-openai requests
	@TOKEN=$$(curl -s -X POST http://127.0.0.1:8080/v0/auth/dev/login \
		-H "Content-Type: application/json" \
		-d "{\"actor_id\":\"$${DEV_ACTOR_ID:-owner-1}\",\"org_id\":\"$${DEV_ORG_ID:-default-org}\",\"roles\":[\"$${DEV_ROLE:-owner}\"]}" \
		| python -c 'import json,sys; print(json.load(sys.stdin)["token"])'); \
	echo "WORKLINE_ACCESS_TOKEN=$$TOKEN"; \
	WORKLINE_ACCESS_TOKEN="$$TOKEN" \
	WORKLINE_PROJECT_ID="$$WORKLINE_PROJECT_ID" \
	./.venv/bin/python examples/langchain_workline.py

bootstrap-automation:
	@./scripts/bootstrap-automation.sh

bootstrap-automation-env:
	@./scripts/bootstrap-automation.sh --env-file .env.automation
	@echo "Wrote .env.automation (load it or copy into .env)."
