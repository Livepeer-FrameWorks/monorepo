.PHONY: build build-images build-bin-commodore build-bin-quartermaster build-bin-purser build-bin-decklog build-bin-foghorn build-bin-helmsman build-bin-periscope-ingest build-bin-periscope-query build-bin-signalman build-bin-bridge build-bin-navigator build-bin-privateer build-bin-deckhand build-bin-steward build-bin-skipper build-bin-chandler build-bin-cli \
		build-image-commodore build-image-quartermaster build-image-purser build-image-decklog build-image-foghorn build-image-helmsman build-image-periscope-ingest build-image-periscope-query build-image-signalman build-image-bridge build-image-logbook build-image-navigator build-image-deckhand build-image-steward build-image-skipper build-image-chandler \
		proto graphql graphql-frontend graphql-tray graphql-all clean version install-tools verify test test-cli test-commodore test-quartermaster test-purser test-decklog test-foghorn test-helmsman test-periscope-ingest test-periscope-query test-signalman test-bridge test-navigator test-privateer test-deckhand test-steward test-skipper test-chandler coverage env frontend-env tidy update outdated fmt format \
		lint lint-go lint-frontend lint-all lint-fix lint-report lint-analyze ci-local ci-local-go ci-local-frontend \
		dead-code-install dead-code-go dead-code-ts dead-code-report dead-code \
		ansible-galaxy-install ansible-lint ansible-yamllint ansible-test ansible-check provision-hello

# Prefer annotated git tags like v1.2.3; fallback to describe or dev
VERSION ?= $(shell git describe --tags --match "v[0-9]*" --exact-match 2>/dev/null || git describe --tags --match "v[0-9]*" --dirty --always 2>/dev/null || echo "0.0.0-dev")
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

# component_version(dir) returns the contents of <dir>/VERSION, failing the
# make invocation loudly if the file is missing. Every buildable component
# must carry a VERSION file; no silent 0.0.0 fallback.
component_version = $(if $(wildcard $(1)/VERSION),$(shell tr -d '\n' < $(1)/VERSION),$(error VERSION file required but missing: $(1)/VERSION))

# component_ldflags(binary_name, source_dir) returns the -ldflags block that
# injects platform + per-component version fields into a go build.
define component_ldflags
-ldflags "-X frameworks/pkg/version.Version=$(VERSION) \
          -X frameworks/pkg/version.GitCommit=$(GIT_COMMIT) \
          -X frameworks/pkg/version.BuildDate=$(BUILD_DATE) \
          -X frameworks/pkg/version.ComponentName=$(1) \
          -X frameworks/pkg/version.ComponentVersion=$(call component_version,$(2))"
endef

# component_build_args(binary_name, source_dir) returns the docker build-args
# that inject the same fields into image builds via the per-service Dockerfiles.
define component_build_args
--build-arg VERSION=$(VERSION) \
--build-arg GIT_COMMIT=$(GIT_COMMIT) \
--build-arg BUILD_DATE=$(BUILD_DATE) \
--build-arg COMPONENT_NAME=$(1) \
--build-arg COMPONENT_VERSION=$(call component_version,$(2))
endef

# All microservices (only services with actual binaries)
SERVICES = commodore quartermaster purser decklog foghorn helmsman periscope-ingest periscope-query signalman bridge navigator privateer deckhand steward skipper chandler

# All Go modules (including pkg for testing)
GO_SERVICES = $(shell find . -name "go.mod" -exec dirname {} \;)
GO_GET_ARGS ?= -u all
PNPM_UP_ARGS ?= -r

SERVICE_DIR_commodore = api_control
SERVICE_DIR_quartermaster = api_tenants
SERVICE_DIR_purser = api_billing
SERVICE_DIR_decklog = api_firehose
SERVICE_DIR_foghorn = api_balancing
SERVICE_DIR_helmsman = api_sidecar
SERVICE_DIR_periscope-ingest = api_analytics_ingest
SERVICE_DIR_periscope-query = api_analytics_query
SERVICE_DIR_signalman = api_realtime
SERVICE_DIR_bridge = api_gateway
SERVICE_DIR_navigator = api_dns
SERVICE_DIR_privateer = api_mesh
SERVICE_DIR_deckhand = api_ticketing
SERVICE_DIR_steward = api_forms
SERVICE_DIR_skipper = api_consultant
SERVICE_DIR_chandler = api_assets
SERVICE_DIR_cli = cli

define run-go-tests
	@echo "Running unit tests for $(1)..."
	@(cd $(2) && \
		go mod tidy && \
		go test ./... -race -count=1)
endef

proto:
	cd pkg/proto && make proto

graphql:
	cd api_gateway && make graphql

graphql-frontend:
	cd website_application && pnpm run gql:codegen

graphql-tray:
	./scripts/generate-swift-gql.sh

graphql-all: graphql graphql-frontend graphql-tray

build:
	@echo "Building service binaries with version: $(VERSION)"
	@mkdir -p bin
	@for service in $(SERVICES); do \
		echo "Building $$service..."; \
		$(MAKE) build-bin-$$service; \
	done
	@echo "Building cli..."
	@$(MAKE) build-bin-cli

# Verify (tidy, fmt, vet, test, build) all Go modules and build images when present
verify:
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

build-images:
	@echo "Building Docker images for all services..."
	@for service in $(SERVICES); do \
		$(MAKE) build-image-$$service 2>/dev/null || echo "Skipping $$service (no Dockerfile)"; \
	done

build-image-commodore:
	docker build -t frameworks-commodore:$(VERSION) \
		$(call component_build_args,commodore,api_control) \
		-f api_control/Dockerfile .

build-image-quartermaster:
	docker build -t frameworks-quartermaster:$(VERSION) \
		$(call component_build_args,quartermaster,api_tenants) \
		-f api_tenants/Dockerfile .

build-image-purser:
	docker build -t frameworks-purser:$(VERSION) \
		$(call component_build_args,purser,api_billing) \
		-f api_billing/Dockerfile .

build-image-decklog:
	docker build -t frameworks-decklog:$(VERSION) \
		$(call component_build_args,decklog,api_firehose) \
		-f api_firehose/Dockerfile .

build-image-foghorn:
	docker build -t frameworks-foghorn:$(VERSION) \
		$(call component_build_args,foghorn,api_balancing) \
		-f api_balancing/Dockerfile .

build-image-helmsman:
	docker build -t frameworks-helmsman:$(VERSION) \
		$(call component_build_args,helmsman,api_sidecar) \
		-f api_sidecar/Dockerfile .

build-image-periscope-ingest:
	docker build -t frameworks-periscope-ingest:$(VERSION) \
		$(call component_build_args,periscope-ingest,api_analytics_ingest) \
		-f api_analytics_ingest/Dockerfile .

build-image-periscope-query:
	docker build -t frameworks-periscope-query:$(VERSION) \
		$(call component_build_args,periscope-query,api_analytics_query) \
		-f api_analytics_query/Dockerfile .

build-image-signalman:
	docker build -t frameworks-signalman:$(VERSION) \
		$(call component_build_args,signalman,api_realtime) \
		-f api_realtime/Dockerfile .

build-image-bridge:
	docker build -t frameworks-bridge:$(VERSION) \
		$(call component_build_args,bridge,api_gateway) \
		-f api_gateway/Dockerfile .

build-image-logbook:
	docker build -t frameworks-logbook:$(VERSION) \
		--build-arg BUILD_ENV=production \
		-f website_docs/Dockerfile .

build-image-navigator:
	docker build -t frameworks-navigator:$(VERSION) \
		$(call component_build_args,navigator,api_dns) \
		-f api_dns/Dockerfile .

build-image-deckhand:
	docker build -t frameworks-deckhand:$(VERSION) \
		$(call component_build_args,deckhand,api_ticketing) \
		-f api_ticketing/Dockerfile .

build-image-steward:
	docker build -t frameworks-steward:$(VERSION) \
		$(call component_build_args,steward,api_forms) \
		-f api_forms/Dockerfile .

build-image-skipper:
	docker build -t frameworks-skipper:$(VERSION) \
		$(call component_build_args,skipper,api_consultant) \
		-f api_consultant/Dockerfile .

build-image-chandler:
	docker build -t frameworks-chandler:$(VERSION) \
		$(call component_build_args,chandler,api_assets) \
		-f api_assets/Dockerfile .

build-bin-commodore:
	cd api_control && go build $(call component_ldflags,commodore,api_control) -o ../bin/commodore ./cmd/commodore

build-bin-quartermaster:
	cd api_tenants && go build $(call component_ldflags,quartermaster,api_tenants) -o ../bin/quartermaster ./cmd/quartermaster

build-bin-purser:
	cd api_billing && go build $(call component_ldflags,purser,api_billing) -o ../bin/purser ./cmd/purser

build-bin-decklog:
	cd api_firehose && go build $(call component_ldflags,decklog,api_firehose) -o ../bin/decklog ./cmd/decklog

build-bin-foghorn:
	cd api_balancing && go build $(call component_ldflags,foghorn,api_balancing) -o ../bin/foghorn ./cmd/foghorn

build-bin-helmsman:
	cd api_sidecar && go build $(call component_ldflags,helmsman,api_sidecar) -o ../bin/helmsman ./cmd/helmsman

build-bin-periscope-ingest:
	cd api_analytics_ingest && go build $(call component_ldflags,periscope-ingest,api_analytics_ingest) -o ../bin/periscope-ingest ./cmd/periscope

build-bin-periscope-query:
	cd api_analytics_query && go build $(call component_ldflags,periscope-query,api_analytics_query) -o ../bin/periscope-query ./cmd/periscope

build-bin-signalman:
	cd api_realtime && go build $(call component_ldflags,signalman,api_realtime) -o ../bin/signalman ./cmd/signalman

build-bin-bridge:
	cd api_gateway && go build $(call component_ldflags,bridge,api_gateway) -o ../bin/bridge ./cmd/bridge

build-bin-navigator:
	cd api_dns && go build $(call component_ldflags,navigator,api_dns) -o ../bin/navigator ./cmd/navigator

build-bin-privateer:
	cd api_mesh && go build $(call component_ldflags,privateer,api_mesh) -o ../bin/privateer ./cmd/privateer

build-bin-deckhand:
	cd api_ticketing && go build $(call component_ldflags,deckhand,api_ticketing) -o ../bin/deckhand ./cmd/deckhand

build-bin-steward:
	cd api_forms && go build $(call component_ldflags,steward,api_forms) -o ../bin/steward ./cmd/steward

build-bin-skipper:
	cd api_consultant && go build $(call component_ldflags,skipper,api_consultant) -o ../bin/skipper ./cmd/skipper

build-bin-chandler:
	cd api_assets && go build $(call component_ldflags,chandler,api_assets) -o ../bin/chandler ./cmd/chandler

build-bin-cli:
	cd cli && go build $(call component_ldflags,cli,cli) -o ../bin/cli .

clean:
	rm -rf bin/
	cd pkg/proto && make clean

version:
	@echo "Version: $(VERSION)"
	@echo "Git Commit: $(GIT_COMMIT)"
	@echo "Build Date: $(BUILD_DATE)"

install-tools:
	cd pkg/proto && make install-tools
	cd api_gateway && make install-tools
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

test:
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

test-cli:
	$(call run-go-tests,cli,$(SERVICE_DIR_cli))

test-commodore:
	$(call run-go-tests,commodore,$(SERVICE_DIR_commodore))

test-quartermaster:
	$(call run-go-tests,quartermaster,$(SERVICE_DIR_quartermaster))

test-purser:
	$(call run-go-tests,purser,$(SERVICE_DIR_purser))

test-decklog:
	$(call run-go-tests,decklog,$(SERVICE_DIR_decklog))

test-foghorn:
	$(call run-go-tests,foghorn,$(SERVICE_DIR_foghorn))

test-helmsman:
	$(call run-go-tests,helmsman,$(SERVICE_DIR_helmsman))

test-periscope-ingest:
	$(call run-go-tests,periscope-ingest,$(SERVICE_DIR_periscope-ingest))

test-periscope-query:
	$(call run-go-tests,periscope-query,$(SERVICE_DIR_periscope-query))

test-signalman:
	$(call run-go-tests,signalman,$(SERVICE_DIR_signalman))

test-bridge:
	$(call run-go-tests,bridge,$(SERVICE_DIR_bridge))

test-navigator:
	$(call run-go-tests,navigator,$(SERVICE_DIR_navigator))

test-privateer:
	$(call run-go-tests,privateer,$(SERVICE_DIR_privateer))

test-deckhand:
	$(call run-go-tests,deckhand,$(SERVICE_DIR_deckhand))

test-steward:
	$(call run-go-tests,steward,$(SERVICE_DIR_steward))

test-skipper:
	$(call run-go-tests,skipper,$(SERVICE_DIR_skipper))

test-chandler:
	$(call run-go-tests,chandler,$(SERVICE_DIR_chandler))

# Run unit tests with JUnit XML output for Codecov Test Analytics
test-junit:
	@echo "Running unit tests with JUnit output for all Go modules..."
	@mkdir -p $(CURDIR)/test-results
	@rm -f $(CURDIR)/test-results/go-junit.xml
	go install github.com/jstemmer/go-junit-report/v2@latest
	@failed=0; \
	failed_modules=""; \
	for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && \
			go mod tidy && \
			go test ./... -race -count=1 -v) > $(CURDIR)/test-results/$$service_name.out 2>&1; \
		test_exit=$$?; \
		go-junit-report < $(CURDIR)/test-results/$$service_name.out >> $(CURDIR)/test-results/go-junit.xml 2>/dev/null; \
		if [ $$test_exit -ne 0 ]; then \
			echo "  FAILED: $$service_name"; \
			grep -E -- "--- FAIL:|^FAIL\b|^panic:" $(CURDIR)/test-results/$$service_name.out || tail -20 $(CURDIR)/test-results/$$service_name.out; \
			failed=1; \
			failed_modules="$$failed_modules $$service_name"; \
		else \
			rm -f $(CURDIR)/test-results/$$service_name.out; \
		fi; \
	done; \
	if [ $$failed -eq 0 ]; then \
		echo "✓ Unit tests passed"; \
	else \
		echo "✗ Unit tests failed:$$failed_modules"; \
		exit 1; \
	fi
	@echo "JUnit report saved to $(CURDIR)/test-results/go-junit.xml"

coverage:
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
	@echo "Generating .env and .env.frontend from config/env/*.env..."
	@cd scripts/env && GOCACHE=$$(pwd)/.gocache go run . --output ../../.env
	@cd scripts/env && GOCACHE=$$(pwd)/.gocache go run . --frontend-only --output ../../.env.frontend

frontend-env:
	@echo "Generating .env.frontend from config/env/base.env..."
	@cd scripts/env && GOCACHE=$$(pwd)/.gocache go run . --frontend-only --output ../../.env.frontend

# SOPS encryption for secrets.env (requires: brew install sops age)
encrypt:
	@sops -e -i config/env/secrets.env
	@echo "Encrypted config/env/secrets.env"

decrypt:
	@sops -d -i config/env/secrets.env
	@echo "Decrypted config/env/secrets.env"

tidy:
	@echo "Running go mod tidy for all Go modules..."
	@for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && go mod tidy); \
	done
	@echo "✓ All modules tidied"

update:
	@echo "Updating Go dependencies for all Go modules (go get $(GO_GET_ARGS))..."
	@failed=0; \
	for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && go get $(GO_GET_ARGS)) || failed=1; \
	done; \
	if [ $$failed -eq 0 ]; then \
		echo "✓ Go dependencies updated"; \
	else \
		echo "✗ Go dependency update failed"; \
		exit 1; \
	fi
	@$(MAKE) tidy
	@echo "Updating JS dependencies (pnpm up $(PNPM_UP_ARGS))..."
	pnpm up $(PNPM_UP_ARGS)
	@echo "✓ Update complete"

outdated:
	@echo "Checking outdated Go dependencies..."
	@for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		stale=$$(cd $$service_dir && go list -m -u all 2>/dev/null | grep '\[' | wc -l | tr -d ' '); \
		if [ "$$stale" -gt 0 ]; then \
			echo "==> $$service_name ($$stale outdated)"; \
			cd $$service_dir && go list -m -u all 2>/dev/null | grep '\['; \
		fi; \
	done
	@echo ""
	@echo "Checking outdated JS dependencies..."
	@pnpm outdated -r 2>/dev/null || true

fmt:
	@echo "Running go fmt for all Go modules..."
	@for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && go fmt ./...); \
	done
	@echo "✓ All modules formatted"

# Matches CI lint jobs (lint-go + lint-frontend).
lint:
	@failed=0; \
	$(MAKE) lint-go || failed=1; \
	$(MAKE) lint-frontend || failed=1; \
	if [ $$failed -eq 1 ]; then exit 1; fi

# Baseline mode: reports only violations newer than .golangci-baseline (matches CI go-lint).
lint-go:
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

# Matches CI frontend-lint.
lint-frontend:
	@echo "Running frontend lint checks (pnpm lint + pnpm format:check)..."
	pnpm lint
	pnpm format:check

# No baseline: reports every violation, including pre-existing ones. For cleanup work.
lint-all:
	@echo "Running golangci-lint for all Go modules (all violations)..."
	@for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && golangci-lint run --timeout=5m ./...); \
	done

lint-fix:
	@echo "Running golangci-lint with auto-fix for all Go modules..."
	@for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		(cd $$service_dir && golangci-lint run --fix --timeout=5m ./...); \
	done

format:
	@$(MAKE) fmt
	pnpm format

lint-report:
	@./scripts/lint-report.sh

lint-analyze:
	@./scripts/lint-analyze.sh

ci-local:
	@failed=0; \
	$(MAKE) ci-local-go || failed=1; \
	$(MAKE) ci-local-frontend || failed=1; \
	if [ $$failed -eq 1 ]; then exit 1; fi
	@echo "✓ Local CI parity checks passed"

ci-local-go:
	@echo "Running local Go CI checks..."
	@$(MAKE) lint-go
	@$(MAKE) test
	@$(MAKE) build

ci-local-frontend:
	@echo "Running local frontend CI checks..."
	pnpm lint
	pnpm format:check
	pnpm test:coverage
	pnpm build

REPORTS_DIR := reports

dead-code-install:
	@echo "Installing Go dead code analysis tools..."
	go install golang.org/x/tools/cmd/deadcode@latest
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	@echo ""
	@echo "✓ Go dead code analysis tools installed"
	@echo "Note: knip must be installed separately (workspace dev dependency)."

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

dead-code-report:
	@mkdir -p $(REPORTS_DIR)
	@echo "=== Generating Consolidated Dead Code Report ==="
	@./scripts/consolidate-dead-code-report.sh > $(REPORTS_DIR)/DEAD_CODE_SUMMARY.md
	@echo "Summary report: $(REPORTS_DIR)/DEAD_CODE_SUMMARY.md"

dead-code: dead-code-go dead-code-ts dead-code-report
	@echo ""
	@echo "=== Dead Code Analysis Complete ==="
	@echo "Reports available in $(REPORTS_DIR)/"
	@echo ""
	@echo "Next steps:"
	@echo "  1. Review $(REPORTS_DIR)/DEAD_CODE_SUMMARY.md"
	@echo "  2. Investigate individual reports for details"
	@echo "  3. Create issues/PRs for confirmed dead code removal"

# ---- Ansible (collection-driven provisioning) -----------------------------
# ansible-galaxy-install   resolve collections into a local cache.
# ansible-lint             lint the frameworks.infra collection.
# ansible-check            syntax-check every playbook under ansible/playbooks.
# provision-hello          end-to-end wiring smoke against localhost.

ANSIBLE_DIR := ansible
ANSIBLE_REQUIREMENTS_REL := requirements.yml
ANSIBLE_CACHE_REL := .cache/collections
ANSIBLE_PLAYBOOKS := $(wildcard $(ANSIBLE_DIR)/playbooks/*.yml)

ansible-galaxy-install:
	@echo "=== Installing Ansible collections ==="
	@mkdir -p $(ANSIBLE_DIR)/$(ANSIBLE_CACHE_REL) $(ANSIBLE_DIR)/.cache/roles
	cd $(ANSIBLE_DIR) && ansible-galaxy collection install -r $(ANSIBLE_REQUIREMENTS_REL) -p $(ANSIBLE_CACHE_REL)
	@echo "=== Installing Ansible roles ==="
	cd $(ANSIBLE_DIR) && ansible-galaxy role install -r $(ANSIBLE_REQUIREMENTS_REL) --roles-path .cache/roles

ansible-lint: ansible-galaxy-install
	@echo "=== Linting frameworks.infra collection ==="
	cd $(ANSIBLE_DIR) && ansible-lint --profile=production \
		collections/ansible_collections/frameworks/infra

ansible-yamllint:
	@echo "=== yamllint on ansible tree ==="
	cd $(ANSIBLE_DIR) && yamllint -s collections/ansible_collections/frameworks/infra playbooks

ansible-test: ansible-galaxy-install ansible-check ansible-lint ansible-yamllint
	@echo "=== ansible test suite complete ==="

# Syntax check each playbook. ANSIBLE_COLLECTIONS_PATH deliberately not set —
# ansible.cfg [defaults] collections_path already lists the source tree first
# (./collections) followed by the install cache (./.cache/collections), so
# exporting the env var would clobber that ordering and lose the source.
ansible-check: ansible-galaxy-install
	@echo "=== Syntax-checking playbooks ==="
	@for pb in $(ANSIBLE_PLAYBOOKS); do \
		echo "  $$pb"; \
		relpb=$${pb#$(ANSIBLE_DIR)/}; \
		( cd $(ANSIBLE_DIR) && \
		  ansible-playbook --syntax-check "$$relpb" \
			-i localhost, -c local \
		) || exit 1; \
	done

provision-hello: ansible-galaxy-install
	@echo "=== Running hello-role smoke test ==="
	cd cli && go run ./internal/ansiblesmoke \
		-requirements ../$(ANSIBLE_DIR)/$(ANSIBLE_REQUIREMENTS_REL) \
		-playbook ../$(ANSIBLE_DIR)/playbooks/hello.yml \
		-cache-dir ../$(ANSIBLE_DIR)/$(ANSIBLE_CACHE_REL)
