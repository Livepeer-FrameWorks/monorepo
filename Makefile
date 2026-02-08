.PHONY: build build-images build-bin-commodore build-bin-quartermaster build-bin-purser build-bin-decklog build-bin-foghorn build-bin-helmsman build-bin-periscope-ingest build-bin-periscope-query build-bin-signalman build-bin-bridge build-bin-deckhand build-bin-forms build-bin-skipper \
	build-image-commodore build-image-quartermaster build-image-purser build-image-decklog build-image-foghorn build-image-helmsman build-image-periscope-ingest build-image-periscope-query build-image-signalman build-image-bridge build-image-deckhand build-image-skipper \
	proto graphql graphql-frontend graphql-all clean version install-tools verify test coverage env tidy fmt \
	lint lint-all lint-fix lint-report lint-analyze \
	dead-code-install dead-code-go dead-code-ts dead-code-report dead-code

# Version information
# Prefer annotated git tags like v1.2.3; fallback to describe or dev
VERSION ?= $(shell git describe --tags --match "v[0-9]*" --exact-match 2>/dev/null || git describe --tags --match "v[0-9]*" --dirty --always 2>/dev/null || echo "0.0.0-dev")
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

# Build flags for version injection (matching pkg/version/version.go variable names)
LDFLAGS = -ldflags "-X frameworks/pkg/version.Version=$(VERSION) \
					-X frameworks/pkg/version.GitCommit=$(GIT_COMMIT) \
					-X frameworks/pkg/version.BuildDate=$(BUILD_DATE)"

# All microservices (only services with actual binaries)
SERVICES = commodore quartermaster purser decklog foghorn helmsman periscope-ingest periscope-query signalman bridge navigator privateer deckhand skipper

# All Go modules (including pkg for testing)
GO_SERVICES = $(shell find . -name "go.mod" -exec dirname {} \;)

# Generate proto files first
proto:
	cd pkg/proto && make proto

# Generate GraphQL files (backend)
graphql:
	cd api_gateway && make graphql

# Generate GraphQL files (frontend)
graphql-frontend:
	cd website_application && pnpm run gql:codegen

# Generate GraphQL files (backend + frontend)
graphql-all: graphql graphql-frontend

# Build all service binaries
build: proto graphql
	@echo "Building service binaries with version: $(VERSION)"
	@mkdir -p bin
	@for service in $(SERVICES); do \
		echo "Building $$service..."; \
		$(MAKE) build-bin-$$service; \
	done

# Verify (tidy, fmt, vet, test, build) all Go modules and build images when present
verify: proto graphql
	@echo "Verifying all Go modules (fmt/vet/test/build + images)..."
	@failed=0; \
	for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && \
			go mod tidy && \
			go fmt ./... && \
			go vet $$(go list ./... | grep -v '/graph/generated') && \
			go test ./... -race -count=1 && \
			go build ./...) || failed=1; \
		if [ -f "$$service_dir/Dockerfile" ]; then \
			echo "Building Docker image for $$service_name..."; \
			docker build -t frameworks-$$service_name:test -f $$service_dir/Dockerfile . || failed=1; \
		fi; \
	done; \
	if [ $$failed -eq 0 ]; then \
		echo "✓ Verification passed"; \
	else \
		echo "✗ Verification failed"; \
		exit 1; \
	fi

# Build all Docker images
build-images: proto graphql
	@echo "Building Docker images for all services..."
	@for service in $(SERVICES); do \
		$(MAKE) build-image-$$service 2>/dev/null || echo "Skipping $$service (no Dockerfile)"; \
	done

# Individual Docker builds
build-image-commodore: proto
	docker build -t frameworks-commodore:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_control/Dockerfile .

build-image-quartermaster: proto
	docker build -t frameworks-quartermaster:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_tenants/Dockerfile .

build-image-purser: proto
	docker build -t frameworks-purser:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_billing/Dockerfile .

build-image-decklog: proto
	docker build -t frameworks-decklog:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_firehose/Dockerfile .

build-image-foghorn: proto
	docker build -t frameworks-foghorn:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_balancing/Dockerfile .

build-image-helmsman: proto
	docker build -t frameworks-helmsman:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_sidecar/Dockerfile .

build-image-periscope-ingest: proto
	docker build -t frameworks-periscope-ingest:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_analytics_ingest/Dockerfile .

build-image-periscope-query: proto
	docker build -t frameworks-periscope-query:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_analytics_query/Dockerfile .

build-image-signalman: proto
	docker build -t frameworks-signalman:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_realtime/Dockerfile .

build-image-bridge: proto
	docker build -t frameworks-bridge:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_gateway/Dockerfile .

build-image-logbook:
	docker build -t frameworks-logbook:$(VERSION) \
		--build-arg BUILD_ENV=production \
		-f website_docs/Dockerfile .

build-image-navigator: proto
	docker build -t frameworks-navigator:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_dns/Dockerfile .

build-image-deckhand: proto
	docker build -t frameworks-deckhand:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_ticketing/Dockerfile .

build-image-skipper:
	docker build -t frameworks-skipper:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_consultant/Dockerfile .

# Individual service bin builds (explicit)
build-bin-commodore: proto
	cd api_control && go build $(LDFLAGS) -o ../bin/commodore cmd/commodore/main.go

build-bin-quartermaster: proto
	cd api_tenants && go build $(LDFLAGS) -o ../bin/quartermaster cmd/quartermaster/main.go

build-bin-purser: proto
	cd api_billing && go build $(LDFLAGS) -o ../bin/purser cmd/purser/main.go

build-bin-decklog: proto
	cd api_firehose && go build $(LDFLAGS) -o ../bin/decklog cmd/decklog/main.go

build-bin-foghorn: proto
	cd api_balancing && go build $(LDFLAGS) -o ../bin/foghorn cmd/foghorn/main.go

build-bin-helmsman: proto
	cd api_sidecar && go build $(LDFLAGS) -o ../bin/helmsman cmd/helmsman/main.go

build-bin-periscope-ingest: proto
	cd api_analytics_ingest && go build $(LDFLAGS) -o ../bin/periscope-ingest cmd/periscope/main.go

build-bin-periscope-query: proto
	cd api_analytics_query && go build $(LDFLAGS) -o ../bin/periscope-query cmd/periscope/main.go

build-bin-signalman: proto
	cd api_realtime && go build $(LDFLAGS) -o ../bin/signalman cmd/signalman/main.go

build-bin-bridge: proto
	cd api_gateway && go build $(LDFLAGS) -o ../bin/bridge cmd/bridge/main.go

build-bin-navigator: proto
	cd api_dns && go build $(LDFLAGS) -o ../bin/navigator cmd/navigator/main.go

build-bin-privateer: proto
	cd api_mesh && go build $(LDFLAGS) -o ../bin/privateer cmd/privateer/main.go

build-bin-deckhand: proto
	cd api_ticketing && go build $(LDFLAGS) -o ../bin/deckhand cmd/deckhand/main.go

build-bin-forms: proto
	cd api_forms && go build $(LDFLAGS) -o ../bin/forms cmd/forms/main.go

build-bin-skipper: proto
	cd api_consultant && go build $(LDFLAGS) -o ../bin/skipper cmd/skipper/main.go


# Clean build artifacts
clean:
	rm -rf bin/
	cd pkg/proto && make clean

# Show version information
version:
	@echo "Version: $(VERSION)"
	@echo "Git Commit: $(GIT_COMMIT)"
	@echo "Build Date: $(BUILD_DATE)"

# Install required development tools
install-tools:
	cd pkg/proto && make install-tools
	cd api_gateway && make install-tools
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

# Run unit tests in every Go module
test: proto graphql
	@echo "Running unit tests for all Go modules..."
	@failed=0; \
	for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && \
			go mod tidy && \
			go test ./... -race -count=1) || failed=1; \
	done; \
	if [ $$failed -eq 0 ]; then \
		echo "✓ Unit tests passed"; \
	else \
		echo "✗ Unit tests failed"; \
		exit 1; \
	fi

test-api-sidecar:
	@echo "Running api_sidecar unit tests..."
	@cd api_sidecar && go test ./... -count=1

# Run unit tests with JUnit XML output for Codecov Test Analytics
test-junit: proto graphql
	@echo "Running unit tests with JUnit output for all Go modules..."
	@mkdir -p $(CURDIR)/test-results
	@rm -f $(CURDIR)/test-results/go-junit.xml
	go install github.com/jstemmer/go-junit-report/v2@latest
	@failed=0; \
	for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && \
			go mod tidy && \
			go test ./... -race -count=1 -v 2>&1) | go-junit-report -set-exit-code >> $(CURDIR)/test-results/go-junit.xml || failed=1; \
	done; \
	if [ $$failed -eq 0 ]; then \
		echo "✓ Unit tests passed"; \
	else \
		echo "✗ Unit tests failed"; \
		exit 1; \
	fi
	@echo "JUnit report saved to $(CURDIR)/test-results/go-junit.xml"

# Generate a single combined coverage report at ./coverage
coverage: proto graphql
	@echo "Generating combined coverage for all Go modules..."
	@rm -rf $(CURDIR)/coverage && mkdir -p $(CURDIR)/coverage
	@echo "mode: atomic" > $(CURDIR)/coverage/coverage.out
	@for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		( cd $$service_dir && \
			go mod tidy >/dev/null 2>&1 && \
			tmpfile=$$(mktemp); \
			if go test ./... -coverpkg=./... -coverprofile="$$tmpfile" -covermode=atomic -count=1 >/dev/null 2>&1; then \
				if [ -s "$$tmpfile" ]; then \
					tail -n +2 "$$tmpfile" >> "$(CURDIR)/coverage/coverage.out"; \
					cov=$$(go tool cover -func="$$tmpfile" | awk '/total:/ {print $$3}'); \
					echo "   coverage: $$cov"; \
				else \
					echo "   no coverage data"; \
				fi; \
			else \
				echo "   tests failed, skipping"; \
			fi; \
		rm -f "$$tmpfile" ); \
	done;
	@# Filter out generated code from coverage
	@if [ -f "$(CURDIR)/coverage/coverage.out" ]; then \
		grep -v '\.pb\.go:' "$(CURDIR)/coverage/coverage.out" | \
			grep -v '_grpc\.pb\.go:' | \
			grep -v 'graph/generated/' | \
			grep -v 'graph/model/models_gen\.go:' > "$(CURDIR)/coverage/coverage.filtered.out" && \
			mv "$(CURDIR)/coverage/coverage.filtered.out" "$(CURDIR)/coverage/coverage.out"; \
		echo "Filtered generated code from coverage report"; \
	fi
	@echo "Combined coverage saved to $(CURDIR)/coverage/coverage.out"

env:
	@echo "Generating .env from config/env/*.env..."
	@cd scripts/env && GOCACHE=$$(pwd)/.gocache go run .

# Tidy all Go modules
tidy:
	@echo "Running go mod tidy for all Go modules..."
	@for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && go mod tidy); \
	done
	@echo "✓ All modules tidied"

# Format all Go code
fmt:
	@echo "Running go fmt for all Go modules..."
	@for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && go fmt ./...); \
	done
	@echo "✓ All modules formatted"

# =============================================================================
# Linting
# =============================================================================

# Run golangci-lint with baseline (matches CI - shows only NEW violations)
lint:
	@echo "Running golangci-lint with baseline (CI mode)..."
	@BASELINE=$$(cat .golangci-baseline 2>/dev/null || echo ""); \
	if [ -z "$$BASELINE" ]; then \
		echo "Warning: No .golangci-baseline file found, running without baseline"; \
		BASELINE_ARG=""; \
	else \
		echo "Using baseline: $$BASELINE"; \
		BASELINE_ARG="--new-from-rev=$$BASELINE"; \
	fi; \
	failed=0; \
	for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> Linting $$service_name"; \
		(cd $$service_dir && golangci-lint run --timeout=5m $$BASELINE_ARG ./...) || failed=1; \
	done; \
	if [ $$failed -eq 1 ]; then exit 1; fi

# Run golangci-lint without baseline (shows ALL violations for cleanup work)
lint-all:
	@echo "Running golangci-lint for all Go modules (all violations)..."
	@for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && golangci-lint run --timeout=5m ./...); \
	done

# Run golangci-lint with auto-fix
lint-fix:
	@echo "Running golangci-lint with auto-fix for all Go modules..."
	@for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && golangci-lint run --fix --timeout=5m ./...); \
	done

# Generate lint report to file
lint-report:
	@./scripts/lint-report.sh

# Analyze lint report
lint-analyze:
	@./scripts/lint-analyze.sh

# =============================================================================
# Dead Code Analysis
# =============================================================================

REPORTS_DIR := reports

# Install dead code analysis tools
dead-code-install:
	@echo "Installing Go dead code analysis tools..."
	go install golang.org/x/tools/cmd/deadcode@latest
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	@echo ""
	@echo "✓ Go dead code analysis tools installed"
	@echo "Note: knip must be installed separately (workspace dev dependency)."

# Run Go dead code analysis
dead-code-go:
	@mkdir -p $(REPORTS_DIR)
	@echo "=== Go Dead Code Analysis ==="
	@echo ""
	@echo "--- Running deadcode (unreachable functions) ---"
	@./scripts/deadcode-analysis.sh
	@echo ""
	@echo "--- Running staticcheck U1000 (unused identifiers) ---"
	@for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "Analyzing $$service_name..."; \
		if ! command -v staticcheck >/dev/null 2>&1; then \
			echo "  WARNING: staticcheck not found; skipping."; \
			echo "# ERROR: staticcheck not found; skipping." > $(REPORTS_DIR)/staticcheck-$$service_name.txt; \
			continue; \
		fi; \
		tmpfile=$$(mktemp); \
		( cd $$service_dir && staticcheck -checks="U1000" ./... > $$tmpfile 2>&1 ); \
		status=$$?; \
		grep -v '\.pb\.go:' $$tmpfile | \
			grep -v '_grpc\.pb\.go:' | \
			grep -v 'graph/generated/' \
			> $(REPORTS_DIR)/staticcheck-$$service_name.txt || true; \
		rm -f $$tmpfile; \
		if [ $$status -gt 1 ]; then \
			echo "  WARNING: staticcheck failed (exit $$status)"; \
			echo "# ERROR: staticcheck failed (exit $$status)" >> $(REPORTS_DIR)/staticcheck-$$service_name.txt; \
		fi; \
		count=$$(wc -l < $(REPORTS_DIR)/staticcheck-$$service_name.txt | tr -d ' '); \
		if [ "$$count" -gt 0 ]; then \
			echo "  Found $$count issues"; \
		else \
			echo "  No issues"; \
		fi; \
	done
	@echo ""
	@echo "Go reports saved to $(REPORTS_DIR)/"

# Run TypeScript dead code analysis
dead-code-ts:
	@mkdir -p $(REPORTS_DIR)
	@echo "=== TypeScript Dead Code Analysis ==="
	@echo ""
	@echo "--- Running knip (comprehensive unused code finder) ---"
	@if ! command -v pnpm >/dev/null 2>&1; then \
		echo "WARNING: pnpm not found; skipping knip." ; \
		echo "# ERROR: pnpm not found; skipping knip." > $(REPORTS_DIR)/knip-report.txt; \
	elif ! pnpm exec knip --version >/dev/null 2>&1; then \
		echo "WARNING: knip not installed; skipping knip." ; \
		echo "# ERROR: knip not installed; skipping knip." > $(REPORTS_DIR)/knip-report.txt; \
	else \
		tmpjson=$$(mktemp); \
		tmptxt=$$(mktemp); \
		pnpm exec knip --config knip.json --reporter json > $$tmpjson 2>&1; \
		status=$$?; \
		cat $$tmpjson > $(REPORTS_DIR)/knip-report.json; \
		pnpm exec knip --config knip.json > $$tmptxt 2>&1 || true; \
		cat $$tmptxt > $(REPORTS_DIR)/knip-report.txt; \
		rm -f $$tmpjson $$tmptxt; \
		if [ $$status -gt 1 ]; then \
			echo "WARNING: knip failed (exit $$status)"; \
			echo "# ERROR: knip failed (exit $$status)" >> $(REPORTS_DIR)/knip-report.txt; \
		fi; \
	fi
	@echo "Report saved to $(REPORTS_DIR)/knip-report.{json,txt}"
	@echo ""
	@echo "--- Summary by category ---"
	@if [ -f $(REPORTS_DIR)/knip-report.json ]; then \
		echo "Unused files:        $$(jq '.files | length' $(REPORTS_DIR)/knip-report.json 2>/dev/null || echo 0)"; \
		echo "Unused dependencies: $$(jq '.dependencies | length' $(REPORTS_DIR)/knip-report.json 2>/dev/null || echo 0)"; \
		echo "Unused exports:      $$(jq '.exports | length' $(REPORTS_DIR)/knip-report.json 2>/dev/null || echo 0)"; \
		echo "Unused types:        $$(jq '.types | length' $(REPORTS_DIR)/knip-report.json 2>/dev/null || echo 0)"; \
	fi

# Generate consolidated dead code report
dead-code-report:
	@mkdir -p $(REPORTS_DIR)
	@echo "=== Generating Consolidated Dead Code Report ==="
	@./scripts/consolidate-dead-code-report.sh > $(REPORTS_DIR)/DEAD_CODE_SUMMARY.md
	@echo "Summary report: $(REPORTS_DIR)/DEAD_CODE_SUMMARY.md"

# Run all dead code analysis
dead-code: dead-code-go dead-code-ts dead-code-report
	@echo ""
	@echo "=== Dead Code Analysis Complete ==="
	@echo "Reports available in $(REPORTS_DIR)/"
	@echo ""
	@echo "Next steps:"
	@echo "  1. Review $(REPORTS_DIR)/DEAD_CODE_SUMMARY.md"
	@echo "  2. Investigate individual reports for details"
	@echo "  3. Create issues/PRs for confirmed dead code removal"
