SHELL := /usr/bin/env bash

GO_FILES := $(shell find . -type f -name '*.go' -not -path './vendor/*' -not -path './.git/*' 2>/dev/null)
.PHONY: fmt fmt-check lint test test-e2e vuln security gate install-hooks build

fmt:
	@if [ -z "$(GO_FILES)" ]; then \
		echo "No Go files; skipping fmt"; \
	else \
		gofmt -w $(GO_FILES); \
		if command -v goimports >/dev/null 2>&1; then \
			goimports -w $(GO_FILES); \
		else \
			echo "goimports not installed; gofmt completed"; \
		fi; \
	fi

fmt-check:
	@if [ -z "$(GO_FILES)" ]; then \
		echo "No Go files; skipping fmt-check"; \
	else \
		unformatted="$$(gofmt -l $(GO_FILES))"; \
		if [ -n "$$unformatted" ]; then \
			echo "Go files need gofmt:"; \
			echo "$$unformatted"; \
			exit 1; \
		fi; \
		if command -v goimports >/dev/null 2>&1; then \
			unformatted="$$(goimports -l $(GO_FILES))"; \
			if [ -n "$$unformatted" ]; then \
				echo "Go files need goimports:"; \
				echo "$$unformatted"; \
				exit 1; \
			fi; \
		fi; \
	fi

lint:
	@if [ ! -f go.mod ]; then \
		echo "No go.mod; skipping Go lint"; \
	else \
		go vet ./...; \
		if ! command -v golangci-lint >/dev/null 2>&1; then \
			echo "golangci-lint is required when go.mod exists"; \
			exit 1; \
		fi; \
		golangci-lint run ./...; \
	fi

test:
	@if [ ! -f go.mod ]; then \
		echo "No go.mod; skipping Go tests"; \
	else \
		go test ./...; \
	fi

test-e2e:
	@if [ ! -f go.mod ]; then \
		echo "No go.mod; skipping hermetic e2e tests"; \
	else \
		go test -tags=e2e ./...; \
	fi

vuln:
	@if [ ! -f go.mod ]; then \
		echo "No go.mod; skipping Go vulnerability checks"; \
	else \
		go mod verify; \
		if ! command -v govulncheck >/dev/null 2>&1; then \
			echo "govulncheck is required when go.mod exists"; \
			exit 1; \
		fi; \
		govulncheck ./...; \
	fi

security:
	@if ! command -v semgrep >/dev/null 2>&1; then \
		if [ "$${CI:-}" = "true" ]; then \
			echo "semgrep is required in CI"; \
			exit 1; \
		fi; \
		echo "semgrep not installed; skipping local security scan"; \
	else \
		semgrep scan --config auto --error; \
	fi

gate: fmt-check lint test test-e2e vuln security

install-hooks:
	@scripts/install-git-hooks.sh

build:
	@mkdir -p bin
	go build -o bin/ksuite-mail  ./cmd/ksuite-mail
	go build -o bin/ksuite-maild ./cmd/ksuite-maild

