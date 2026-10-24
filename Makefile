# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

.PHONY: help build run test test-integration test-coverage test-race clean lint fmt vet sec check deps \
        docker-build docker-run docker-stop docker-up migrate web-build web-dev build-cli generate docs-gen docs-check bench bench-load

# Variables
BINARY_NAME=openartifact
MAIN_PATH=./cmd/server
GO=go
DOCKER_IMAGE=openartifact:latest
# docker-build runtime base: dhi (needs `docker login dhi.io`) or distroless
# (anonymous). docker-up reads OPENARTIFACT_RUNTIME from the environment or docker/.env.
RUNTIME?=dhi
# docker-build vulnerability scanners to bundle: none, trivy, grype or all.
SCANNERS?=none
DOCKER_CONTAINER=openartifact

# Build version metadata injected via -ldflags at link time.
# The release version comes from the top-level VERSION file; override with
# CI_VERSION / CI_COMMIT for custom builds. Formats are the same in every
# build path (Makefile, docker/Dockerfile, .goreleaser.yml): COMMIT is the
# 7-character short SHA, BUILD_TIME is RFC 3339 in UTC with second precision.
# Example: make build CI_VERSION=1.2.3 CI_COMMIT=abc1234
VERSION   ?= $(if $(CI_VERSION),$(CI_VERSION),$(shell cat VERSION 2>/dev/null || echo dev))
COMMIT    ?= $(if $(CI_COMMIT),$(CI_COMMIT),$(shell git rev-parse --short=7 HEAD 2>/dev/null || echo unknown))
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   = -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)"

help:
	@echo "Available targets:"
	@echo "  build           - Build the application (includes web)"
	@echo "  build-cli       - Build the CLI tool (bin/oa)"
	@echo "  run             - Build and run the application"
	@echo "  test            - Run unit tests with race detector"
	@echo "  test-integration- Run integration tests"
	@echo "  test-coverage   - Run tests with HTML coverage report"
	@echo "  test-race       - Run tests with race detector only"
	@echo "  bench           - Run Go micro-benchmarks (-benchmem)"
	@echo "  bench-load      - Seed a running instance and run the HTTP load scenarios (bench/run.sh)"
	@echo "  clean           - Remove build artifacts"
	@echo "  lint            - Run golangci-lint"
	@echo "  fmt             - Format code (gofmt + goimports)"
	@echo "  vet             - Run go vet"
	@echo "  sec             - Run gosec security scanner"
	@echo "  check           - Run fmt + vet + lint (pre-commit gate)"
	@echo "  generate        - Run sqlc generate (DB codegen)"
	@echo "  docs-gen        - Regenerate docs/reference (config, metrics, CLI, REST API)"
	@echo "  docs-check      - Fail when docs/reference is out of date"
	@echo "  deps            - Download and tidy dependencies"
	@echo "  docker-build    - Build Docker image"
	@echo "  docker-run      - Run Docker container"
	@echo "  docker-stop     - Stop Docker container"
	@echo "  docker-up       - Build and start the compose stack with version/commit/build time stamped"
	@echo "  migrate         - Run migrations (go run ./cmd/server -migrate)"
	@echo "  help            - Show this help message"

web-build:
	@echo "Building web UI..."
	cd web && bun install && bun run build && bun scripts/check-bundle-size.js

web-dev:
	@echo "Starting web dev server..."
	cd web && bun run dev

build: web-build
	@echo "Building $(BINARY_NAME)..."
	$(GO) build $(LDFLAGS) -o bin/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Build complete: bin/$(BINARY_NAME)"

build-cli:
	@echo "Building CLI tool..."
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o bin/oa ./cmd/oa
	@echo "Build complete: bin/oa"

run: build
	@echo "Running $(BINARY_NAME)..."
	./bin/$(BINARY_NAME)

test:
	@echo "Running tests..."
	$(GO) test -short -race -timeout 5m ./...

test-race:
	@echo "Running tests with race detector..."
	$(GO) test -race -timeout 120s ./...

test-integration:
	@echo "Running integration tests..."
	$(GO) test -tags=integration -race -count=1 ./tests/... ./internal/database/... ./internal/worker/... ./internal/format/gomod/... ./internal/search/... -timeout 10m

test-coverage:
	@echo "Running tests with coverage..."
	$(GO) test -v -race -timeout 10m -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

clean:
	@echo "Cleaning build artifacts..."
	rm -rf bin/
	rm -f coverage.out coverage.html
	$(GO) clean

lint:
	@echo "Running golangci-lint..."
	@which golangci-lint > /dev/null || (echo "golangci-lint not found. Install: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0" && exit 1)
	golangci-lint run ./...

fmt:
	@echo "Formatting code..."
	$(GO) fmt ./...
	@which goimports > /dev/null && goimports -w . || true
	@echo "Code formatted"

vet:
	@echo "Running go vet..."
	$(GO) vet ./...

sec:
	@echo "Running gosec security scanner..."
	@which gosec > /dev/null || (echo "gosec not found. Install: go install github.com/securego/gosec/v2/cmd/gosec@latest" && exit 1)
	gosec -quiet ./...

check: fmt vet lint
	@echo "All checks passed"

generate:
	@echo "Running sqlc generate..."
	$(GO) tool -modfile=tools/go.mod sqlc generate

docs-gen:
	$(GO) run ./cmd/docgen

docs-check:
	$(GO) run ./cmd/docgen -check

bench:
	@echo "Running benchmarks..."
	$(GO) test -bench=. -benchmem -run=^$$ ./...

bench-load:
	@echo "Running HTTP load scenarios (see docs/operations/performance.md)..."
	bench/run.sh

deps:
	@echo "Downloading dependencies..."
	$(GO) mod download
	$(GO) mod tidy
	@echo "Dependencies downloaded"

docker-build:
	@echo "Building Docker image: $(DOCKER_IMAGE)"
	docker build --build-arg RUNTIME=$(RUNTIME) --build-arg SCANNERS=$(SCANNERS) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_TIME=$(BUILD_TIME) -t $(DOCKER_IMAGE) -f docker/Dockerfile .
	@echo "Docker image built: $(DOCKER_IMAGE)"

docker-run: docker-build
	@echo "Running Docker container: $(DOCKER_CONTAINER)"
	@test -f docker/.env || (echo "docker/.env not found — copy docker/.env.example to docker/.env and set real credentials first" && exit 1)
	docker run -d \
		--name $(DOCKER_CONTAINER) \
		-p 8080:8080 \
		--add-host=host.docker.internal:host-gateway \
		-v openartifact_data:/var/lib/artifacts \
		-v openartifact_backups:/var/lib/openartifact/backups \
		--env-file docker/.env \
		-e SERVER_HOST=0.0.0.0 \
		-e SERVER_PORT=8080 \
		-e STORAGE_BACKEND=filesystem \
		-e STORAGE_PATH=/var/lib/artifacts \
		-e LOG_LEVEL=info \
		$(DOCKER_IMAGE)
	@echo "Container running: $(DOCKER_CONTAINER)"

# docker compose cannot read the VERSION file or run git, so the build metadata
# is handed over through the OPENARTIFACT_* variables docker-compose.yml expects.
docker-up:
	@test -f docker/.env || (echo "docker/.env not found — copy docker/.env.example to docker/.env and set real credentials first" && exit 1)
	OPENARTIFACT_VERSION=$(VERSION) OPENARTIFACT_COMMIT=$(COMMIT) OPENARTIFACT_BUILD_TIME=$(BUILD_TIME) \
		docker compose -f docker/docker-compose.yml --env-file docker/.env up -d --build

docker-stop:
	@echo "Stopping Docker container: $(DOCKER_CONTAINER)"
	docker stop $(DOCKER_CONTAINER) || true
	docker rm $(DOCKER_CONTAINER) || true
	@echo "Container stopped"

migrate:
	@echo "Running database migrations via 'go run $(MAIN_PATH) -migrate'..."
	@echo "(the server also auto-migrates on normal startup unless MIGRATE_ON_START=false)"
	$(GO) run $(MAIN_PATH) -migrate

.DEFAULT_GOAL := help
