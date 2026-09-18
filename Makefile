# Makefile — works on Mac and Linux.
# Windows users: use `task <target>` (Taskfile.yml) or `go run ./scripts/<script>`.
# Both tools produce identical results.
#
# Run `make help` for a list of targets.

BINARY    := overcast
BUILD_DIR := ./bin
GO        := go
# Where the lambda-init target writes the in-container Lambda init, and where
# internal/services/lambda/initbin embeds it from. The two binaries are build
# output and gitignored; the committed dist/.gitkeep beside them is what keeps
# the embed pattern resolving in a bare checkout. See AGENTS.md.
LAMBDA_INIT_DIR := internal/services/lambda/initbin/dist
AWS_MODELS_REVISION ?= $(shell sed -n 's/^revision=//p' models/aws/VERSION)
GOFLAGS   := -trimpath
VERSION   := $(shell cat VERSION)
LDFLAGS   := -w -s -X main.version=$(VERSION)
# renovate: datasource=github-releases depName=rhysd/actionlint
ACTIONLINT_VERSION := v1.7.12
# golangci-lint v2.x — v2 config schema (.golangci.yml declares `version: "2"`).
# The v2.10.0+ toolchain floor that used to hold this at v2.8.0 was cleared when
# go.mod moved to Go 1.25 (#1213), so a bump that now fails Lint is reporting
# real findings, not a toolchain mismatch — read them before assuming the bump
# is premature. The `# renovate:` lines are read by the custom manager in
# .github/renovate.json5; CONTRIBUTING points here rather than repeating the
# numbers.
# renovate: datasource=github-releases depName=golangci/golangci-lint
GOLANGCI_LINT_VERSION := v2.13.1

# Docker image names. The tag defaults to the sanitised current branch name so
# that parallel worktrees do not build into one tag and silently run each
# other's image; scripts/image-tag.sh derives it and documents what a slash,
# uppercase and a detached HEAD each become. `?=` throughout, so any of the
# three can be overridden:
#
#   make docker-console IMAGE_TAG=scratch
#   make docker-console CONSOLE_IMAGE=overcast:whatever
#
# `?=` defines a recursively-expanded variable, so the `$(shell ...)` is deferred
# until an image name is actually used and every other target costs nothing.
# Built images are not free: `make docker-clean` removes this branch's pair.
IMAGE_TAG     ?= $(shell sh scripts/image-tag.sh)
CONSOLE_IMAGE ?= overcast:$(IMAGE_TAG)
SLIM_IMAGE    ?= overcast-slim:$(IMAGE_TAG)

.PHONY: help setup build build-web build-slim build-cross lambda-init \
        build-linux-amd64 build-linux-arm64 \
        build-darwin-amd64 build-darwin-arm64 \
        build-windows-amd64 \
        build-slim-linux-amd64 build-slim-linux-arm64 \
        build-slim-darwin-amd64 build-slim-darwin-arm64 \
        build-slim-windows-amd64 \
        run test test-unit test-integration test-coverage \
        ci-local ci-local-web ci-local-go \
        bench bench-startup lint lint-go lint-web lint-actions lint-encoding fmt vet tidy check verify aws-models-check docker docker-slim docker-console docker-run docker-clean docker-clean-test-networks clean \
        compat compat-build compat-serve compat-dev compat-docker compat-report compat-registry-check \
        generate-caps check-caps generate-ts check-ts generate-aws-operations aws-models-check aws-models-check-ci generate-compat-model compat-model-check docs docs-lint docs-check supportmeta-check check-binary-symbols

## help: print this help message
help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "Windows: use 'task <target>' (Taskfile.yml) — all targets are equivalent."
	@echo "Any platform: use 'go run ./scripts/<script>' for zero-install scripts."
	@echo ""
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'

## setup: verify all dev tools are installed (cross-platform)
setup:
	$(GO) run ./scripts/check-tools.go

## lambda-init: build the in-container Lambda init for both Linux architectures
# Every overcast binary embeds both: a function's Architectures picks the one
# copied into its container, and an arm64 function runs under emulation on an
# amd64 host, so neither can be assumed away. The build targets depend on this
# rather than leaving it as a step to remember; it is a few seconds of pure Go.
lambda-init:
	@mkdir -p $(LAMBDA_INIT_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "-s -w" -o $(LAMBDA_INIT_DIR)/lambda-init-linux-amd64 ./cmd/lambda-init
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "-s -w" -o $(LAMBDA_INIT_DIR)/lambda-init-linux-arm64 ./cmd/lambda-init

## build: compile the overcast binary for the current platform (includes embedded web UI)
build: lambda-init
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/overcast

## build-web: build the web UI (run before build if assets are stale)
build-web:
	cd web && VITE_BUNDLED=true pnpm run build

## build-slim: compile the slim binary (no web UI, no SQLite) for the current platform
# -tags slim does NOT drop the Lambda init: slim images run Lambda.
build-slim: lambda-init
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -tags slim,nosqlite -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/overcastd ./cmd/overcast

## build-cross: compile release binaries for all supported platforms
build-cross: lambda-init build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 build-windows-amd64 \
             build-slim-linux-amd64 build-slim-linux-arm64 build-slim-darwin-amd64 build-slim-darwin-arm64 build-slim-windows-amd64

## build-linux-amd64: compile overcast for Linux x86-64
build-linux-amd64: lambda-init
	@mkdir -p $(BUILD_DIR)
	GOOS=linux  GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-amd64        ./cmd/overcast

## build-linux-arm64: compile overcast for Linux ARM64 (Raspberry Pi, AWS Graviton, etc.)
build-linux-arm64: lambda-init
	@mkdir -p $(BUILD_DIR)
	GOOS=linux  GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-arm64        ./cmd/overcast

## build-darwin-amd64: compile overcast for macOS Intel
build-darwin-amd64: lambda-init
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-darwin-amd64       ./cmd/overcast

## build-darwin-arm64: compile overcast for macOS Apple Silicon
build-darwin-arm64: lambda-init
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-darwin-arm64       ./cmd/overcast

## build-windows-amd64: compile overcast for Windows x86-64 (console .exe)
build-windows-amd64: lambda-init
	@mkdir -p $(BUILD_DIR)
	GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-windows-amd64.exe ./cmd/overcast

## build-slim-linux-amd64: compile slim overcastd for Linux x86-64
build-slim-linux-amd64: lambda-init
	@mkdir -p $(BUILD_DIR)
	GOOS=linux  GOARCH=amd64 $(GO) build $(GOFLAGS) -tags slim,nosqlite -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/overcastd-linux-amd64        ./cmd/overcast

## build-slim-linux-arm64: compile slim overcastd for Linux ARM64
build-slim-linux-arm64: lambda-init
	@mkdir -p $(BUILD_DIR)
	GOOS=linux  GOARCH=arm64 $(GO) build $(GOFLAGS) -tags slim,nosqlite -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/overcastd-linux-arm64        ./cmd/overcast

## build-slim-darwin-amd64: compile slim overcastd for macOS Intel
build-slim-darwin-amd64: lambda-init
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 $(GO) build $(GOFLAGS) -tags slim,nosqlite -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/overcastd-darwin-amd64       ./cmd/overcast

## build-slim-darwin-arm64: compile slim overcastd for macOS Apple Silicon
build-slim-darwin-arm64: lambda-init
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=arm64 $(GO) build $(GOFLAGS) -tags slim,nosqlite -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/overcastd-darwin-arm64       ./cmd/overcast

## build-slim-windows-amd64: compile slim overcastd for Windows x86-64
build-slim-windows-amd64: lambda-init
	@mkdir -p $(BUILD_DIR)
	GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -tags slim,nosqlite -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/overcastd-windows-amd64.exe ./cmd/overcast

## run: build and run with dev defaults (uses cross-platform Go script)
run:
	$(GO) run ./scripts/run.go

## dev-server: watch Go sources and hot-reload the server (requires air)
dev-server:
	air

## test: run all tests (unit + integration, with race detector where supported)
test:
	$(GO) test -race -count=1 -timeout=1200s ./...

## test-unit: run unit tests only (fast — no server startup)
test-unit:
	$(GO) test -race -count=1 -timeout=900s ./internal/...

## test-integration: run integration tests only
test-integration:
	$(GO) test -race -count=1 -timeout=600s ./tests/...

## test-coverage: run tests and generate HTML coverage report
test-coverage:
	$(GO) test -race -count=1 -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report written to coverage.html"

## bench: run benchmarks with memory allocation reporting
bench:
	$(GO) test -bench=. -benchmem -count=3 ./...

## bench-startup: measure cold-start time across all storage backends (pre-release gate)
bench-startup:
	$(GO) run ./scripts/bench-startup.go

## lint: run all linters (Go/emulation, web UI, GitHub Actions)
lint: lint-go lint-web lint-actions lint-encoding lint-todos

## lint-encoding: scan tracked text files for mojibake, stray BOMs, and U+FFFD (see scripts/check_encoding.py)
lint-encoding:
	python3 scripts/check_encoding.py

## lint-todos: scan comments for markers the TODO-to-issue Action would misfile (see scripts/check_todos.py)
lint-todos:
	python3 scripts/check_todos.py

## lint-go: run golangci-lint for Go/emulation code (pinned via go run — no install needed)
lint-go:
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...
	cd testcontainers/go && $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...
	@bash scripts/verify-changed.sh --record go

## lint-web: run web UI linting (oxlint — web/.oxlintrc.json is the whole gate)
lint-web:
	cd web && pnpm run lint

## lint-actions: lint GitHub Actions workflows
lint-actions:
	$(GO) run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION) .github/workflows/*.yml

## fmt: format all Go source files
fmt:
	$(GO) fmt ./...

## vet: run go vet
vet:
	$(GO) vet ./...

## tidy: tidy and verify go.mod / go.sum
tidy:
	$(GO) mod tidy
	$(GO) mod verify

## check: run all pre-PR checks (fmt + vet + lint + test)
check: fmt vet lint test

## verify: run the required-CI checks this branch touches (cached — a no-op if nothing changed)
verify:
	bash scripts/verify-changed.sh $(ARGS)

## ci-local: run the whole CI pipeline locally (Go via Docker — no host Go needed)
ci-local:
	bash scripts/ci-local.sh $(ARGS)

## ci-local-web: web stages only (lint, typecheck, vitest, build)
ci-local-web:
	bash scripts/ci-local.sh --web-only

## ci-local-go: Go stages only (docs index, vet, build, unit tests)
ci-local-go:
	bash scripts/ci-local.sh --go-only

## generate-caps: generate internal/capabilities/all.gen.go from per-service capabilities_dev.go files
generate-caps:
	$(GO) run -tags dev ./cmd/capgen --generate

## check-caps: verify capabilities declarations match handler operation registrations
check-caps:
	$(GO) run -tags dev ./cmd/capgen --check

## generate-ts: regenerate web/src/types/api.gen.ts from the Go response structs the web UI consumes
generate-ts:
	$(GO) run ./cmd/tsgen --write

## check-ts: verify web/src/types/api.gen.ts matches the Go response structs (CI gate; fails with "run make generate-ts")
check-ts:
	$(GO) run ./cmd/tsgen --check

## generate-ddb-reserved-words: refresh internal/services/dynamodb/reserved_words.txt from the AWS DynamoDB Developer Guide
## Needs network access, so no CI job runs it. Refresh by hand and review the diff.
generate-ddb-reserved-words:
	$(GO) run ./scripts/dynamodb-reserved-words.go

## generate-aws-operations: regenerate the AWS operation manifest, pruned shape snapshot and runtime SDK shape tables from a pinned local api-models-aws checkout
## Set AWS_MODELS_DIR to its models directory. The checked-out commit must match models/aws/VERSION.
generate-aws-operations:
	@test -n "$(AWS_MODELS_DIR)" || (echo "ERROR: set AWS_MODELS_DIR to api-models-aws/models" && exit 1)
	@test -n "$(AWS_MODELS_REVISION)" || (echo "ERROR: set AWS_MODELS_REVISION to the api-models-aws commit" && exit 1)
	$(GO) run ./cmd/awsmodelgen -models "$(AWS_MODELS_DIR)" -output internal/awsapi/manifest.gen.go -source-revision "$(AWS_MODELS_REVISION)" \
		-shapes-out models/aws/shapes -shapes-services models/aws/shapes-services.txt -sdk-shapes-out internal/awsshapes -sdk-shapes-services models/aws/sdk-shapes-services.txt

## aws-models-check-ci: CI-only subset of aws-models-check
## `./cmd/awsmodelgen ./internal/awsapi ./internal/protocol/codec ./tests/integration/router` are inside `./...` and
## already run by the `Full test suite with coverage` job, so the `aws-operation-coverage` CI job runs this narrower
## target instead of the full aws-models-check and skips re-running ~60s of tests another job already ran. Nothing
## else compiles the `dev` tag set below, so it is not duplicated and must stay.
aws-models-check-ci:
	$(GO) test -count=1 -tags dev ./internal/router ./cmd/capgen
	$(GO) run -tags dev ./cmd/capgen --check-model

## aws-models-check: validate the committed AWS operation corpus, generated runtime ownership indexes and pruned shape snapshot
## Without AWS_MODELS_DIR this is the offline gate: the Go tests verify the committed manifest and snapshot against the
## manifest-sha256/shapes-sha256 digests in models/aws/VERSION, and hold the snapshot to its reviewed size budget.
## Set AWS_MODELS_DIR to additionally prove both artifacts match that pinned checkout byte-for-byte.
## This is the full, developer-facing target. CI's aws-operation-coverage job runs aws-models-check-ci instead (see
## above) because the first line below duplicates packages that job's sibling coverage job already tests.
aws-models-check: aws-models-check-ci
	$(GO) test -count=1 ./cmd/awsmodelgen ./internal/awsapi ./internal/awsshapes ./internal/protocol/codec ./tests/integration/router
	@if [ -n "$(AWS_MODELS_DIR)" ]; then \
		$(GO) run ./cmd/awsmodelgen -models "$(AWS_MODELS_DIR)" -output internal/awsapi/manifest.gen.go -source-revision "$(AWS_MODELS_REVISION)" \
			-shapes-out models/aws/shapes -shapes-services models/aws/shapes-services.txt -sdk-shapes-out internal/awsshapes -sdk-shapes-services models/aws/sdk-shapes-services.txt -check; \
	fi

## generate-compat-model: regenerate the compat scenario IR, gaps.json and registry.generated.json from compat/model/recipes/
## Offline: reads the committed shape snapshot (models/aws/shapes/) and the capability table, never the raw AWS models.
generate-compat-model:
	$(GO) run -tags dev ./cmd/compatgen

## compat-model-check: prove the committed compat model is byte-identical to what the generator produces (CI gate)
## Runs the generator's own tests (the fixture suite plus the committed-corpus sync and schema checks) and then
## the -check mode, which regenerates in memory and diffs without writing.
compat-model-check:
	$(GO) test -count=1 -tags dev ./cmd/compatgen
	$(GO) run -tags dev ./cmd/compatgen -check

## docs-lint: check docs frontmatter, anchors, service page structure, the length budget and house-style tells
# There is nothing to regenerate: the console derives its docs navigation and
# search index from the docs the binary embeds (internal/docsindex), so editing
# docs/ needs no follow-up command. This is the gate on the docs themselves.
docs-lint:
	$(GO) run ./scripts/docs-index.go --check

## docs: regenerate sentinel-bracketed capability tables in docs/services/*.md
docs: generate-caps
	$(GO) run -tags dev ./cmd/capgen --write-docs

## docs-check: verify docs capability tables, all.gen.go, api.gen.ts, and STATUS.md are up to date, and every doc has frontmatter (CI gate)
docs-check: check-caps check-ts docs-lint
	$(GO) run -tags dev ./cmd/capgen --generate
	@git diff --exit-code internal/capabilities/all.gen.go \
		|| (echo "ERROR: internal/capabilities/all.gen.go is stale. Run: make generate-caps" && exit 1)
	$(GO) run -tags dev ./cmd/capgen --write-docs
	@git diff --exit-code README.md STATUS.md docs/README.md docs/configuration.md docs/cdk.md docs/services/ docs/generated/service-support.json \
		|| (echo "ERROR: README.md, STATUS.md, docs/README.md, docs/configuration.md, docs/cdk.md, docs/services/, or docs/generated/service-support.json are stale. Run: make docs" && exit 1)

## supportmeta-check: alias for docs-check (manifest schema, registry parity, docs parity, generated artifacts)
supportmeta-check: docs-check

## check-binary-symbols: verify no capability symbols leak into the production binary
check-binary-symbols:
	bash scripts/check-binary-symbols.sh

## docker: build both Docker images (console + slim)
docker: docker-console docker-slim

## docker-console: build the Docker image with web console (default target)
docker-console:
	docker build -t $(CONSOLE_IMAGE) .

## docker-slim: build the slim Docker image (no web UI, no SQLite, for CI)
docker-slim:
	docker build --target slim -t $(SLIM_IMAGE) .

## docker-run: run the Docker image with debug logging (mounts Docker socket for Lambda)
docker-run:
	docker run --rm -p 4566:4566 \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-e OVERCAST_LOG_LEVEL=debug $(CONSOLE_IMAGE)

## docker-clean: remove this branch's images (run this when you are done with them)
docker-clean:
	-docker image rm $(CONSOLE_IMAGE) $(SLIM_IMAGE)

## docker-clean-test-networks: remove the overcast_*_test_* networks a killed test run left behind
# Only per-test networks (overcast_<suite>_test_<id> and its _control twin) with
# no container attached and older than 15 minutes; never overcast, overcast_control
# or any other instance's planes. `-dry-run` lists, `-min-age 0` includes younger ones.
docker-clean-test-networks:
	$(GO) run ./scripts/docker-clean-test-networks.go

## clean: remove build artefacts
clean:
	$(GO) clean ./...
	@rm -rf $(BUILD_DIR) coverage.out coverage.html
	@rm -f $(LAMBDA_INIT_DIR)/lambda-init-linux-amd64 $(LAMBDA_INIT_DIR)/lambda-init-linux-arm64

# ---- Compat dashboard -------------------------------------------------------
# Every target manages its own throwaway Overcast instance on free ports —
# 4566/4567 stay yours. Pass ARGS='--endpoint http://localhost:4566' to target
# an instance you are already running.

## compat: run the compat suites headlessly (starts its own Overcast instance)
compat:
	$(GO) run ./cmd/compat $(ARGS)

## compat-build: build the compat UI and embed it into the compat binary
compat-build:
	@echo "Building compat UI…"
	cd compat/ui && npm run build
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/compat ./cmd/compat

## compat-serve: start the compat dashboard with a freshly built UI
compat-serve:
	$(GO) run ./cmd/compat --serve --interactive --build-ui --open $(ARGS)

## compat-dev: start the compat dashboard with a hot-reloading UI
compat-dev:
	$(GO) run ./cmd/compat --dev $(ARGS)

## compat-docker: run the compat suites entirely in containers (no host Go or Node)
compat-docker:
	docker compose -f compat/docker-compose.yml run --rm compat

## compat-report: print an agent-friendly summary of the last compat run (reads compat-results.json)
compat-report:
	$(GO) run ./cmd/compat --report

## compat-registry-check: validate compat/suites/registry.json against registry.schema.json, and lint suite name hygiene
compat-registry-check:
	python3 scripts/validate-compat-registry.py
	python3 scripts/compat-name-hygiene.py

# ---- Container-based development (cross-platform) --------------------------
# These targets work identically on Mac, Linux, and Windows.
# They require Docker but not a local Go installation.

# These go through scripts/container-test.sh rather than calling docker compose
# directly: it derives the container's CPU cap from the host and hands it to the
# Compose file, which cannot compute one itself. See that script's header.

## container-test: run all tests inside a Linux container (cross-platform)
container-test:
	bash scripts/container-test.sh

## container-test-unit: run unit tests inside a container
container-test-unit:
	bash scripts/container-test.sh -race -count=1 -timeout=900s ./internal/...

## container-test-integration: run integration tests inside a container
container-test-integration:
	bash scripts/container-test.sh -race -count=1 -timeout=600s ./tests/...

## dev: start the development server with source mounted (rebuilds on --build)
dev:
	docker compose -f docker-compose.dev.yml up overcast

## dev-build: force rebuild of the dev server
dev-build:
	docker compose -f docker-compose.dev.yml up --build overcast
