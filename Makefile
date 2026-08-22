.PHONY: build build-images build-bin-commodore build-bin-quartermaster build-bin-purser build-bin-decklog build-bin-foghorn build-bin-helmsman build-bin-periscope-ingest build-bin-periscope-query build-bin-periscope-metering build-bin-signalman build-bin-bridge build-bin-navigator build-bin-privateer build-bin-deckhand build-bin-steward build-bin-skipper build-bin-chandler build-bin-cli \
		build-image-commodore build-image-quartermaster build-image-purser build-image-decklog build-image-foghorn build-image-helmsman build-image-periscope-ingest build-image-periscope-query build-image-periscope-metering build-image-signalman build-image-bridge build-image-logbook build-image-navigator build-image-deckhand build-image-steward build-image-skipper build-image-chandler \
		proto proto-check sqlc sqlc-check graphql graphql-frontend graphql-tray graphql-all clean version install-tools verify test test-cli test-pkg test-topology test-crypto-evm test-dashboards test-commodore test-quartermaster test-purser test-decklog test-foghorn test-helmsman test-periscope-ingest test-periscope-query test-signalman test-bridge test-navigator test-privateer test-deckhand test-steward test-skipper test-chandler coverage env frontend-env tidy update outdated fmt format \
		lint lint-go lint-frontend lint-all lint-fix lint-report lint-analyze ci-local ci-local-go ci-local-frontend \
		validate-migrations verify-release-state test-release-state verify-schema verify-schema-migrations verify-schema-migrations-core verify-schema-postgres verify-navigator-db verify-skipper-db verify-periscope-metering-db verify-commodore-db verify-schema-yugabyte verify-yugabyte-ha verify-schema-clickhouse verify-feature-registry seed-demo seed-demo-postgres seed-demo-clickhouse reset-demo-databases-plan reset-demo-databases release-plan test-release-plan \
		dead-code-install dead-code-go dead-code-ts dead-code-report dead-code \
		ansible-galaxy-install ansible-lint ansible-yamllint ansible-test ansible-check ansible-molecule ansible-molecule-run ansible-molecule-all provision-hello

# Prefer annotated git tags like v1.2.3; fallback to describe or dev
VERSION ?= $(shell git describe --tags --match "v[0-9]*" --exact-match 2>/dev/null || git describe --tags --match "v[0-9]*" --dirty --always 2>/dev/null || echo "0.0.0-dev")
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
GO_BUILD_TAGS ?= nomsgpack
GO_TAG_FLAGS = $(if $(strip $(GO_BUILD_TAGS)),-tags=$(GO_BUILD_TAGS),)
SQLC_VERSION ?= v1.31.1

# component_ldflags(binary_name, source_dir) returns the -ldflags block that
# injects platform + per-component version fields into a go build. Components
# ship in lockstep with the platform, so ComponentVersion equals VERSION; the
# field is kept for runtime logs/metrics that read it.
define component_ldflags
-ldflags "-X github.com/Livepeer-FrameWorks/monorepo/pkg/version.Version=$(VERSION) \
          -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.GitCommit=$(GIT_COMMIT) \
          -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.BuildDate=$(BUILD_DATE) \
          -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.ComponentName=$(1) \
          -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.ComponentVersion=$(VERSION)"
endef

# component_build_args(binary_name, source_dir) returns the docker build-args
# that inject the same fields into image builds via the per-service Dockerfiles.
define component_build_args
--build-arg VERSION=$(VERSION) \
--build-arg GIT_COMMIT=$(GIT_COMMIT) \
--build-arg BUILD_DATE=$(BUILD_DATE) \
--build-arg COMPONENT_NAME=$(1) \
--build-arg COMPONENT_VERSION=$(VERSION)
endef

# All microservices (only services with actual binaries)
SERVICES = commodore quartermaster purser decklog foghorn helmsman periscope-ingest periscope-query periscope-metering signalman bridge navigator privateer deckhand steward skipper chandler

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
SERVICE_DIR_periscope-metering = api_analytics_query
SERVICE_DIR_signalman = api_realtime
SERVICE_DIR_bridge = api_gateway
SERVICE_DIR_navigator = api_dns
SERVICE_DIR_privateer = api_mesh
SERVICE_DIR_deckhand = api_ticketing
SERVICE_DIR_steward = api_forms
SERVICE_DIR_skipper = api_consultant
SERVICE_DIR_chandler = api_assets
SERVICE_DIR_cli = cli
SERVICE_DIR_pkg = pkg

define run-go-tests
	@echo "Running unit tests for $(1)..."
	@(cd $(2) && \
		go mod tidy && \
		go test $(GO_TAG_FLAGS) ./... -race -count=1)
endef

proto:
	cd pkg/proto && make proto

proto-check:
	cd pkg/proto && make proto-check

sqlc:
	cd api_billing && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd api_dns && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd api_consultant && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd api_analytics_query && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd api_control && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

sqlc-check: sqlc
	@git diff --exit-code -- api_billing/internal/database/purserdb
	@git diff --exit-code -- api_dns/internal/database/navigatordb
	@git diff --exit-code -- api_consultant/internal/database/skipperdb
	@git diff --exit-code -- api_analytics_query/internal/database/meteringdb
	@git diff --exit-code -- api_control/internal/database/commodoredb
	@test -z "$$(git status --porcelain --untracked-files=all -- api_billing/internal/database/purserdb api_dns/internal/database/navigatordb api_consultant/internal/database/skipperdb api_analytics_query/internal/database/meteringdb api_control/internal/database/commodoredb)" || { \
		echo "ERROR: sqlc generated files are untracked or stale; run make sqlc"; \
		git status --short --untracked-files=all -- api_billing/internal/database/purserdb api_dns/internal/database/navigatordb api_consultant/internal/database/skipperdb api_analytics_query/internal/database/meteringdb api_control/internal/database/commodoredb; \
		exit 1; \
	}

seed-demo: seed-demo-postgres seed-demo-clickhouse

seed-demo-postgres:
	@set -eu; \
	for db in quartermaster purser commodore foghorn periscope; do \
		echo "Seeding PostgreSQL database $$db..."; \
		docker compose exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -U "$$1" -d "$$1"' sh "$$db" < "pkg/database/sql/seeds/demo/postgres/$$db.sql"; \
	done

seed-demo-clickhouse:
	@echo "Seeding ClickHouse database periscope..."
	@docker compose exec -T clickhouse clickhouse-client --multiquery < pkg/database/sql/seeds/demo/clickhouse_demo_data.sql

reset-demo-databases-plan:
	@./scripts/reset-demo-databases.sh --plan

reset-demo-databases:
	@./scripts/reset-demo-databases.sh

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
	@failed=0; \
	for service in $(SERVICES); do \
		echo "Building $$service..."; \
		$(MAKE) build-bin-$$service || failed=1; \
	done; \
	echo "Building cli..."; \
	$(MAKE) build-bin-cli || failed=1; \
	if [ $$failed -eq 0 ]; then \
		echo "✓ Build passed"; \
	else \
		echo "✗ Build failed"; \
		exit 1; \
	fi

# release-plan: compute per-artefact source hashes + carry-forward decisions
# against a track-aware baseline release manifest. Output is JSON. The
# release workflow runs this before the build matrix to skip rebuilds of
# unchanged artefacts. Pass GITOPS=../gitops, TAG=v0.2.40, OUT=dist/release-plan.json
# to override defaults.
release-plan:
	@cd tools/release-plan && go run . \
		--monorepo $(CURDIR) \
		--gitops $(if $(GITOPS),$(abspath $(GITOPS)),$(CURDIR)/../gitops) \
		--tag $(if $(TAG),$(TAG),$(VERSION)) \
		$(if $(OUT),--out $(OUT))

# Unit tests for the release-plan tool (its own module; not in the service loop).
test-release-plan:
	$(call run-go-tests,release-plan,tools/release-plan)

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
			go vet $(GO_TAG_FLAGS) $$(go list $(GO_TAG_FLAGS) ./... | grep -v '/graph/generated') && \
			go test $(GO_TAG_FLAGS) ./... -race -count=1 && \
			go build $(GO_TAG_FLAGS) ./...) || failed=1; \
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

build-image-periscope-metering:
	docker build -t frameworks-periscope-metering:$(VERSION) \
		$(call component_build_args,periscope-metering,api_analytics_query) \
		--build-arg CMD_PACKAGE=./cmd/periscope-metering \
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
	cd api_control && go build $(GO_TAG_FLAGS) $(call component_ldflags,commodore,api_control) -o ../bin/commodore ./cmd/commodore

build-bin-quartermaster:
	cd api_tenants && go build $(GO_TAG_FLAGS) $(call component_ldflags,quartermaster,api_tenants) -o ../bin/quartermaster ./cmd/quartermaster

build-bin-purser:
	cd api_billing && go build $(GO_TAG_FLAGS) $(call component_ldflags,purser,api_billing) -o ../bin/purser ./cmd/purser

build-bin-decklog:
	cd api_firehose && go build $(GO_TAG_FLAGS) $(call component_ldflags,decklog,api_firehose) -o ../bin/decklog ./cmd/decklog

build-bin-foghorn:
	cd api_balancing && go build $(GO_TAG_FLAGS) $(call component_ldflags,foghorn,api_balancing) -o ../bin/foghorn ./cmd/foghorn

build-bin-helmsman:
	cd api_sidecar && go build $(GO_TAG_FLAGS) $(call component_ldflags,helmsman,api_sidecar) -o ../bin/helmsman ./cmd/helmsman

build-bin-periscope-ingest:
	cd api_analytics_ingest && go build $(GO_TAG_FLAGS) $(call component_ldflags,periscope-ingest,api_analytics_ingest) -o ../bin/periscope-ingest ./cmd/periscope

build-bin-periscope-query:
	cd api_analytics_query && go build $(GO_TAG_FLAGS) $(call component_ldflags,periscope-query,api_analytics_query) -o ../bin/periscope-query ./cmd/periscope

build-bin-periscope-metering:
	cd api_analytics_query && go build $(GO_TAG_FLAGS) $(call component_ldflags,periscope-metering,api_analytics_query) -o ../bin/periscope-metering ./cmd/periscope-metering

build-bin-signalman:
	cd api_realtime && go build $(GO_TAG_FLAGS) $(call component_ldflags,signalman,api_realtime) -o ../bin/signalman ./cmd/signalman

build-bin-bridge:
	cd api_gateway && go build $(GO_TAG_FLAGS) $(call component_ldflags,bridge,api_gateway) -o ../bin/bridge ./cmd/bridge

build-bin-navigator:
	cd api_dns && go build $(GO_TAG_FLAGS) $(call component_ldflags,navigator,api_dns) -o ../bin/navigator ./cmd/navigator

build-bin-privateer:
	cd api_mesh && go build $(GO_TAG_FLAGS) $(call component_ldflags,privateer,api_mesh) -o ../bin/privateer ./cmd/privateer

build-bin-deckhand:
	cd api_ticketing && go build $(GO_TAG_FLAGS) $(call component_ldflags,deckhand,api_ticketing) -o ../bin/deckhand ./cmd/deckhand

build-bin-steward:
	cd api_forms && go build $(GO_TAG_FLAGS) $(call component_ldflags,steward,api_forms) -o ../bin/steward ./cmd/steward

build-bin-skipper:
	cd api_consultant && go build $(GO_TAG_FLAGS) $(call component_ldflags,skipper,api_consultant) -o ../bin/skipper ./cmd/skipper

build-bin-chandler:
	cd api_assets && go build $(GO_TAG_FLAGS) $(call component_ldflags,chandler,api_assets) -o ../bin/chandler ./cmd/chandler

build-bin-cli:
	cd cli && go build $(GO_TAG_FLAGS) $(call component_ldflags,cli,cli) -o ../bin/cli .

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
			go test $(GO_TAG_FLAGS) ./... -race -count=1) || failed=1; \
	done; \
	if [ $$failed -eq 0 ]; then \
		echo "✓ Unit tests passed"; \
	else \
		echo "✗ Unit tests failed"; \
		exit 1; \
	fi

test-cli:
	$(call run-go-tests,cli,$(SERVICE_DIR_cli))

test-pkg:
	$(call run-go-tests,pkg,$(SERVICE_DIR_pkg))

test-topology:
	@echo "Running infrastructure topology contract tests..."
	@(cd $(SERVICE_DIR_pkg) && go test $(GO_TAG_FLAGS) ./topology -race -count=1)

test-accesspolicy:
	@echo "Running access-policy contract tests..."
	@(cd $(SERVICE_DIR_pkg) && go test $(GO_TAG_FLAGS) ./accesspolicy -race -count=1)

test-dashboards:
	@echo "Running dashboard divergence checks..."
	@cd cli && go test $(GO_TAG_FLAGS) ./pkg/dashcheck -count=1

test-commodore:
	$(call run-go-tests,commodore,$(SERVICE_DIR_commodore))

test-quartermaster:
	$(call run-go-tests,quartermaster,$(SERVICE_DIR_quartermaster))

test-purser:
	$(call run-go-tests,purser,$(SERVICE_DIR_purser))

test-crypto-evm:
	@command -v anvil >/dev/null 2>&1 || { echo "ERROR: test-crypto-evm requires Foundry anvil"; exit 1; }
	@echo "Running deterministic local-EVM x402 and sweep fault tests..."
	@cd api_billing && go test -tags 'nomsgpack crypto_evm' -run 'TestEmbeddedFacilitatorAgainstLocalEVMFaults|TestNativeSweepAgainstLocalEVMNonceAndFinality' -count=1 -timeout 120s ./internal/handlers/ ./internal/grpc/

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
			go test $(GO_TAG_FLAGS) ./... -race -count=1 -v) > $(CURDIR)/test-results/$$service_name.out 2>&1; \
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
			if go test $(GO_TAG_FLAGS) ./... -coverpkg=./... -coverprofile="$$tmpfile" -covermode=atomic -count=1 >/dev/null 2>&1; then \
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
	pnpm run format:check

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

verify-release-state:
	@if [ -n "$(RELEASE_DIFF_BASE)" ]; then \
		scripts/check-migration-version.sh --diff-base "$(RELEASE_DIFF_BASE)"; \
	else \
		scripts/check-migration-version.sh --worktree; \
	fi

test-release-state:
	@scripts/check-migration-version.test.sh

validate-migrations: verify-release-state
	@echo "Validating embedded SQL migrations..."
	@cd cli && go run . cluster migrate validate

# Real-engine schema + behavior verification harness (Docker). Runs the ENTIRE schema_verify-tagged
# suite: the baseline==baseline+migrations drift guards (Postgres + ClickHouse) AND the real-engine
# behavior tests that run production SQL against a live engine — the freeze-attempt state machine,
# creation-command CAS/lease, playback-index upgrade, artifact-events dedup, etc. These are the durable
# gate for concurrency/constraint properties sqlmock can't prove. Needs a running Docker daemon; gated
# behind the schema_verify build tag so a plain `make test` never needs Docker.
SCHEMA_VERIFY_FROM_TAG ?= $(shell git tag --merged HEAD --sort=-v:refname | awk '/^v[0-9]+\.[0-9]+\.[0-9]+$$/ { print; exit }')
SCHEMA_VERIFY_TESTS := TestComposeUsesSchemaHarnessImages|TestPostgresServiceDatabaseInitialization|TestPostgresIntrospectionCoversDeployRelevantObjects|TestPostgresBaselineEqualsReplay|TestPostgresTaggedBaselineUpgradeEqualsCurrent|TestClickHouseBaselineEqualsReplay|TestClickHouseTaggedBaselineUpgradeEqualsCurrent|TestArtifactEventsDedupedPreservesLegacyRows|TestArtifactPlaybackIndexUpgradeFromReleasedLower|TestCreationCommandAckLeaseClaim|TestCreationCommandAckLeaseTokenFencesStaleSettlement|TestCreationCommandCASMutualExclusion
# CI sets CONTRACT_COVERAGE_DIR so these same test executions emit engine-specific profiles.
# Leaving it unset preserves the ordinary local targets without coverage artifacts.
CONTRACT_GO_TEST := $(CURDIR)/scripts/run-go-contract-test.sh

verify-schema:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-schema requires a running Docker daemon (real-engine tests must run, not skip)"; exit 1; }
	@echo "Verifying schema convergence + real-engine behavior tests (Docker)..."
	@# Explicit -run list so ONLY the real-engine schema tests execute — the schema_verify build tag is
	@# additive and would otherwise also run the package's ordinary (untagged) unit tests.
	@cd cli && FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG='$(SCHEMA_VERIFY_FROM_TAG)' go test -tags schema_verify -run '$(SCHEMA_VERIFY_TESTS)' -count=1 -timeout 1200s ./pkg/provisioner/
	@echo "Running real-engine production-path freeze + chapter-auth + thumbnail-foundation tests (Docker: claim concurrency, ledger atomicity, NULL-safe constraints, finalize-node binding, thumbnail publish/completion, deletion-saga tombstone fences, HA recovery lease)..."
	@cd api_balancing && go test -tags schema_verify -run 'TestClaimFreezeAttempt_RealPG|TestClaimFreezeAttempt_LedgerAtomicity_RealPG|TestFreezeConstraints_RealPG|TestChapterFinalizeNodeBinding_RealPG|TestThumbnailPublication_RealPG|TestThumbnailCompletion_RealPG|TestThumbnailProjectionFence_RealPG|TestThumbnailProjectionRecoveryPoison_RealPG|TestThumbnailProjectionReassert_RealPG|TestThumbnailReassertClaim_LeaseAndLimit_RealPG|TestThumbnailServingClusterTriggersReprojection_RealPG|TestStreamCleanupSaga_TombstoneFences_RealPG|TestThumbnailRecoveryLease_RealPG|TestThumbnailPromoteVsDeleteLeak_RealPG|TestThumbnailPublishLease_RealPG|TestThumbnailPublishTokenFence_RealPG|TestThumbnailRecoveryRedrivesTokenizedPublishing_RealPG|TestThumbnailPublishingRequiresToken_RealPG|TestEnforceImmutableLocalBackend_RealPG|TestEnforceImmutableLocalBackend_ExactMatchNotNormalized_RealPG|TestEnforceImmutableLocalBackend_ConcurrentFirstBootRace_RealPG|TestEstablishOrEnforceLocalBackend_FirstBootAuthority_RealPG|TestIngestSessionIdentity_RealPG|TestIngestSessionConcurrentCreate_RealPG|TestDVRCloseBeforeStartFence_RealPG|TestIngestSessionSchemaInvariants_RealPG|TestDVRRecheckGenerationScoped_RealPG|TestListUnstartedDVRIntents_RealPG|TestIngestSessionReuseAndStopClaim_RealPG|TestIngestSessionConcurrentDifferentSessions_RealPG|TestAdvisoryLockKeysAreValidPGText_RealPG|TestStopDVRForEndedSourceGenerationFence_RealPG|TestIngestSessionAlreadyEndedIdempotency_RealPG|TestIngestSessionConcurrentCrossNodeAdmission_RealPG|TestEndIngestSessionsForStreamEnd_ReapsLostCloseFencedByEventTime_RealPG|TestIngestSessionReaper_RealPG|TestIngestSessionReaper_BlipToleranceRealPG|TestIngestSessionReaper_RechecksAbsenceUnderRetireGuardRealPG|TestNeverProjectedSessionReaperQueuesInactiveProjectionRealPG|TestIngestCloseTombstone_RealPG|TestFenceOfflineBackstop_RealPG|TestProjectSourceIfCurrent_RealPG|TestOfflineEffectSerializesWithAdmission_RealPG|TestOfflineEffectSupersededByReconnect_RealPG|TestProjectSourceFailureAbortsPendingSession_RealPG|TestPushRewriteRetry_IdempotentResumedProjection_RealPG|TestResumedProjectionDeniedWhenRegistryHoldsNewerRevision_RealPG|TestAdmissionEffectApplyAndSupersede_RealPG|TestAdmissionEffectPoisonSettlesLegOnly_RealPG|TestAdmissionAckCompletesWhileWorkerPaused_RealPG|TestAdmissionClaimAffinityRoutesToAuthority_RealPG' -count=1 -timeout 600s ./internal/control/
	@echo "Running real-engine admission-ledger to Redis membership-cleanup proof (Docker: tenant/revision anti-join and exact tombstone purge)..."
	@cd api_balancing && go test -tags schema_verify -run 'TestMembershipTombstoneCleanup_PostgresProofToRedisPurge_RealPG' -count=1 -timeout 600s ./internal/federation/
	@echo "Running real-engine production-path cleanup + purge-ownership + stream-cleanup drainer + thumbnail-lifecycle integration tests (Docker: multipart-vs-stale-freeze cleanup, ownership filter NULL semantics, durable stream-cleanup convergence + local-alias routing, full publish→delete→drain lifecycle)..."
	@cd api_balancing && go test -tags schema_verify -run 'TestStaleFreezeCleanup_RealPG|TestPurgeOwnershipFilter_RealPG|TestStreamCleanupDrainer_ConvergesFromDurableRow_RealPG|TestStreamCleanupDrainer_LocallyBackedAliasSweepsLocally_RealPG|TestThumbnailLifecycleIntegration_RealPG|TestStreamCleanupDrainer_RepointGuardFailsClosed_RealPG|TestStreamCleanupDrainer_DelayedResweep_RealPG|TestStreamCleanupDrainer_FinalizeAtomicOnControlCleanupFailure_RealPG' -count=1 -timeout 600s ./internal/jobs/
	@echo "Running real-engine Commodore two-phase deletion saga test (Docker: outbox claim/lease/finalize converges a stream deletion through a Foghorn delivery outage — coordination only, Foghorn RPCs faked)..."
	@cd api_control && go test -tags schema_verify -run 'TestStreamCleanupOutboxLoop_DeliveryOutageConverges_RealPG|TestUpdateArtifactCatalogSnapshot_ServingClusterEqualRevisionRepair_RealPG|TestStreamThumbnailCleanup_DispatchesEveryOwningCell_RealPG|TestRecordStreamActiveCluster_ServiceOnly_DoesNotTouchServingSet_RealPG|TestDeleteStream_RoutesToEveryServingCell_RealPG|TestRegisterStreamThumbnailServingCell_FencesOnDeletion_RealPG|TestStreamThumbnailCleanup_HangingCellDoesNotStarveSiblings_RealPG|TestRegisterVsDeleteStream_Linearizes_RealPG|TestStreamCleanupOutbox_ThumbnailPhaseMarkedThenSkipped_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@echo "Running real-engine ingest placement-claim ownership tests (Docker: same-cluster theft, reserve-vs-refresh, lapse, owner-fenced renew/release, cross-writer isolation)..."
	@cd api_control && go test -tags schema_verify -run 'TestValidateStreamKey_SameClusterCannotStealLiveClaim_RealPG|TestValidateStreamKey_OwnerRefreshIsNotAReservation_RealPG|TestValidateStreamKey_LapsedClaimIsReservable_RealPG|TestSyncActiveIngestPlacement_ReleaseRequiresOwnership_RealPG|TestSyncActiveIngestPlacement_RenewalRequiresOwnership_RealPG|TestSyncActiveIngestPlacement_RenewalEstablishesUnheldClaim_RealPG|TestClearStreamActiveCluster_CannotClearPushClaim_RealPG' -count=1 -timeout 600s ./internal/grpc/

verify-schema-migrations: verify-schema-migrations-core verify-schema-yugabyte

verify-schema-migrations-core:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-schema-migrations requires a running Docker daemon"; exit 1; }
	@echo "Verifying PostgreSQL tagged upgrades and current baseline replay on PostgreSQL/ClickHouse (Docker)..."
	@FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG='$(SCHEMA_VERIFY_FROM_TAG)' $(CONTRACT_GO_TEST) cli postgres/cli -tags schema_verify -run 'TestComposeUsesSchemaHarnessImages|TestPostgresServiceDatabaseInitialization|TestPostgresIntrospectionCoversDeployRelevantObjects|TestPostgresBaselineEqualsReplay|TestPostgresTaggedBaselineUpgradeEqualsCurrent|TestPostgresDemoSeedAppliesToCurrentBaseline' -count=1 -timeout 1200s ./pkg/provisioner/
	@FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG='$(SCHEMA_VERIFY_FROM_TAG)' $(CONTRACT_GO_TEST) cli clickhouse/cli -tags schema_verify -run 'TestClickHouseBaselineEqualsReplay|TestClickHouseTaggedBaselineUpgradeEqualsCurrent|TestClickHouseDemoSeedAndMeteringQueries' -count=1 -timeout 1200s ./pkg/provisioner/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-handlers -tags schema_verify -run 'TestProcessUsageSummaryAbsentDimensions_RealPG|TestProviderWebhookInboxRepository_RealPG|TestCryptoTaxDocuments_RealPG|TestEmbeddedFacilitatorSerializesRelayerNoncesAcrossReplicas_RealPG|TestInvoiceEmailOutboxLifecycleAndReads_RealPG|TestInvoiceEmailOverdueBalanceRead_RealPG|TestOperationalDatabaseGuards_RealPG|TestInvoiceCollectionMinimumSerializesAndPersists_RealPG|TestInvoiceRatingRepository_RealPG' -count=1 -timeout 600s ./internal/handlers/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-grpc -tags schema_verify -run 'TestBillingTransitionsSerializeAndPreserveCredit_RealPG|TestBillingEventOutboxLifecycle_RealPG|TestTierCatalogReads_RealPG|TestSubscriptionLifecycleRepository_RealPG|TestAccountOnboardingConvergence_RealPG|TestPrepaidBalanceRepository_RealPG|TestGRPCQueryPack_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG' -count=1 -timeout 600s ./internal/database/purserdb/
	@$(CONTRACT_GO_TEST) api_dns postgres/navigator-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG' -count=1 -timeout 600s ./internal/database/navigatordb/
	@$(CONTRACT_GO_TEST) api_dns postgres/navigator-store -tags schema_verify -run 'TestNavigatorStoreQueryPack_RealPG' -count=1 -timeout 600s ./internal/store/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestCrawlJobCatalog_RealPG' -count=1 -timeout 600s ./internal/database/skipperdb/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-conversations -tags schema_verify -run 'TestConversationQueryPack_RealPG' -count=1 -timeout 600s ./internal/chat/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-metering -tags schema_verify -run 'TestUsagePublicationRepository_RealPG' -count=1 -timeout 600s ./internal/metering/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-reports -tags schema_verify -run 'TestReportRepository_RealPG' -count=1 -timeout 600s ./internal/heartbeat/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-social -tags schema_verify -run 'TestSocialPostRepository_RealPG' -count=1 -timeout 600s ./internal/social/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-baselines -tags schema_verify -run 'TestBaselineRepository_RealPG' -count=1 -timeout 600s ./internal/diagnostics/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-pagecache -tags schema_verify -run 'TestPageCacheRepository_RealPG' -count=1 -timeout 600s ./internal/knowledge/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-knowledge -tags schema_verify -run 'TestKnowledgeRepository_RealPG' -count=1 -timeout 600s ./internal/knowledge/
	@$(CONTRACT_GO_TEST) api_analytics_query postgres/periscope-metering -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestMeteringStateTransitions_RealPG' -count=1 -timeout 600s ./internal/database/meteringdb/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-bootstrap -tags schema_verify -run 'TestBootstrapAccountsRepository_RealPG|TestBootstrapPullStreamsRepository_RealPG|TestBootstrapMistNativeRepository_RealPG' -count=1 -timeout 600s ./internal/bootstrap/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestManualQueryAdapters_RealPG|TestAccountSessionRepository_RealPG|TestAccountRecoveryWalletRepository_RealPG' -count=1 -timeout 600s ./internal/database/commodoredb/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-outboxes -tags schema_verify -run 'TestDurableOutboxRepositories_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-stream-cleanup -tags schema_verify -run 'TestStreamCleanupOutboxLoop_DeliveryOutageConverges_RealPG|TestStreamThumbnailCleanup_DispatchesEveryOwningCell_RealPG|TestStreamThumbnailCleanup_HangingCellDoesNotStarveSiblings_RealPG|TestRecordStreamActiveCluster_ServiceOnly_DoesNotTouchServingSet_RealPG|TestRegisterStreamThumbnailServingCell_FencesOnDeletion_RealPG|TestRegisterVsDeleteStream_Linearizes_RealPG|TestStreamCleanupOutbox_ThumbnailPhaseMarkedThenSkipped_RealPG|TestDeleteStream_RoutesToEveryServingCell_RealPG|TestClaimStreamCleanupOutboxBatch_TenantFencedLease_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-signing-keys -tags schema_verify -run 'TestSigningKeyRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-playback-policy -tags schema_verify -run 'TestPlaybackPolicyRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-artifact-intents -tags schema_verify -run 'TestArtifactCreationIntentRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-artifact-catalog -tags schema_verify -run 'TestUpdateArtifactCatalogSnapshot_ServingClusterEqualRevisionRepair_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-media-retention -tags schema_verify -run 'TestMediaRetentionRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-pull-source-events -tags schema_verify -run 'TestPullSourceEventRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-api-tokens -tags schema_verify -run 'TestAPITokenRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-native-auth -tags schema_verify -run 'TestNativeAuthorizationRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-stripe -tags schema_verify -run 'TestStripeMeterEventRepository_RealPG' -count=1 -timeout 600s ./internal/stripe/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-billing -tags schema_verify -run 'TestLoadEffectiveTierPartialOverrides_RealPG' -count=1 -timeout 600s ./internal/billing/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-tieraccess -tags schema_verify -run 'TestTierAccessEligibilityQuery_RealPG' -count=1 -timeout 600s ./internal/tieraccess/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-bootstrap -tags schema_verify -run 'TestBootstrapPricingRepositories_RealPG|TestBootstrapCustomerBillingRepository_RealPG|TestBootstrapTierCatalogRepository_RealPG|TestTierStripeSyncRepository_RealPG' -count=1 -timeout 600s ./internal/bootstrap/

# Granular subsets of the suite above, for iterating on one engine without the full run.
verify-navigator-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-navigator-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Navigator's generated query catalog and store behavior on PostgreSQL (Docker)..."
	@$(CONTRACT_GO_TEST) api_dns postgres/navigator-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG' -count=1 -timeout 600s ./internal/database/navigatordb/
	@$(CONTRACT_GO_TEST) api_dns postgres/navigator-store -tags schema_verify -run 'TestNavigatorStoreQueryPack_RealPG' -count=1 -timeout 600s ./internal/store/

verify-skipper-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-skipper-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Skipper's converted repositories on PostgreSQL (Docker)..."
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestCrawlJobCatalog_RealPG' -count=1 -timeout 600s ./internal/database/skipperdb/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-conversations -tags schema_verify -run 'TestConversationQueryPack_RealPG' -count=1 -timeout 600s ./internal/chat/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-metering -tags schema_verify -run 'TestUsagePublicationRepository_RealPG' -count=1 -timeout 600s ./internal/metering/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-reports -tags schema_verify -run 'TestReportRepository_RealPG' -count=1 -timeout 600s ./internal/heartbeat/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-social -tags schema_verify -run 'TestSocialPostRepository_RealPG' -count=1 -timeout 600s ./internal/social/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-baselines -tags schema_verify -run 'TestBaselineRepository_RealPG' -count=1 -timeout 600s ./internal/diagnostics/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-pagecache -tags schema_verify -run 'TestPageCacheRepository_RealPG' -count=1 -timeout 600s ./internal/knowledge/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-knowledge -tags schema_verify -run 'TestKnowledgeRepository_RealPG' -count=1 -timeout 600s ./internal/knowledge/

verify-periscope-metering-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-periscope-metering-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Periscope Metering's generated PostgreSQL catalog and state transitions (Docker)..."
	@$(CONTRACT_GO_TEST) api_analytics_query postgres/periscope-metering -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestMeteringStateTransitions_RealPG' -count=1 -timeout 600s ./internal/database/meteringdb/

verify-commodore-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-commodore-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Commodore's converted repositories on PostgreSQL (Docker)..."
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-bootstrap -tags schema_verify -run 'TestBootstrapAccountsRepository_RealPG|TestBootstrapPullStreamsRepository_RealPG|TestBootstrapMistNativeRepository_RealPG' -count=1 -timeout 600s ./internal/bootstrap/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestManualQueryAdapters_RealPG|TestAccountSessionRepository_RealPG|TestAccountRecoveryWalletRepository_RealPG' -count=1 -timeout 600s ./internal/database/commodoredb/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-outboxes -tags schema_verify -run 'TestDurableOutboxRepositories_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-stream-cleanup -tags schema_verify -run 'TestStreamCleanupOutboxLoop_DeliveryOutageConverges_RealPG|TestStreamThumbnailCleanup_DispatchesEveryOwningCell_RealPG|TestStreamThumbnailCleanup_HangingCellDoesNotStarveSiblings_RealPG|TestRecordStreamActiveCluster_ServiceOnly_DoesNotTouchServingSet_RealPG|TestRegisterStreamThumbnailServingCell_FencesOnDeletion_RealPG|TestRegisterVsDeleteStream_Linearizes_RealPG|TestStreamCleanupOutbox_ThumbnailPhaseMarkedThenSkipped_RealPG|TestDeleteStream_RoutesToEveryServingCell_RealPG|TestClaimStreamCleanupOutboxBatch_TenantFencedLease_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-signing-keys -tags schema_verify -run 'TestSigningKeyRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-playback-policy -tags schema_verify -run 'TestPlaybackPolicyRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-artifact-intents -tags schema_verify -run 'TestArtifactCreationIntentRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-artifact-catalog -tags schema_verify -run 'TestUpdateArtifactCatalogSnapshot_ServingClusterEqualRevisionRepair_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-media-retention -tags schema_verify -run 'TestMediaRetentionRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-pull-source-events -tags schema_verify -run 'TestPullSourceEventRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-api-tokens -tags schema_verify -run 'TestAPITokenRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-native-auth -tags schema_verify -run 'TestNativeAuthorizationRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/

verify-schema-postgres:
	@echo "Verifying Postgres current baseline and tagged-release upgrade convergence (Docker)..."
	@cd cli && FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG='$(SCHEMA_VERIFY_FROM_TAG)' go test -tags schema_verify -run 'TestComposeUsesSchemaHarnessImages|TestPostgresServiceDatabaseInitialization|TestPostgresIntrospectionCoversDeployRelevantObjects|TestPostgresBaselineEqualsReplay|TestPostgresTaggedBaselineUpgradeEqualsCurrent|TestPostgresDemoSeedAppliesToCurrentBaseline' -count=1 -timeout 600s ./pkg/provisioner/
	@cd api_billing && go test -tags schema_verify -run 'TestProcessUsageSummaryAbsentDimensions_RealPG|TestProviderWebhookInboxRepository_RealPG|TestCryptoTaxDocuments_RealPG|TestEmbeddedFacilitatorSerializesRelayerNoncesAcrossReplicas_RealPG|TestInvoiceEmailOutboxLifecycleAndReads_RealPG|TestInvoiceEmailOverdueBalanceRead_RealPG|TestOperationalDatabaseGuards_RealPG|TestInvoiceCollectionMinimumSerializesAndPersists_RealPG|TestInvoiceRatingRepository_RealPG' -count=1 -timeout 600s ./internal/handlers/
	@cd api_billing && go test -tags schema_verify -run 'TestBillingTransitionsSerializeAndPreserveCredit_RealPG|TestBillingEventOutboxLifecycle_RealPG|TestTierCatalogReads_RealPG|TestSubscriptionLifecycleRepository_RealPG|TestAccountOnboardingConvergence_RealPG|TestPrepaidBalanceRepository_RealPG|TestGRPCQueryPack_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_billing && go test -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG' -count=1 -timeout 600s ./internal/database/purserdb/
	@cd api_dns && go test -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG' -count=1 -timeout 600s ./internal/database/navigatordb/
	@cd api_dns && go test -tags schema_verify -run 'TestNavigatorStoreQueryPack_RealPG' -count=1 -timeout 600s ./internal/store/
	@cd api_consultant && go test -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestCrawlJobCatalog_RealPG' -count=1 -timeout 600s ./internal/database/skipperdb/
	@cd api_consultant && go test -tags schema_verify -run 'TestConversationQueryPack_RealPG' -count=1 -timeout 600s ./internal/chat/
	@cd api_consultant && go test -tags schema_verify -run 'TestUsagePublicationRepository_RealPG' -count=1 -timeout 600s ./internal/metering/
	@cd api_consultant && go test -tags schema_verify -run 'TestReportRepository_RealPG' -count=1 -timeout 600s ./internal/heartbeat/
	@cd api_consultant && go test -tags schema_verify -run 'TestSocialPostRepository_RealPG' -count=1 -timeout 600s ./internal/social/
	@cd api_consultant && go test -tags schema_verify -run 'TestBaselineRepository_RealPG' -count=1 -timeout 600s ./internal/diagnostics/
	@cd api_consultant && go test -tags schema_verify -run 'TestPageCacheRepository_RealPG' -count=1 -timeout 600s ./internal/knowledge/
	@cd api_consultant && go test -tags schema_verify -run 'TestKnowledgeRepository_RealPG' -count=1 -timeout 600s ./internal/knowledge/
	@cd api_analytics_query && go test -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestMeteringStateTransitions_RealPG' -count=1 -timeout 600s ./internal/database/meteringdb/
	@cd api_control && go test -tags schema_verify -run 'TestBootstrapAccountsRepository_RealPG|TestBootstrapPullStreamsRepository_RealPG|TestBootstrapMistNativeRepository_RealPG' -count=1 -timeout 600s ./internal/bootstrap/
	@cd api_control && go test -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestManualQueryAdapters_RealPG|TestAccountSessionRepository_RealPG|TestAccountRecoveryWalletRepository_RealPG' -count=1 -timeout 600s ./internal/database/commodoredb/
	@cd api_control && go test -tags schema_verify -run 'TestDurableOutboxRepositories_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestStreamCleanupOutboxLoop_DeliveryOutageConverges_RealPG|TestStreamThumbnailCleanup_DispatchesEveryOwningCell_RealPG|TestStreamThumbnailCleanup_HangingCellDoesNotStarveSiblings_RealPG|TestRecordStreamActiveCluster_ServiceOnly_DoesNotTouchServingSet_RealPG|TestRegisterStreamThumbnailServingCell_FencesOnDeletion_RealPG|TestRegisterVsDeleteStream_Linearizes_RealPG|TestStreamCleanupOutbox_ThumbnailPhaseMarkedThenSkipped_RealPG|TestDeleteStream_RoutesToEveryServingCell_RealPG|TestClaimStreamCleanupOutboxBatch_TenantFencedLease_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestSigningKeyRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestPlaybackPolicyRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestArtifactCreationIntentRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestUpdateArtifactCatalogSnapshot_ServingClusterEqualRevisionRepair_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestMediaRetentionRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestPullSourceEventRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestAPITokenRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestNativeAuthorizationRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_billing && go test -tags schema_verify -run 'TestStripeMeterEventRepository_RealPG' -count=1 -timeout 600s ./internal/stripe/
	@cd api_billing && go test -tags schema_verify -run 'TestLoadEffectiveTierPartialOverrides_RealPG' -count=1 -timeout 600s ./internal/billing/
	@cd api_billing && go test -tags schema_verify -run 'TestTierAccessEligibilityQuery_RealPG' -count=1 -timeout 600s ./internal/tieraccess/
	@cd api_billing && go test -tags schema_verify -run 'TestBootstrapPricingRepositories_RealPG|TestBootstrapCustomerBillingRepository_RealPG|TestBootstrapTierCatalogRepository_RealPG|TestTierStripeSyncRepository_RealPG' -count=1 -timeout 600s ./internal/bootstrap/
	@echo "Verifying the real PostgreSQL admission-ledger proof drives exact Redis membership cleanup..."
	@cd api_balancing && go test -tags schema_verify -run 'TestMembershipTombstoneCleanup_PostgresProofToRedisPurge_RealPG' -count=1 -timeout 600s ./internal/federation/

verify-schema-yugabyte:
	@echo "Verifying supported Yugabyte baselines and runtime SQL capabilities (Docker)..."
	@$(CONTRACT_GO_TEST) cli yugabyte/schema -tags schema_verify -run 'TestYugabyteCurrentBaselinesAndCapabilities' -count=1 -timeout 1200s ./pkg/provisioner/

verify-yugabyte-ha:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-yugabyte-ha requires a running Docker daemon"; exit 1; }
	@echo "Verifying Yugabyte RF=3 smart-driver distribution, transaction retry, node loss, and recovery (Docker)..."
	@$(CONTRACT_GO_TEST) pkg yugabyte/ha -tags yugabyte_ha -run 'TestYugabyteSmartDriverThreeNodeHA' -count=1 -timeout 900s ./database/

verify-schema-clickhouse:
	@echo "Verifying Replicated ClickHouse baseline == baseline + post-floor migrations (Docker)..."
	@cd cli && FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG='$(SCHEMA_VERIFY_FROM_TAG)' go test -tags schema_verify -run 'TestComposeUsesSchemaHarnessImages|TestClickHouseBaselineEqualsReplay|TestClickHouseTaggedBaselineUpgradeEqualsCurrent|TestClickHouseDemoSeedAndMeteringQueries' -count=1 -timeout 600s ./pkg/provisioner/

verify-feature-registry:
	@echo "Validating docs/platform-features.yaml and checking generated renderers..."
	@cd scripts/registry && go run . -check
	@echo "✓ Feature registry verified"

# Regenerate the feature-registry artifacts in place (registry.json, feature-matrix.mdx,
# platform-capabilities.mdx). Run this after editing docs/platform-features.yaml, then commit.
generate-feature-registry:
	@cd scripts/registry && go run .

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
	pnpm run format:check
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
# ansible-molecule         run one role's Molecule default scenario.
# provision-hello          end-to-end wiring smoke against localhost.

ANSIBLE_DIR := ansible
ANSIBLE_REQUIREMENTS_REL := requirements.yml
ANSIBLE_CACHE_REL := .cache/collections
ANSIBLE_LOCAL_TEMP := $(CURDIR)/$(ANSIBLE_DIR)/.cache/tmp
ANSIBLE_HOME := $(CURDIR)/$(ANSIBLE_DIR)/.cache/ansible-home
ANSIBLE_ENV := ANSIBLE_LOCAL_TEMP=$(ANSIBLE_LOCAL_TEMP) ANSIBLE_HOME=$(ANSIBLE_HOME)
ANSIBLE_PLAYBOOKS := $(wildcard $(ANSIBLE_DIR)/playbooks/*.yml)
ANSIBLE_COLLECTION_ROOT := $(ANSIBLE_DIR)/collections/ansible_collections/frameworks/infra
ANSIBLE_MOLECULE_IMAGE ?= geerlingguy/docker-ubuntu2404-ansible:latest
ANSIBLE_MOLECULE_ROLES := yugabyte postgres redis clickhouse zookeeper kafka caddy compose_stack prometheus_stack privateer listmonk mistserver helmsman edge
ANSIBLE_MOLECULE_ENV := $(ANSIBLE_ENV) \
	ANSIBLE_COLLECTIONS_PATH=$(CURDIR)/$(ANSIBLE_DIR)/collections:$(CURDIR)/$(ANSIBLE_DIR)/.cache/collections \
	ANSIBLE_ROLES_PATH=$(CURDIR)/$(ANSIBLE_DIR)/.cache/roles \
	MOLECULE_NO_LOG=false \
	MOLECULE_DOCKER_IMAGE=$(ANSIBLE_MOLECULE_IMAGE)

ansible-galaxy-install:
	@echo "=== Installing Ansible collections ==="
	@mkdir -p $(ANSIBLE_DIR)/$(ANSIBLE_CACHE_REL) $(ANSIBLE_DIR)/.cache/roles $(ANSIBLE_LOCAL_TEMP) $(ANSIBLE_HOME)
	cd $(ANSIBLE_DIR) && $(ANSIBLE_ENV) ansible-galaxy collection install -r $(ANSIBLE_REQUIREMENTS_REL) -p $(ANSIBLE_CACHE_REL)
	@echo "=== Installing Ansible roles ==="
	cd $(ANSIBLE_DIR) && $(ANSIBLE_ENV) ansible-galaxy role install -r $(ANSIBLE_REQUIREMENTS_REL) --roles-path .cache/roles

ansible-lint: ansible-galaxy-install
	@echo "=== Linting frameworks.infra collection ==="
	cd $(ANSIBLE_DIR) && $(ANSIBLE_ENV) ansible-lint --profile=production \
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
		  $(ANSIBLE_ENV) ansible-playbook --syntax-check "$$relpb" \
			-i localhost, -c local \
		) || exit 1; \
	done

ansible-molecule: ansible-galaxy-install ansible-molecule-run

ansible-molecule-run:
ifndef ROLE
	$(error ROLE is required, e.g. make ansible-molecule ROLE=postgres)
endif
	@test -d "$(ANSIBLE_COLLECTION_ROOT)/roles/$(ROLE)/molecule/default" || \
		(echo "No molecule/default scenario for ROLE=$(ROLE)" >&2; exit 1)
	cd $(ANSIBLE_COLLECTION_ROOT)/roles/$(ROLE) && $(ANSIBLE_MOLECULE_ENV) molecule test -s default

ansible-molecule-all: ansible-galaxy-install
	@for role in $(ANSIBLE_MOLECULE_ROLES); do \
		echo "=== Molecule $$role ==="; \
		$(MAKE) ansible-molecule-run ROLE=$$role || exit 1; \
	done

provision-hello: ansible-galaxy-install
	@echo "=== Running hello-role smoke test ==="
	cd cli && go run ./internal/ansiblesmoke \
		-requirements ../$(ANSIBLE_DIR)/$(ANSIBLE_REQUIREMENTS_REL) \
		-playbook ../$(ANSIBLE_DIR)/playbooks/hello.yml \
		-cache-dir ../$(ANSIBLE_DIR)/$(ANSIBLE_CACHE_REL)
