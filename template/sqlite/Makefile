SHELL := bash
.DEFAULT_GOAL := gate

GOVULNCHECK_VERSION ?= v1.9.0
GOBIN := $(or $(shell go env GOBIN),$(firstword $(subst :, ,$(shell go env GOPATH)))/bin)
GOVULNCHECK ?= $(GOBIN)/govulncheck
APP := myapp
RENAME_FILES := .gitignore Makefile Dockerfile compose.yaml config/deploy.yml README.md \
	docs/database.md docs/deploy.md docs/operations.md

.PHONY: help up down logs run build fmt tidy gate fmt-check vet test vulncheck vulncheck-install \
	check-config migrate migrate-status image rename deploy-check deploy
.NOTPARALLEL: gate

gate: fmt-check vet test vulncheck

help:
	@printf '%s\n' \
		'Local:    up down logs run build image' \
		'Checks:   fmt fmt-check vet test tidy vulncheck vulncheck-install gate' \
		'Database: check-config migrate migrate-status' \
		'Deploy:   rename NAME=<app> deploy-check deploy'

up:
	docker compose up --build --detach

down:
	docker compose down

logs:
	docker compose logs --follow server

run:
	go run .

build:
	go build -o $(APP) .

check-config:
	go run . -check-config

migrate:
	go run . -migrate

migrate-status:
	go run . -migrate-status

fmt:
	go fmt ./...

tidy:
	go mod tidy

fmt-check:
	@set -euo pipefail; \
		if git rev-parse --is-inside-work-tree > /dev/null 2>&1; then \
			files=$$(git ls-files -z -- '*.go' | xargs -0 -r gofmt -l); \
		else \
			files=$$(find . -type f -name '*.go' -print0 | xargs -0 -r gofmt -l); \
		fi; \
		if [[ -n "$$files" ]]; then printf '%s\n' "$$files"; exit 1; fi

vet:
	go vet ./...

test:
	@set -euo pipefail; output=$$(mktemp); trap 'rm -f "$$output"' EXIT; \
		status=0; go test -race -count=1 -v ./... | tee "$$output" || status=$$?; \
		printf 'Skipped tests: %s\n' "$$(grep -c -- '--- SKIP:' "$$output" || true)"; \
		exit "$$status"

# The scanner installs itself the first time it is needed, so a clean machine
# needs no setup step. Delete the binary, or run vulncheck-install, to move it
# to another GOVULNCHECK_VERSION.
vulncheck: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...

$(GOVULNCHECK):
	GOBIN='$(dir $@)' go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

vulncheck-install:
	GOBIN='$(dir $(GOVULNCHECK))' go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

image:
	docker build -t $(APP) .

rename: export NAME := $(NAME)
rename:
	@set -euo pipefail; \
		if [[ ! "$$NAME" =~ ^[a-z][a-z0-9]*(-[a-z0-9]+)*$$ || "$$NAME" == *my[a]pp* ]]; then \
			printf '%s\n' 'Set NAME to a new lowercase app name (letters, digits and single hyphens).' >&2; exit 1; \
		fi; \
		for file in $(RENAME_FILES); do \
			if [[ ! -f "$$file" || -e "$$file.bak" || -L "$$file.bak" ]]; then \
				printf 'Missing %s or its .bak already exists.\n' "$$file" >&2; exit 1; \
			fi; \
		done; \
		sed -i.bak "s/my[a]pp/$$NAME/g" $(RENAME_FILES); \
		for file in $(RENAME_FILES); do rm -f "$$file.bak"; done

deploy-check:
	@set -euo pipefail; \
		if grep -Eq 'OWNER|server[.]example|example[.]com|my[a]pp' config/deploy.yml; then \
			printf '%s\n' 'Replace the placeholders in config/deploy.yml before deploying.' >&2; exit 1; \
		elif [[ $$? != 1 ]]; then exit 1; fi; \
		git rev-parse --verify HEAD >/dev/null; \
		git ls-files --error-unmatch -- config/deploy.yml >/dev/null; \
		dirty=$$(git status --porcelain --untracked-files=no --ignore-submodules=none); \
		if [[ -n "$$dirty" ]]; then \
			printf '%s\n' 'Commit tracked changes first: Kamal builds from HEAD.' >&2; exit 1; \
		fi
	$(MAKE) gate

deploy: deploy-check
	kamal deploy

include db.mk
