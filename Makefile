SHELL := /usr/bin/env bash
.DEFAULT_GOAL := build

GO ?= go
PYTHON ?= python3
UV ?= uv
GOCACHE ?= $(CURDIR)/.cache/go-build
GO_PARALLEL ?= 2
GOMAXPROCS ?= $(GO_PARALLEL)
export GOCACHE
export GOMAXPROCS
TOOLS_DIR := $(CURDIR)/.tools/bin
GOIMPORTS := $(TOOLS_DIR)/goimports
STATICCHECK := $(TOOLS_DIR)/staticcheck
GOLANGCI_LINT := $(TOOLS_DIR)/golangci-lint
PYTHON_PROJECT := $(CURDIR)/sidecar/python

GOIMPORTS_VERSION := v0.36.0
STATICCHECK_VERSION := 2025.1.1
GOLANGCI_LINT_VERSION := v2.4.0

.PHONY: tools python-sync fmt fmt-check lint test test-race build check clean legacy-check

$(TOOLS_DIR):
	mkdir -p "$@"

$(GOIMPORTS): | $(TOOLS_DIR)
	GOBIN="$(TOOLS_DIR)" $(GO) install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)

$(STATICCHECK): | $(TOOLS_DIR)
	GOBIN="$(TOOLS_DIR)" $(GO) install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)

$(GOLANGCI_LINT): | $(TOOLS_DIR)
	GOBIN="$(TOOLS_DIR)" $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

tools: $(GOIMPORTS) $(STATICCHECK) $(GOLANGCI_LINT)

python-sync:
	$(UV) sync --project "$(PYTHON_PROJECT)" --locked --group dev

fmt: $(GOIMPORTS) python-sync
	find . -type f -name '*.go' -not -path './.tools/*' -print0 | xargs -0 gofmt -w
	find . -type f -name '*.go' -not -path './.tools/*' -print0 | xargs -0 "$(GOIMPORTS)" -w
	$(UV) run --project "$(PYTHON_PROJECT)" ruff format "$(PYTHON_PROJECT)"
	$(UV) run --project "$(PYTHON_PROJECT)" ruff check --fix "$(PYTHON_PROJECT)"

fmt-check: $(GOIMPORTS) python-sync
	@test -z "$$(find . -type f -name '*.go' -not -path './.tools/*' -print0 | xargs -0 gofmt -l)" || \
		{ echo 'gofmt changes required'; find . -type f -name '*.go' -not -path './.tools/*' -print0 | xargs -0 gofmt -l; exit 1; }
	@test -z "$$(find . -type f -name '*.go' -not -path './.tools/*' -print0 | xargs -0 "$(GOIMPORTS)" -l)" || \
		{ echo 'goimports changes required'; find . -type f -name '*.go' -not -path './.tools/*' -print0 | xargs -0 "$(GOIMPORTS)" -l; exit 1; }
	$(UV) run --project "$(PYTHON_PROJECT)" ruff format --check "$(PYTHON_PROJECT)"
	$(UV) run --project "$(PYTHON_PROJECT)" ruff check "$(PYTHON_PROJECT)"

lint: $(STATICCHECK) $(GOLANGCI_LINT) python-sync
	$(GO) vet -p=$(GO_PARALLEL) ./...
	"$(STATICCHECK)" ./...
	"$(GOLANGCI_LINT)" run ./...
	$(UV) run --project "$(PYTHON_PROJECT)" pyrefly check "$(PYTHON_PROJECT)/src" "$(PYTHON_PROJECT)/tests"
	$(UV) run --project "$(PYTHON_PROJECT)" $(PYTHON) -m compileall -q "$(PYTHON_PROJECT)/src" "$(PYTHON_PROJECT)/tests"

test: python-sync
	$(GO) test -p=$(GO_PARALLEL) ./...
	$(UV) run --project "$(PYTHON_PROJECT)" $(PYTHON) -m unittest discover -s "$(PYTHON_PROJECT)/tests" -t "$(PYTHON_PROJECT)"

test-race:
	$(GO) test -p=$(GO_PARALLEL) -race ./...

build: python-sync
	mkdir -p bin
	$(GO) build -p=$(GO_PARALLEL) -trimpath -o bin/serenox ./cmd/serenox
	$(GO) build -p=$(GO_PARALLEL) -trimpath -o bin/serenoxd ./cmd/serenoxd
	$(GO) build -p=$(GO_PARALLEL) -trimpath -o bin/serenox-plugin ./cmd/serenox-plugin
	$(GO) build -p=$(GO_PARALLEL) -trimpath -o bin/srnx-plugin ./cmd/srnx-plugin
	$(UV) run --project "$(PYTHON_PROJECT)" $(PYTHON) -m compileall -q "$(PYTHON_PROJECT)/src"

legacy-check:
	./scripts/check-legacy-tree.sh

check: fmt-check lint test test-race build legacy-check

clean:
	rm -rf bin .cache .tools "$(PYTHON_PROJECT)/.venv" "$(PYTHON_PROJECT)"/**/__pycache__
