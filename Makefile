.PHONY: verify-prepush verify-generated-contracts test-frontend-components build build-images build-bin-commodore build-bin-quartermaster build-bin-purser build-bin-decklog build-bin-foghorn build-bin-helmsman build-bin-periscope-ingest build-bin-periscope-query build-bin-periscope-metering build-bin-signalman build-bin-bridge build-bin-navigator build-bin-privateer build-bin-deckhand build-bin-steward build-bin-skipper build-bin-chandler build-bin-lookout build-bin-bosun build-bin-cli cli-embed-assets \
		build-image-commodore build-image-quartermaster build-image-purser build-image-decklog build-image-foghorn build-image-periscope-ingest build-image-periscope-query build-image-periscope-metering build-image-signalman build-image-bridge build-image-logbook test-logbook-image-health build-image-navigator build-image-deckhand build-image-steward build-image-skipper build-image-chandler build-image-lookout build-image-bosun \
		proto proto-check sqlc sqlc-check graphql graphql-events verify-graphql-events graphql-frontend graphql-tray graphql-all clean version install-tools verify test test-cli test-pkg test-topology test-crypto-evm test-dashboards test-commodore test-quartermaster test-purser test-decklog test-foghorn test-helmsman test-periscope-ingest test-periscope-query test-media-topology-real-clickhouse test-signalman test-bridge test-navigator test-privateer test-deckhand test-steward test-skipper test-chandler test-lookout test-bosun coverage env frontend-env tidy update outdated fmt format \
		lint lint-go lint-frontend lint-all lint-fix lint-report lint-analyze ci-local ci-local-go ci-local-frontend \
		validate-migrations verify-release-state test-release-state test-release-preflight release-preflight release-tag verify-schema verify-schema-migrations verify-schema-migrations-core verify-schema-postgres verify-navigator-db verify-lookout-db verify-bosun-db verify-skipper-db verify-periscope-metering-db verify-periscope-ingest-db verify-periscope-query-db verify-periscope-metering-chain verify-commodore-db verify-quartermaster-db verify-quartermaster-yugabyte-db verify-foghorn-db verify-foghorn-valkey verify-foghorn-test-selection verify-schema-yugabyte verify-schema-yugabyte-schema verify-schema-yugabyte-schema-isolated verify-schema-yugabyte-selection-contracts verify-schema-yugabyte-schema-contracts verify-yugabyte-services verify-yugabyte-services-isolated verify-yugabyte-service verify-yugabyte-database verify-yugabyte-shared-fixture verify-yugabyte-commodore-contracts verify-yugabyte-purser-contracts verify-yugabyte-navigator-contracts verify-yugabyte-skipper-contracts verify-yugabyte-lookout-contracts verify-yugabyte-bosun-contracts verify-yugabyte-quartermaster-contracts verify-yugabyte-periscope-metering-contracts verify-yugabyte-foghorn-contracts-a verify-yugabyte-foghorn-contracts-b verify-yugabyte-ha verify-schema-clickhouse verify-feature-registry generate-pricing-catalog verify-pricing-catalog seed-demo seed-demo-postgres seed-demo-clickhouse reset-demo-databases-plan reset-demo-databases release-plan test-release-plan \
		verify-backup-restore-postgres verify-backup-restore-clickhouse verify-backup-restore-yugabyte \
		dead-code-install dead-code-go dead-code-ts dead-code-report dead-code \
		ansible-galaxy-install ansible-lint ansible-yamllint ansible-test ansible-check ansible-molecule ansible-molecule-run ansible-molecule-all provision-hello

# Prefer annotated git tags like v1.2.3; fallback to describe or dev
VERSION ?= $(shell git describe --tags --match "v[0-9]*" --exact-match 2>/dev/null || git describe --tags --match "v[0-9]*" --dirty --always 2>/dev/null || echo "0.0.0-dev")
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
GO_BUILD_TAGS ?= nomsgpack
GO_TAG_FLAGS = $(if $(strip $(GO_BUILD_TAGS)),-tags=$(GO_BUILD_TAGS),)
SQLC_VERSION ?= v1.31.1
GOLANGCI_LINT_VERSION ?= v2.13.1

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
SERVICES = commodore quartermaster purser decklog foghorn helmsman periscope-ingest periscope-query periscope-metering signalman bridge navigator privateer deckhand steward skipper chandler lookout bosun

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
SERVICE_DIR_lookout = api_incidents
SERVICE_DIR_bosun = api_webhooks
SERVICE_DIR_cli = cli
SERVICE_DIR_pkg = pkg

define run-go-tests
	@echo "Running unit tests for $(1)..."
	@(cd $(2) && \
		go mod tidy && \
		go test $(GO_TAG_FLAGS) $(GO_TEST_FLAGS) $(GO_TEST_PACKAGES) -race -count=$(GO_TEST_COUNT))
endef

GO_TEST_PACKAGES ?= ./...
GO_TEST_FLAGS ?=
GO_TEST_COUNT ?= 1

proto:
	cd pkg/proto && make proto

proto-check:
	cd pkg/proto && make proto-check

# Domain event schemas must stay compatible with the latest release tag:
# public events at FILE level, internal events and the envelope at WIRE level.
proto-breaking:
	@test -n "$(SCHEMA_VERIFY_FROM_TAG)" || { echo "ERROR: no release tag reachable from HEAD (fetch tags first)"; exit 1; }
	cd pkg/proto && make proto-breaking AGAINST_TAG='$(SCHEMA_VERIFY_FROM_TAG)'

sqlc:
	cd api_billing && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd api_dns && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd api_consultant && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd api_analytics_query && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd api_control && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd api_tenants && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd api_balancing && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd api_incidents && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

sqlc-check: sqlc
	@git diff --exit-code -- api_billing/internal/database/purserdb
	@git diff --exit-code -- api_dns/internal/database/navigatordb
	@git diff --exit-code -- api_consultant/internal/database/skipperdb
	@git diff --exit-code -- api_analytics_query/internal/database/meteringdb
	@git diff --exit-code -- api_control/internal/database/commodoredb
	@git diff --exit-code -- api_tenants/internal/database/quartermasterdb
	@git diff --exit-code -- api_balancing/internal/database/foghorndb
	@git diff --exit-code -- api_incidents/internal/database/lookoutdb
	@test -z "$$(git status --porcelain --untracked-files=all -- api_billing/internal/database/purserdb api_dns/internal/database/navigatordb api_consultant/internal/database/skipperdb api_analytics_query/internal/database/meteringdb api_control/internal/database/commodoredb api_tenants/internal/database/quartermasterdb api_balancing/internal/database/foghorndb api_incidents/internal/database/lookoutdb)" || { \
		echo "ERROR: sqlc generated files are untracked or stale; run make sqlc"; \
		git status --short --untracked-files=all -- api_billing/internal/database/purserdb api_dns/internal/database/navigatordb api_consultant/internal/database/skipperdb api_analytics_query/internal/database/meteringdb api_control/internal/database/commodoredb api_tenants/internal/database/quartermasterdb api_balancing/internal/database/foghorndb api_incidents/internal/database/lookoutdb; \
		exit 1; \
	}

seed-demo: seed-demo-postgres seed-demo-clickhouse

seed-demo-postgres:
	@set -eu; \
	for db in quartermaster purser commodore foghorn periscope; do \
		echo "Seeding PostgreSQL database $$db..."; \
		docker compose exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -U "$$1" -d "$$1"' sh "$$db" < "pkg/database/sql/seeds/demo/postgres/$$db.sql"; \
	done
	@# The S3 descriptor one-shot only fills cluster rows that exist; the seed creates the media cluster rows, so establish it again now.
	@docker compose up -d --no-deps storage-init >/dev/null

seed-demo-clickhouse:
	@echo "Seeding ClickHouse database periscope..."
	@docker compose exec -T clickhouse clickhouse-client --multiquery < pkg/database/sql/seeds/demo/clickhouse_demo_data.sql

reset-demo-databases-plan:
	@./scripts/reset-demo-databases.sh --plan

reset-demo-databases:
	@./scripts/reset-demo-databases.sh

# The public event types gqlgen binds are generated from the event registry first.
graphql: graphql-events
	cd api_gateway && make graphql

# Public event GraphQL types and their gqlgen models, from the event registry
# (scripts/eventsgql rewrites the delimited blocks in pkg/graphql/schema.graphql
# and api_gateway/gqlgen.yml).
graphql-events:
	@cd scripts/eventsgql && go run . -repo ../..

# Generator golden tests, then fail when either block differs from the registry.
verify-graphql-events:
	@cd scripts/eventsgql && go test ./... -count=1
	@cd scripts/eventsgql && go run . -check -repo ../..

graphql-frontend:
	cd website_application && pnpm run gql:codegen

graphql-tray:
	./scripts/generate-swift-gql.sh

graphql-all: graphql graphql-frontend graphql-tray graphql-sdk

.PHONY: sdk-audit sdk-manifest generate-graphql-reference verify-graphql-reference verify-api-compat verify-schema-compat test-sdkcontract graphql-sdk graphql-sdk-ts test-sdk-ts \
	verify-sdk-generated sdk-release-gates sdk-version-sync generate-public-schema verify-public-schema verify-experimental-fields \
	generate-ops verify-ops

# Public SDK contract (pkg/graphql/public, docs/standards/graphql-deprecation.md).
# generate-public-schema writes pkg/graphql/public/schema.public.graphql, the
# schema without its @internal fields, which every SDK generator reads.
# generate-ops writes pkg/graphql/public/generated/: a default operation for
# every public root field and argument field no hand-written operation
# replaces (scripts/sdkcontract/defaultops.go has the rules).
# sdk-manifest rewrites the current line of majors/v<N>.json from all public
# operations; the compatibility checks read release schemas with git show, so
# they need the release tags fetched.
SDKCONTRACT = cd scripts/sdkcontract && go run .

generate-public-schema:
	@$(SDKCONTRACT) public-schema -repo ../..

verify-public-schema:
	@$(SDKCONTRACT) public-schema -repo ../.. -check

generate-ops: generate-public-schema
	@$(SDKCONTRACT) generate-ops -repo ../..

verify-ops:
	@$(SDKCONTRACT) generate-ops -repo ../.. -check

# Lists every @experimental field; fails once the pending release reaches a
# field's until release.
verify-experimental-fields:
	@$(SDKCONTRACT) experimental -repo ../..

sdk-audit:
	@$(SDKCONTRACT) audit -repo ../..

generate-graphql-reference:
	@$(SDKCONTRACT) reference -repo ../..

verify-graphql-reference:
	@$(SDKCONTRACT) reference -repo ../.. -check

sdk-manifest: generate-ops
	@$(SDKCONTRACT) manifest -repo ../..

verify-api-compat:
	@$(SDKCONTRACT) api-compat -repo ../..

verify-schema-compat:
	@$(SDKCONTRACT) schema-compat -repo ../..

test-sdkcontract:
	@cd scripts/sdkcontract && go mod tidy && go test ./... -count=1
	@$(SDKCONTRACT) audit -repo ../..

# SDK code generation. Each target regenerates one SDK from pkg/graphql/public
# and pkg/proto/events/public/v1; none touches api_gateway or the webapp.
SDK_BUF = go run github.com/bufbuild/buf/cmd/buf@v1.72.0

graphql-sdk: graphql-sdk-ts graphql-sdk-go graphql-sdk-py

SDK_GENERATED = pkg/graphql/public/schema.public.graphql pkg/graphql/public/generated pkg/graphql/public/majors sdk_conformance/operations.json \
	npm_api/src/generated sdk_go/generated.go sdk_go/operation_calls_gen_test.go sdk_go/manifest_gen.go sdk_go/events_gen.go \
	sdk_python/src/livepeer_frameworks/_generated

# Regenerates every SDK and fails when the committed generated code differs.
verify-sdk-generated: graphql-sdk
	@git diff --exit-code -- $(SDK_GENERATED) || { echo "ERROR: SDK generated code is stale; run make graphql-sdk"; exit 1; }
	@test -z "$$(git status --porcelain --untracked-files=all -- $(SDK_GENERATED))" || { \
		echo "ERROR: SDK generation produced untracked files; run make graphql-sdk and commit them"; \
		git status --short --untracked-files=all -- $(SDK_GENERATED); \
		exit 1; \
	}

# The SDK version lives in npm_api/package.json, which changesets bump; this
# copies it into the Go and Python packages (pnpm version-packages runs it).
sdk-version-sync:
	@$(SDKCONTRACT) emit -lang go -repo ../..
	@$(SDKCONTRACT) emit -lang py -repo ../..

# Every gate an SDK release needs; scripts/publish-packages.sh runs it before
# any language publishes. test-sdkcontract includes sdk-audit. The -check
# targets run before verify-sdk-generated, which regenerates the public schema
# and default operations in place.
sdk-release-gates: test-sdkcontract verify-public-schema verify-ops verify-graphql-reference verify-experimental-fields \
	verify-api-compat verify-schema-compat verify-sdk-generated \
	test-sdk-ts test-sdk-go sdk-py-venv test-sdk-py typecheck-sdk-py

graphql-sdk-ts: sdk-manifest
	@$(SDKCONTRACT) emit -lang ts -repo ../..
	@rm -rf npm_api/src/generated/proto
	@$(SDK_BUF) generate pkg/proto --template npm_api/buf.gen.yaml --path pkg/proto/events/public/v1
	@cd npm_api && pnpm run codegen >/dev/null

test-sdk-ts:
	@cd npm_api && pnpm run type-check && pnpm run lint && pnpm test

# Go SDK (sdk_go). sdk_go/tools/genclient runs genqlient and opens every
# generated union to members a newer server adds; it lives in a separate
# module so the generator's dependencies never enter the SDK's go.mod.
graphql-sdk-go: sdk-manifest
	@$(SDKCONTRACT) emit -lang go -repo ../..
	@cd sdk_go/tools && go run ./genclient ../genqlient.yaml

test-sdk-go:
	@cd sdk_go/tools && go test ./... -count=1
	@cd sdk_go && go mod tidy && go vet ./... && go test -race -count=1 ./...

# Python SDK (sdk_python)
# The dev venv at sdk_python/.venv holds the generators (ariadne-codegen,
# betterproto2-compiler) and the test tools. ariadne-codegen emits one client
# flavour per run and rejects subscriptions in a sync client, so the sync run
# reads a copy of the operations without subscriptions and the async run then
# writes the same models plus the subscription module into the same package.
# The async run reads a copy too, because pkg/graphql/public also holds the
# public schema, which is not an operation document. codegen/public_exports.py
# then writes the export list of livepeer_frameworks.graphql from the result.
SDK_PY_VENV = sdk_python/.venv
SDK_PY_GEN = sdk_python/src/livepeer_frameworks/_generated

sdk-py-venv: $(SDK_PY_VENV)/.installed

# The package version is read from the emitted manifest, so it exists first.
$(SDK_PY_GEN)/manifest.py:
	@$(SDKCONTRACT) emit -lang py -repo ../..

$(SDK_PY_VENV)/.installed: sdk_python/pyproject.toml $(SDK_PY_GEN)/manifest.py
	@python3 -m venv $(SDK_PY_VENV)
	@$(SDK_PY_VENV)/bin/pip install -q --upgrade pip
	@$(SDK_PY_VENV)/bin/pip install -q -e 'sdk_python[dev]'
	@touch $@

graphql-sdk-py: sdk-manifest sdk-py-venv
	@$(SDKCONTRACT) emit -lang py -repo ../..
	@rm -rf $(SDK_PY_GEN)/proto $(SDK_PY_GEN)/graphql sdk_python/build/sync-operations
	@PATH="$(CURDIR)/$(SDK_PY_VENV)/bin:$$PATH" $(SDK_BUF) generate pkg/proto --template sdk_python/buf.gen.yaml --path pkg/proto/events/public/v1
	@rm -rf sdk_python/build/async-operations
	@mkdir -p sdk_python/build/sync-operations/generated sdk_python/build/async-operations
	@cp -R pkg/graphql/public/fragments pkg/graphql/public/queries pkg/graphql/public/mutations sdk_python/build/sync-operations/
	@cp pkg/graphql/public/generated/fragments.graphql pkg/graphql/public/generated/queries.graphql \
		pkg/graphql/public/generated/mutations.graphql sdk_python/build/sync-operations/generated/
	@cp -R pkg/graphql/public/fragments pkg/graphql/public/queries pkg/graphql/public/mutations pkg/graphql/public/subscriptions \
		pkg/graphql/public/generated sdk_python/build/async-operations/
	@cd sdk_python && PYTHONPATH=codegen .venv/bin/ariadne-codegen client --config codegen/sync.toml >/dev/null
	@cd sdk_python && PYTHONPATH=codegen .venv/bin/ariadne-codegen client --config codegen/async.toml >/dev/null
	@cd sdk_python && .venv/bin/python codegen/public_exports.py
	@rm -rf sdk_python/build/sync-operations sdk_python/build/async-operations

test-sdk-py: sdk-py-venv
	@cd sdk_python && .venv/bin/pytest -q

typecheck-sdk-py: sdk-py-venv
	@cd sdk_python && .venv/bin/mypy && .venv/bin/mypy --strict examples

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

.PHONY: test-go-livepeer-dispatch test-go-livepeer-pkg-impact
test-go-livepeer-dispatch:
	node --test scripts/ci/notify-go-livepeer-pkg-bump.test.mjs
	@$(MAKE) --no-print-directory test-go-livepeer-pkg-impact

test-go-livepeer-pkg-impact:
	@impact_output="$$(mktemp)"; \
	trap 'rm -f "$$impact_output"' EXIT; \
	GITHUB_OUTPUT="$$impact_output" scripts/ci/go-livepeer-pkg-impact.sh deadbeefdeadbeefdeadbeefdeadbeefdeadbeef HEAD >/dev/null; \
	grep -qx 'affected=true' "$$impact_output"; \
	grep -qx 'pkg/go.mod' "$$impact_output"; \
	grep -qx 'scripts/ci/go-livepeer-pkg-impact.sh' "$$impact_output"; \
	: >"$$impact_output"; \
	GITHUB_OUTPUT="$$impact_output" scripts/ci/go-livepeer-pkg-impact.sh HEAD HEAD >/dev/null; \
	grep -qx 'affected=false' "$$impact_output"; \
	if ! git diff --quiet HEAD^ HEAD -- scripts/ci/go-livepeer-pkg-impact.sh; then \
		: >"$$impact_output"; \
		GITHUB_OUTPUT="$$impact_output" scripts/ci/go-livepeer-pkg-impact.sh HEAD^ HEAD >/dev/null; \
		grep -qx 'affected=true' "$$impact_output"; \
		grep -qx 'scripts/ci/go-livepeer-pkg-impact.sh' "$$impact_output"; \
	fi

# verify-prepush runs, locally and in the same order, the CI checks that unit
# tests and lint do not cover: the Generated contracts job and the frontend
# component job.
verify-prepush: verify-generated-contracts test-frontend-components

verify-generated-contracts:
	$(MAKE) --no-print-directory verify-config-annotations
	$(MAKE) --no-print-directory verify-graphql-reference
	$(MAKE) --no-print-directory verify-release-channels
	$(MAKE) --no-print-directory verify-graphql-events
	$(MAKE) --no-print-directory verify-swift-gql
	$(MAKE) --no-print-directory graphql-frontend
	$(MAKE) --no-print-directory verify-sdk-generated

test-frontend-components:
	pnpm --filter frameworks-frontend exec svelte-kit sync
	pnpm --filter frameworks-frontend test:components

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

build-image-chartroom:
	docker build -t frameworks-chartroom:$(VERSION) -f website_application/Dockerfile .

build-image-foredeck:
	docker build -t frameworks-foredeck:$(VERSION) -f website_marketing/Dockerfile .

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

build-image-periscope-ingest:
	docker build -t frameworks-periscope-ingest:$(VERSION) \
		$(call component_build_args,periscope-ingest,api_analytics_ingest) \
		-f api_analytics_ingest/Dockerfile .

build-image-periscope-query:
	docker build -t frameworks-periscope-query:$(VERSION) \
		$(call component_build_args,periscope-query,api_analytics_query) \
		--build-arg HEALTH_PORT=18004 \
		-f api_analytics_query/Dockerfile .

build-image-periscope-metering:
	docker build -t frameworks-periscope-metering:$(VERSION) \
		$(call component_build_args,periscope-metering,api_analytics_query) \
		--build-arg CMD_PACKAGE=./cmd/periscope-metering \
		--build-arg HEALTH_PORT=18021 \
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

test-logbook-image-health: build-image-logbook
	./scripts/test-logbook-image-health.sh frameworks-logbook:$(VERSION)

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

build-image-lookout:
	docker build -t frameworks-lookout:$(VERSION) \
		$(call component_build_args,lookout,api_incidents) \
		-f api_incidents/Dockerfile .

build-image-bosun:
	docker build -t frameworks-bosun:$(VERSION) \
		$(call component_build_args,bosun,api_webhooks) \
		-f api_webhooks/Dockerfile .

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

build-bin-lookout:
	cd api_incidents && go build $(GO_TAG_FLAGS) $(call component_ldflags,lookout,api_incidents) -o ../bin/lookout ./cmd/lookout

build-bin-bosun:
	cd api_webhooks && go build $(GO_TAG_FLAGS) $(call component_ldflags,bosun,api_webhooks) -o ../bin/bosun ./cmd/bosun

# The CLI embeds ansible/ and config/skipper (cli/internal/runtimeassets) so a
# released binary runs from any directory; the cli module cannot embed paths
# above cli/, so they are staged into the module first.
cli-embed-assets:
	@./scripts/cli-embed-assets.sh

build-bin-cli: cli-embed-assets
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
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

test: cli-embed-assets
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

test-cli: cli-embed-assets
	$(call run-go-tests,cli,$(SERVICE_DIR_cli))

test-pkg:
	$(call run-go-tests,pkg,$(SERVICE_DIR_pkg))

.PHONY: verify-mist-protocols
.PHONY: verify-mist-source-admission
.PHONY: verify-mist-source-credential
.PHONY: verify-mist-viewer-credentials
.PHONY: verify-mist-current-source
verify-mist-current-source:
	@bash $(CURDIR)/scripts/verify-mist-current-source.sh

verify-mist-viewer-credentials:
	@$(MAKE) --no-print-directory test-pkg GO_TAG_FLAGS=-tags=media_verify GO_TEST_PACKAGES=./mist GO_TEST_FLAGS='-run TestViewerCredentials_RealMist -v -timeout 150s'

verify-mist-source-admission:
	@$(MAKE) --no-print-directory test-pkg GO_TAG_FLAGS=-tags=media_verify GO_TEST_PACKAGES=./mist GO_TEST_FLAGS='-run TestSourceAdmission_RealMist -v -timeout 150s'

# An accepted source pull is authorized by a per-attempt credential carried on
# the DTSC source URL. The origin only sees it if Mist forwards that query
# argument into the play command, so the image under test has to prove it.
verify-mist-source-credential:
	@$(MAKE) --no-print-directory test-pkg GO_TAG_FLAGS=-tags=media_verify GO_TEST_PACKAGES=./mist GO_TEST_FLAGS='-run TestSourcePullCredential_RealMist -v -timeout 180s'

verify-mist-protocols:
	@$(MAKE) --no-print-directory test-pkg GO_TAG_FLAGS=-tags=media_verify GO_TEST_PACKAGES=./mist GO_TEST_FLAGS='-run TestIngestProtocolReport_RealMist -v -timeout 120s'

# Real RTMP/SRT publishers against the image under test: Mist must append its own
# connector name as the 4th PUSH_REWRITE line (placement ingest admission input).
verify-mist-push-connector:
	@$(MAKE) --no-print-directory test-pkg GO_TAG_FLAGS=-tags=media_verify GO_TEST_PACKAGES=./mist GO_TEST_FLAGS='-run TestPushRewriteConnector_RealMist -v -timeout 180s'

verify-mist-finite-source:
	@$(MAKE) --no-print-directory test-pkg GO_TAG_FLAGS=-tags=media_verify GO_TEST_PACKAGES=./mist GO_TEST_FLAGS='-run TestFiniteLiveSource_RealMist -v -timeout 180s'

.PHONY: verify-mist-finite-source verify-mist-hls-realtime verify-mist-processing-recording
verify-mist-processing-recording:
	@$(MAKE) --no-print-directory test-pkg GO_TAG_FLAGS=-tags=media_verify GO_TEST_PACKAGES=./mist GO_TEST_FLAGS='-run TestProcessingRecording_RealMist -v -timeout 240s'

verify-mist-hls-realtime:
	@$(MAKE) --no-print-directory test-pkg GO_TAG_FLAGS=-tags=media_verify GO_TEST_PACKAGES=./mist GO_TEST_FLAGS='-run TestHLSChapterRealtime_RealMist -v -timeout 150s'

# Two real cells on the dev compose stack (profiles edge and two-cell): publish into
# cell A, resolve viewers through cell B, assert media at B, then private-only
# refusal, capacity fallback, publisher reconnect and a control-plane outage. Needs
# Docker, a fresh dev volume (foghorn_b is created on first boot), frameworks-edge:dev
# staged by edge-dev-dist with a Mist that emits the PUSH_REWRITE connector line, and
# MEDIA_TOOLS_IMAGE set to an image with ffmpeg, python3 and pkill. Minutes, not a CI gate.
verify-two-cell-media:
	@bash $(CURDIR)/scripts/verify-two-cell-media.sh

# Media lifecycle with real media on the two-cell stack: live viewers on both
# cells, clip from live, DVR ledger + chapters, VOD upload -> processing ->
# playback, analytics rows + GraphQL summary. Runs on the stack
# verify-two-cell-media leaves behind; MEDIA_LIFECYCLE_BOOTSTRAP=1 runs that
# proof first. The Mist staged into frameworks-edge:dev must include the
# processing binaries (MistProcAV, MistProcThumbs); a build without them is refused.
verify-media-lifecycle:
	@bash $(CURDIR)/scripts/verify-media-lifecycle.sh

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

test-media-topology-real-clickhouse:
	@docker info >/dev/null 2>&1 || { echo "ERROR: test-media-topology-real-clickhouse requires a running Docker daemon"; exit 1; }
	@$(CONTRACT_GO_TEST) api_analytics_ingest clickhouse/media-topology-ingest -tags schema_verify -run 'TestSourceFactProjectionAndLedgerReplay_RealClickHouse' -count=1 -timeout 600s ./internal/handlers/
	@$(CONTRACT_GO_TEST) api_analytics_query clickhouse/media-topology-query -tags schema_verify -run 'TestClusterWorkloadSeparatesStorageFlowStockAndScope_RealClickHouse|TestFederationSummaryPreservesOperatorHistoryWithoutDualRollupDuplicates_RealClickHouse' -count=1 -timeout 600s ./internal/grpc/

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

test-lookout:
	$(call run-go-tests,lookout,$(SERVICE_DIR_lookout))

test-bosun:
	$(call run-go-tests,bosun,$(SERVICE_DIR_bosun))

# Run unit tests with JUnit XML output for Codecov Test Analytics
test-junit: cli-embed-assets
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

# Stages edge/dist for the dev compose edge image: Helmsman from source, pinned MistServer and
# Caddy release tarballs. EDGE_DEV_MIST_TAR=<install-tree tar.gz> stages a local Mist build.
# Build the image afterwards with `docker compose build edge`.
.PHONY: edge-dev-dist verify-compose-profiles
edge-dev-dist:
	@bash $(CURDIR)/scripts/edge-dev-dist.sh

# docker compose config for every COMPOSE_PROFILES preset the generated .env lists.
verify-compose-profiles:
	@bash $(CURDIR)/scripts/verify-compose-profiles.sh

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

.PHONY: fmt-check
fmt-check:
	@bash scripts/check-go-format.sh

# Matches CI lint jobs (Go formatting, lint-go and lint-frontend).
lint:
	@failed=0; \
	$(MAKE) fmt-check || failed=1; \
	$(MAKE) lint-go || failed=1; \
	$(MAKE) verify-db-transactions || failed=1; \
	$(MAKE) lint-frontend || failed=1; \
	if [ $$failed -eq 1 ]; then exit 1; fi

# Every explicit transaction replays through database.WithRetryablePostgresTx or documents why it cannot.
.PHONY: verify-db-transactions
verify-db-transactions:
	@$(CURDIR)/scripts/verify-db-transactions.sh

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
		(cd $$service_dir && GIT_WORK_TREE="$(CURDIR)" golangci-lint run --timeout=5m $$BASELINE_ARG ./...) || failed=1; \
	done; \
	if [ $$failed -eq 1 ]; then exit 1; fi

# Matches CI frontend-lint.
lint-frontend:
	@echo "Running frontend lint checks (pnpm lint + pnpm format:check)..."
	pnpm lint
	pnpm run format:check
	pnpm --dir website_docs check:links

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

test-release-preflight:
	@set -e; version=$$(awk '/^releases:/ { in_releases = 1; next } in_releases && /^[^[:space:]#]/ { exit } in_releases && /^[[:space:]]*-[[:space:]]+version:/ { version = $$3 } END { print version }' cli/internal/releases/catalog.yaml); \
		for target in "$$version" "$$version-rc1" "$$version-rc2"; do \
			scripts/release-preflight.sh "$$target"; \
		done; \
		for target in v0.0.0 v0.0.0-rc1 "$$version-RC1" "$$version-rc.1" "$$version-rc01"; do \
			if scripts/release-preflight.sh "$$target" >/dev/null 2>&1; then \
				echo "release preflight accepted unsupported target $$target" >&2; \
				exit 1; \
			fi; \
		done

release-preflight:
	@scripts/release-preflight.sh "$(RELEASE_VERSION)"

release-tag: release-preflight
	@if git show-ref --verify --quiet "refs/tags/$(RELEASE_VERSION)"; then \
		echo "release-tag: tag $(RELEASE_VERSION) already exists" >&2; \
		exit 1; \
	fi
	@git tag -a "$(RELEASE_VERSION)" -m "$(RELEASE_VERSION)"
	@echo "Created local tag $(RELEASE_VERSION). Push it explicitly after review."

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
SCHEMA_VERIFY_COMMON_TESTS := TestComposeUsesSchemaHarnessImages|TestSchemaContractEnginePin|TestSchemaVerifyFromTagIsRequiredInCI
SCHEMA_VERIFY_POSTGRES_TESTS := TestPurserViewsUseExplicitProjectionLists|TestPostgresServiceDatabaseInitialization|TestPostgresServiceDatabaseProbeQueries|TestPostgresIntrospectionCoversDeployRelevantObjects|TestPostgresServiceCapabilitiesExecute|TestPostgresBaselineEqualsReplay|TestPostgresTaggedBaselineUpgradeEqualsCurrent|TestPostgresDemoSeedAppliesToCurrentBaseline|TestArtifactPlaybackIndexUpgradeFromReleasedLower|TestCreationCommandCASMutualExclusion|TestPostgresServiceBaselinesApplyTwice|TestPostgresServiceDatabaseCompletesInterruptedBaseline|TestPostgresServiceDatabaseRefusesDivergentSchema|TestPostgresServiceDatabaseDropsReferenceOnFailedApply
SCHEMA_VERIFY_CLICKHOUSE_TESTS := TestClickHouseServiceCapabilitiesExecute|TestClickHouseDeliveryRollupContractSeedsAndRetainsDiscoveryThroughScheduledRefreshes|TestClickHouseBaselineEqualsReplay|TestClickHouseTaggedBaselineUpgradeEqualsCurrent|TestClickHouseDemoSeedAndMeteringQueries|TestArtifactEventsDedupedPreservesLegacyRows
MIGRATION_PRECHECK_REALPG_TESTS := TestMigrationPrecheckRefusesDuplicateFingerprints_RealPG
MIGRATION_PRECHECK_REALYB_TESTS := TestMigrationPrecheckRefusesDuplicateFingerprints_RealYugabyte
SCHEMA_VERIFY_POSTGRES_TESTS := $(SCHEMA_VERIFY_POSTGRES_TESTS)|$(MIGRATION_PRECHECK_REALPG_TESTS)
CLI_CMD_REALPG_TESTS := TestPostExpandReportAgainstQuartermasterRealPG
YUGABYTE_SCHEMA_DATABASES := bosun commodore foghorn lookout navigator periscope purser quartermaster skipper
SCHEMA_VERIFY_YUGABYTE_STATIC_TESTS := $(SCHEMA_VERIFY_COMMON_TESTS)|TestYugabyteDatabaseSelection
SCHEMA_VERIFY_YUGABYTE_COMPAT_TESTS := TestYugabyteTaggedMigrationPaths|TestYugabyteCurrentBaselinesAndCapabilities
SCHEMA_VERIFY_YUGABYTE_COMPLETION_TESTS := TestYugabyteServiceBaselineReapplyAndCompletion
SCHEMA_VERIFY_YUGABYTE_PREFLIGHT_TESTS := TestYugabyteRelayoutPreflightRehearsesDatabase
SCHEMA_VERIFY_YUGABYTE_DATABASE_TESTS := $(SCHEMA_VERIFY_YUGABYTE_COMPAT_TESTS)|$(SCHEMA_VERIFY_YUGABYTE_COMPLETION_TESTS)|$(SCHEMA_VERIFY_YUGABYTE_PREFLIGHT_TESTS)
SCHEMA_VERIFY_YUGABYTE_ENGINE_TESTS := TestYugabyteColocatedDDLAbortsAreRetryable|TestYugabyteDistributedOptOutSplits|TestYugabyteRelayoutMovesDatabaseIntoDeclaredLayout|TestYugabyteRelayoutCutoverRunsTheWindowFromPrepare|TestYugabyteRelayoutRefusesASchemaChangedAfterPrepare|TestYugabyteRelayoutRollbackRestoresOriginalDatabase|TestYugabyteRelayoutLeaseExcludesSecondOwner|TestYugabyteRelayoutTakeoverEndsTheStaleOwnersRemoteWork|TestYugabyteRelayoutWorkerAdmissionFailsClosed|TestYugabyteRelayoutRestoresADefaultACL|TestYugabyteRelayoutVerifyRejectsAChangedShadow|TestYugabyteRelayoutRollbackResumesAfterAnInterruptedRollback
SCHEMA_VERIFY_YUGABYTE_ENGINE_TESTS := $(SCHEMA_VERIFY_YUGABYTE_ENGINE_TESTS)|$(MIGRATION_PRECHECK_REALYB_TESTS)
SCHEMA_VERIFY_YUGABYTE_TESTS := TestYugabyteDatabaseSelection|$(SCHEMA_VERIFY_YUGABYTE_DATABASE_TESTS)|$(SCHEMA_VERIFY_YUGABYTE_ENGINE_TESTS)
SCHEMA_VERIFY_TESTS := $(SCHEMA_VERIFY_COMMON_TESTS)|$(SCHEMA_VERIFY_POSTGRES_TESTS)|$(SCHEMA_VERIFY_CLICKHOUSE_TESTS)|$(SCHEMA_VERIFY_YUGABYTE_TESTS)
YUGABYTE_RELAYOUT_REHEARSAL_TESTS := TestYugabyteRelayoutRehearsalThreeNodes
YUGABYTE_LAYOUT_BENCHMARK_TESTS := TestYugabyteColocatedWriteRateBenchmark
# `cluster backup create` / `cluster restore` round trips on real engines: dump, restore into a shadow, compare with
# the manifest, swap, roll back and finish (cli/pkg/provisioner/database_backup_realdb_test.go). They run from the
# verify-backup-restore-* targets, never from the SCHEMA_VERIFY_TESTS runs.
CLI_BACKUP_RESTORE_REALPG_TESTS := TestDatabaseBackupRestoreRoundTrip_RealPG
CLI_BACKUP_RESTORE_REALCH_TESTS := TestClickHouseBackupRestoreRoundTrip_RealClickHouse
CLI_BACKUP_RESTORE_REALYB_TESTS := TestDatabaseBackupRestoreRoundTrip_RealYugabyte
# Every schema_verify test in cli/pkg/provisioner, each named exactly once across the selectors that run it.
CLI_PROVISIONER_REAL_ENGINE_TESTS := $(SCHEMA_VERIFY_TESTS)|$(YUGABYTE_RELAYOUT_REHEARSAL_TESTS)|$(YUGABYTE_LAYOUT_BENCHMARK_TESTS)|$(CLI_BACKUP_RESTORE_REALPG_TESTS)|$(CLI_BACKUP_RESTORE_REALCH_TESTS)|$(CLI_BACKUP_RESTORE_REALYB_TESTS)
# CI sets CONTRACT_COVERAGE_DIR so these same test executions emit engine-specific profiles.
# Leaving it unset preserves the ordinary local targets without coverage artifacts.
CONTRACT_GO_TEST := $(CURDIR)/scripts/run-go-contract-test.sh
FOGHORN_CONTROL_REALPG_TESTS_BASE := TestSyncCompletePlacementRespectsDeletionWatermark_RealPG|TestClaimFreezeAttempt_RealPG|TestClaimFreezeAttempt_LedgerAtomicity_RealPG|TestFreezeConstraints_RealPG|TestChapterFinalizeNodeBinding_RealPG|TestThumbnailPublication_RealPG|TestPublishThumbnailAttemptTakesAssetLockBeforeAssignmentRow_RealPG|TestDeleteThumbnailControlRowsFollowsAssignmentThenPointerOrder_RealPG|TestDVRChapterMutationLockIsNamespacedAndTransactionScoped_RealPG|TestThumbnailCompletion_RealPG|TestThumbnailProjectionFence_RealPG|TestThumbnailProjectionRecoveryPoison_RealPG|TestThumbnailProjectionReassert_RealPG|TestThumbnailReassertClaim_LeaseAndLimit_RealPG|TestThumbnailServingClusterTriggersReprojection_RealPG|TestStreamCleanupSaga_TombstoneFences_RealPG|TestThumbnailRecoveryLease_RealPG|TestThumbnailPromoteVsDeleteLeak_RealPG|TestThumbnailPublishLease_RealPG|TestThumbnailPublishTokenFence_RealPG|TestThumbnailRecoveryRedrivesTokenizedPublishing_RealPG|TestThumbnailPublishingRequiresToken_RealPG|TestEnforceImmutableLocalBackend_RealPG|TestEnforceImmutableLocalBackend_ExactMatchNotNormalized_RealPG|TestEnforceImmutableLocalBackend_ConcurrentFirstBootRace_RealPG|TestEstablishOrEnforceLocalBackend_FirstBootAuthority_RealPG|TestIngestSessionIdentity_RealPG|TestIngestSessionCapacityAuthority_RealPG|TestIngestSessionCapacityRejectionRetiresPIDReuseIncumbent_RealPG|TestIngestSessionConcurrentCreate_RealPG|TestDVRCloseBeforeStartFence_RealPG|TestIngestSessionSchemaInvariants_RealPG|TestDVRRecheckGenerationScoped_RealPG|TestListUnstartedDVRIntents_RealPG|TestIngestSessionReuseAndStopClaim_RealPG|TestIngestSessionConcurrentDifferentSessions_RealPG|TestAdvisoryLockKeysAreValidPGText_RealPG|TestStopDVRForEndedSourceGenerationFence_RealPG|TestIngestSessionAlreadyEndedIdempotency_RealPG|TestIngestSessionConcurrentCrossNodeAdmission_RealPG|TestEndIngestSessionsForStreamEnd_ReapsLostCloseFencedByEventTime_RealPG|TestIngestSessionReaper_RealPG|TestIngestSessionReaper_BlipToleranceRealPG|TestIngestSessionReaper_RechecksAbsenceUnderRetireGuardRealPG|TestNeverProjectedSessionReaperQueuesInactiveProjectionRealPG|TestIngestCloseTombstone_RealPG|TestIngestCloseTombstoneRetiresPIDReuseIncumbent_RealPG|TestSourceProjectionRepairAllocatorKeyScoped_RealPG|TestFenceOfflineBackstop_RealPG|TestProjectSourceIfCurrent_RealPG|TestOfflineEffectDoesNotLockAdmissionAcrossIO_RealPG|TestOfflineEffectSupersededByReconnect_RealPG|TestOfflineEffectExhaustsRetryBudget_RealPG|TestProjectSourceFailureAbortsPendingSession_RealPG|TestPushRewriteRetry_IdempotentResumedProjection_RealPG|TestResumedProjectionRepairsWhenRegistryHoldsNewerRevision_RealPG|TestResumedProjectionRepairAllowsPurgedTerminalEffect_RealPG|TestAdmissionEffectApplyAndSupersede_RealPG|TestAdmissionEffectPoisonSettlesLegOnly_RealPG|TestActivePushTargetsRearmAcrossNodeReconnect_RealPG|TestAdmissionAckCompletesWhileWorkerPaused_RealPG|TestAdmissionClaimAffinityRoutesToAuthority_RealPG
COMMODORE_INGEST_CLAIM_REALPG_TESTS := TestValidateStreamKey_SameClusterCannotStealLiveClaim_RealPG|TestValidateStreamKey_OwnerRefreshIsNotAReservation_RealPG|TestValidateStreamKey_LapsedClaimIsReservable_RealPG|TestSyncActiveIngestPlacement_ReleaseRequiresOwnership_RealPG|TestSyncActiveIngestPlacement_RenewalRequiresOwnership_RealPG|TestSyncActiveIngestPlacement_RenewalEstablishesUnheldClaim_RealPG|TestClearStreamActiveCluster_CannotClearPushClaim_RealPG|TestClearStreamActiveCluster_ReleasesManagedClaimAfterSoftDelete_RealPG
DOMAIN_EVENT_OUTBOX_REALPG_TESTS := TestDomainEventOutbox_RealPG
BOSUN_REALPG_TESTS := TestBosunMirroredDuplicatesCollapse_RealPG|TestBosunTenantIsolation_RealPG|TestBosunLeaseReclaimNoDoubleDelivery_RealPG|TestBosunBackoffAndAutoDisable_RealPG|TestBosunReplayKeepsID_RealPG|TestBosunPruning_RealPG|TestBosunEndpointLimitAndSecrets_RealPG|TestBosunOffsetsCommitAfterTransaction_RealPG|TestBosunDeliveryAgainstTLSReceiver_RealPG|TestBosunInternalFailureSettles_RealPG|TestBosunPruneKeepsReplayedDelivery_RealPG
BOSUN_REALYB_TESTS := TestBosunLeaseReclaimNoDoubleDelivery_RealYugabyte|TestBosunBackoffAndAutoDisable_RealYugabyte|TestBosunInternalFailureSettles_RealYugabyte|TestBosunReplayKeepsID_RealYugabyte|TestBosunPruning_RealYugabyte
PERISCOPE_DOMAIN_EVENTS_REALCH_TESTS := TestDomainEventProjection_RealClickHouse|TestAPIUsageRootFieldSignatures_RealClickHouse
COMMODORE_QUERY_CATALOG_REALPG_TESTS :=TestGeneratedQueryCatalogPrepares_RealPG|TestDVRRegistrationSnapshotsReadyWebhookAuthority_RealPG|TestResolveVODByHashIdentifiesChapterParent_RealPG|TestChapterPlaybackAuthorityDataMigration_RealPG|TestChapterPlaybackAuthorityBackfillDoesNotOverwriteConcurrentPolicyCascade_RealPG|TestUpsertChapterPlaybackIDRejectsCrossTenantCollision_RealPG|TestArtifactCreationCommandAckLease_RealPG|TestManualQueryAdapters_RealPG|TestAccountSessionRepository_RealPG|TestAccountRecoveryWalletRepository_RealPG|TestFieldEncryptionSuppressesEveryMediaAuthorityTrigger_RealPG|TestPushTargetOwnershipQueriesEnforceOwnerOrTenantManager_RealPG|TestAdminAPITokenListing_RealPG|TestArtifactOriginBackfillMigration_RealPG
FOGHORN_CONTROL_REALPG_TESTS := $(FOGHORN_CONTROL_REALPG_TESTS_BASE)|TestDelayedSyncCannotMovePlacementClockBackward_RealPG|TestCapacityPendingRestreamRearmUsesBoundedStableRunCycle_RealPG|TestOfflineAckWaitHasIndependentBackoffAndDeadLetters_RealPG|TestAdmissionMistIDBindRequiresCurrentActivationAttempt_RealPG
FOGHORN_CONTROL_REALPG_TESTS := $(FOGHORN_CONTROL_REALPG_TESTS)|TestDVRRecordingSource_RealPG|TestDVRStartTimeAnchorsChapters_RealPG|TestRecordingReadyRequiresChapters_RealPG|TestTerminalChapterBackfill_RealPG|TestDVRParentStorageSettles_RealPG
FOGHORN_DOMAIN_EVENTS_REALPG_TESTS := TestStreamLifecycleDomainEvents_RealPG|TestArtifactTransitionDomainEvents_RealPG|TestRecordingLifecycleDomainEvents_RealPG|TestFoghornDomainEventRelay_RealPG|TestArtifactDeletedRecordedOncePerDeletion_RealPG|TestArtifactAggregateVersions_RealPG|TestArtifactAggregateVersionReplicaRace_RealPG
FOGHORN_CONTROL_REALPG_TESTS := $(FOGHORN_CONTROL_REALPG_TESTS)|$(FOGHORN_DOMAIN_EVENTS_REALPG_TESTS)
FOGHORN_CONTROL_REALPG_TESTS := $(FOGHORN_CONTROL_REALPG_TESTS)|TestProcessingFailureReachesFailed_RealPG|TestProcessingProgressWatchdogClock_RealPG
FOGHORN_JOBS_REALPG_TESTS := TestStaleFreezeCleanup_RealPG|TestPurgeOwnershipFilter_RealPG|TestFederatedPointerPurgeDefersActiveRestoreUntilCleanupSettlement_RealPG|TestFederatedPointerRecoveryDoesNotSerializeBehindSlowDestination_RealPG|TestDailyFederatedPointerPurgeDoesNotSerializeBehindSlowDestination_RealPG|TestStreamCleanupDrainer_ConvergesFromDurableRow_RealPG|TestStreamCleanupDrainer_LocallyBackedAliasSweepsLocally_RealPG|TestThumbnailLifecycleIntegration_RealPG|TestStreamCleanupDrainer_RepointGuardFailsClosed_RealPG|TestStreamCleanupDrainer_DelayedResweep_RealPG|TestStreamCleanupDrainer_FinalizeAtomicOnControlCleanupFailure_RealPG
FOGHORN_JOBS_REALPG_TESTS := $(FOGHORN_JOBS_REALPG_TESTS)|TestProcessingProgressWatchdogFailsHeartbeatingStall_RealPG
FOGHORN_FEDERATION_REALPG_TESTS := TestMembershipTombstoneCleanup_PostgresProofToRedisPurge_RealPG
FOGHORN_MEDIA_AUTHORITY_REALPG_TESTS := TestMediaAuthorityApplyRejectionsCommitAudit_RealPG|TestMediaAuthorityCollectionAndFetch_RealPG|TestMediaAuthorityRestoreFence_RealPG|TestMediaAuthorityTenantRevivalWithholdsObjects_RealPG|TestMediaAuthorityRecovery_RealPG
FOGHORN_MEDIA_AUTHORITY_REALYB_TESTS := TestMediaAuthorityRecovery_RealYugabyte
FOGHORN_QUERY_CATALOG_REALPG_TESTS := TestFoghornGeneratedQueryCatalogPrepares_RealPG|TestConfigSeedApplyAckOutboxSameVersionReplacement_RealPG|TestSourceProjectionRevisionMigrationSeedsDurableHighWater_RealPG|TestSourceProjectionAllocatorKeyScoped_RealPG|TestKeyScopedOrderingAllocators_RealPG|TestLegacyOrderingSequencesRemainBelowCounters_RealPG|TestFederatedArtifactLifecycleDataMigration_RealPG|TestFederatedPointerPurgeEligibilityDataMigrationPreservesAge_RealPG|TestFederatedPointerPurgeEligibilityUsesSessionTimezone_RealPG|TestPurgeableArtifactsIncludeOwnedChaptersAndExcludeFederatedPointers_RealPG|TestFederatedPointerPurgeRetainsSignedTombstoneFence_RealPG|TestTombstoneDuringFederatedPointerPurgePreservesRecoveryClock_RealPG|TestFederatedPointerEligibilityIgnoresOrdinaryMetadataWriters_RealPG|TestFederatedPointerFenceSerializesWithAuthorityProjection_RealPG|TestFailedFederatedPointerCleanupRemainsFencedAndReclaimable_RealPG|TestActiveAuthorityRestoresOnlyInterruptedStalePointerFence_RealPG|TestFederatedPointersAreCacheOnlyForCapacityAndStalePurge_RealPG|TestFederatedPointersCannotEnterOwnerDeletionPaths_RealPG|TestMintArtifactShellRemainsRemoteParentPointer_RealPG|TestArtifactNodePlacementSerializesAbsentRows_RealPG|TestArtifactDeletionRejectsReplayOlderThanPlacement_RealPG|TestMediaAuthorityLookupIndexes_RealPG|TestPushTargetStatusRejectsOlderEvent_RealPG|TestPushTargetStatusUnknownEventTimeUsesArrivalOrder_RealPG|TestDVRParentStorageBackfillMigration_RealPG
FOGHORN_QUERY_CATALOG_REALYB_TESTS_A := TestFoghornGeneratedQueryCatalogPrepares_RealYugabyte|TestConfigSeedApplyAckOutboxSameVersionReplacement_RealYugabyte|TestSourceProjectionRevisionMigrationSeedsDurableHighWater_RealYugabyte|TestSourceProjectionRepairAllocator_RealYugabyte|TestSourceProjectionAllocatorKeyScoped_RealYugabyte
FOGHORN_QUERY_CATALOG_REALYB_TESTS_B := TestKeyScopedOrderingAllocators_RealYugabyte|TestLegacyOrderingSequencesRemainBelowCounters_RealYugabyte|TestArtifactNodePlacementSerializesAbsentRows_RealYugabyte|TestArtifactDeletionRejectsReplayOlderThanPlacement_RealYugabyte
FOGHORN_QUERY_CATALOG_REALPG_TESTS := $(FOGHORN_QUERY_CATALOG_REALPG_TESTS)|TestPlacementAuthorityPairTenantAndVersionFences_RealPG|TestControlReplicaCapabilityLedger_RealPG|TestDVRDiagnosis_RealPG
FOGHORN_QUERY_CATALOG_REALYB_TESTS_B := $(FOGHORN_QUERY_CATALOG_REALYB_TESTS_B)|TestPlacementAuthorityPairTenantAndVersionFences_RealYugabyte|TestControlReplicaCapabilityLedger_RealYugabyte|TestDVRDiagnosis_RealYugabyte
FOGHORN_QUERY_CATALOG_REALYB_TESTS := $(FOGHORN_QUERY_CATALOG_REALYB_TESTS_A)|$(FOGHORN_QUERY_CATALOG_REALYB_TESTS_B)
COMMODORE_QUERY_CATALOG_REALYB_TESTS := TestGeneratedQueryCatalogPrepares_RealYugabyte|TestArtifactCreationCommandAckLease_RealYugabyte
COMMODORE_PLACEMENT_REALYB_TESTS_A := TestMediaPlacementRepository_RealYugabyte|TestMediaPlacementManagement_RealYugabyte|TestMediaPlacementOptions_RealYugabyte|TestMediaPlacementPreview_RealYugabyte
COMMODORE_PLACEMENT_REALYB_TESTS_B := TestMediaPlacementObjectPolicy_RealYugabyte|TestMediaPlacementObjectPolicyRetainedDefault_RealYugabyte|TestMediaPlacementDeliveryClaims_RealYugabyte|TestMediaPlacementDeadlineDelivery_RealYugabyte
COMMODORE_MEDIA_AUTHORITY_REALPG_TESTS := TestMediaAuthorityObligationQueue_RealPG|TestMediaAuthorityPublishesOnlyOnChange_RealPG|TestMediaAuthorityFollowsUse_RealPG|TestMediaAuthorityInUseDecision_RealPG|TestMediaAuthorityReconcileReissuesOncePerCompilerChange_RealPG|TestMediaAuthorityDeliveryQueue_RealPG|TestMediaAuthorityConvergenceInvariants_RealPG|TestMediaAuthorityRetention_RealPG
COMMODORE_MEDIA_AUTHORITY_REALPG_TESTS := $(COMMODORE_MEDIA_AUTHORITY_REALPG_TESTS)|TestMediaAuthorityFirstPublicationRechecksParent_RealPG|TestMediaAuthorityCompileFailurePreservesAccess_RealPG
COMMODORE_MEDIA_AUTHORITY_REALYB_TESTS_C := TestMediaAuthorityObligationQueue_RealYugabyte|TestMediaAuthorityPublishesOnlyOnChange_RealYugabyte|TestMediaAuthorityFollowsUse_RealYugabyte|TestMediaAuthorityDeliveryQueue_RealYugabyte|TestMediaAuthorityRetention_RealYugabyte|TestMediaAuthorityQueueIndexesUseRangeSharding_RealYugabyte
COMMODORE_MEDIA_AUTHORITY_REALYB_TESTS_D := TestMediaAuthorityInUseDecision_RealYugabyte|TestMediaAuthorityReconcileReissuesOncePerCompilerChange_RealYugabyte|TestMediaAuthorityConvergenceInvariants_RealYugabyte|TestMediaAuthorityFirstPublicationRechecksParent_RealYugabyte|TestMediaAuthorityCompileFailurePreservesAccess_RealYugabyte
COMMODORE_MEDIA_AUTHORITY_REALYB_TESTS := $(COMMODORE_MEDIA_AUTHORITY_REALYB_TESTS_C)|$(COMMODORE_MEDIA_AUTHORITY_REALYB_TESTS_D)
YUGABYTE_HA_TESTS := TestYugabyteSmartDriverThreeNodeHA
NAVIGATOR_QUERY_CATALOG_REALPG_TESTS := TestGeneratedQueryCatalogPrepares_RealPG|TestTenantEdgeApplyAckDeliveryFence_RealPG|TestTenantEdgeApplyAckTeardownSerialization_RealPG|TestTenantEdgeApplyAckClusterRevocationSerialization_RealPG|TestTenantAliasReactivationTeardownSerialization_RealPG|TestTenantBundleAuthoritySerialization_RealPG|TestTenantEdgeApplyAliasFKMigrationCleansOrphans_RealPG|TestNavigatorAutomaticMigrationPhasesConverge_RealPG|TestTenantTLSBundleRevisionExpandReadsLegacyRows_RealPG
NAVIGATOR_QUERY_CATALOG_REALYB_TESTS := TestGeneratedQueryCatalogPrepares_RealYugabyte|TestTenantEdgeApplyAckDeliveryFence_RealYugabyte|TestTenantEdgeApplyAckTeardownSerialization_RealYugabyte|TestTenantEdgeApplyAckClusterRevocationSerialization_RealYugabyte|TestTenantAliasReactivationTeardownSerialization_RealYugabyte|TestTenantBundleAuthoritySerialization_RealYugabyte|TestTenantEdgeApplyAliasFKMigrationCleansOrphans_RealYugabyte|TestNavigatorAutomaticMigrationPhasesConverge_RealYugabyte|TestTenantTLSBundleRevisionExpandReadsLegacyRows_RealYugabyte
NAVIGATOR_STORE_REALPG_TESTS := TestNavigatorStoreQueryPack_RealPG|TestNavigatorCustomDomainSingleLifecycle_RealPG|TestNavigatorDomainEventOutbox_RealPG
PURSER_PRESENTMENT_GRPC_REALPG_TESTS := TestCardTopupsCreditTheEURLockedAtCheckoutCreation_RealPG|TestUSDCDepositQuoteLocksEURAndAssetUnitsOnOneReferenceDate_RealPG|TestListBillingDocumentsUnionStatesChargedAndEURAmounts_RealPG|TestOnSessionInvoicePaymentRecordsTheInvoiceLockedEUR_RealPG
PURSER_TIER_FEATURES_REALPG_TESTS := TestTierFeatureFlagRemovalConverges_RealPG
QUARTERMASTER_CAPABILITIES_REALPG_TESTS := TestTenantClusterCapabilities_RealPG
QUARTERMASTER_CAPABILITIES_REALYB_TESTS := TestTenantClusterCapabilities_RealYugabyte
QUARTERMASTER_DOMAIN_EVENTS_REALPG_TESTS := TestQuartermasterDomainEventOutbox_RealPG|TestClusterAccessEventsCommitWithAccess_RealPG|TestConcurrentFirstGrantsRecordOneAssignment_RealPG|TestNodeFingerprintOperatorQueries_RealPG
COMMODORE_DOMAIN_EVENTS_REALPG_TESTS := TestCommodoreDomainEventOutbox_RealPG
PURSER_DOMAIN_EVENTS_REALPG_TESTS := TestPurserDomainEventOutbox_RealPG|TestMollieBillingEventsCommitWithTheirState_RealPG
FOGHORN_GRPC_REALPG_TESTS := TestRequestedUploadEventsCarryTheCaller_RealPG|TestVodImportRecordsSourceAndQueuesProcessing_RealPG
PURSER_HANDLERS_DOMAIN_EVENTS_REALPG_TESTS := TestStripeSubscriptionPaymentFailedRecordedOncePerCharge_RealPG
PURSER_PRESENTMENT_HANDLERS_REALPG_TESTS := TestUSDCDepositCreditsTheLockedEURAndDocumentsEURVAT_RealPG|TestX402QuoteLocksTheUSDRateForCreditAndDocument_RealPG|TestUSDInvoiceFinalizationPresentsAtTheFinalizationRate_RealPG|TestStripeSettlementFromChargeUpdatedAndRetry_RealPG|TestMollieSettlementReadsBalanceTransactionsBackToTheCursor_RealPG|TestPrepaidTopupReversalLedger_RealPG|TestLatePrepaidCryptoReceiptGoesToReviewWithoutCredit_RealPG|TestX402QuoteExpiryRefusesClaimAndRecordsLateSettlement_RealPG|TestAdvanceBilledTierUpgradeClosesThePeriodAndProratesTheBaseFee_RealPG|TestAdvanceBilledTierDowngradeCreditsTheExcessToThePrepaidBalance_RealPG|TestAdvanceBilledTierChangeReducesAnUnpaidPreviousBaseFeeInvoice_RealPG|TestAdvanceBilledTierChangeWaitsForAPendingPreviousBaseFeeCharge_RealPG|TestOffSessionInvoicePaymentsRecordTheInvoiceLockedEUR_RealPG|TestMollieSettlementRereadsLateRowsWithoutPinningTheCursor_RealPG|TestPurserInvoicedClusterMonthlyFeeProratesByActiveTime_RealPG|TestMollieFirstPaymentUsesThePresentmentCurrencyItWasCreatedUnder_RealPG
PURSER_PRESENTMENT_HANDLERS_REALPG_TESTS := $(PURSER_PRESENTMENT_HANDLERS_REALPG_TESTS)|TestExpiredCheckoutUnblocksPreviousBaseFee_RealPG|TestMollieCursorAdvancePreservesConcurrentRewind_RealPG
NAVIGATOR_STORE_REALYB_TESTS := TestNavigatorStoreQueryPack_RealYugabyte|TestNavigatorCustomDomainSingleLifecycle_RealYugabyte
FOGHORN_STATE_REALVALKEY_TESTS := TestRedisStateContracts_RealValkey|TestTenantCapacityContracts_RealValkey|TestTenantCapacityPlacementDeadline_RealValkey|TestTenantCapacityRenewRespectsCap_RealValkey|TestTenantCapacityBoundedCleanupCannotEraseReactivatedViewer_RealValkey|TestTenantCapacityTargetReconciledBeyondBoundedCleanup_RealValkey
FOGHORN_CONTROL_REALVALKEY_TESTS := TestStreamRegistryContracts_RealValkey|TestRelayGrantContracts_RealValkey
FOGHORN_FEDERATION_REALVALKEY_TESTS := TestFederationCacheContracts_RealValkey|TestPlacementReceipts_RealValkey

.PHONY: verify-commodore-placement-test-selection
verify-commodore-placement-test-selection:
	@./scripts/check-go-test-selection.sh api_control ./internal/grpc '$(COMMODORE_PLACEMENT_REALYB_TESTS_A)|$(COMMODORE_PLACEMENT_REALYB_TESTS_B)' '^TestMediaPlacement.*_RealYugabyte$$'
	@./scripts/check-go-test-selection.sh api_control ./internal/grpc '$(COMMODORE_MEDIA_AUTHORITY_REALPG_TESTS)' '^TestMediaAuthority.*_RealPG$$'
	@./scripts/check-go-test-selection.sh api_control ./internal/grpc '$(COMMODORE_MEDIA_AUTHORITY_REALYB_TESTS)' '^TestMediaAuthority.*_RealYugabyte$$'

verify-foghorn-test-selection: verify-commodore-placement-test-selection
	@./scripts/check-go-test-selection.sh cli ./pkg/provisioner '$(CLI_PROVISIONER_REAL_ENGINE_TESTS)' '^Test' schema_verify
	@./scripts/check-go-test-selection.sh api_control ./internal/grpc '$(COMMODORE_INGEST_CLAIM_REALPG_TESTS)' '^(TestValidateStreamKey_|TestSyncActiveIngestPlacement_|TestClearStreamActiveCluster_).*RealPG$$'
	@./scripts/check-go-test-selection.sh api_control ./internal/grpc '$(COMMODORE_PLACEMENT_ACTIVATION_REALPG_TESTS)' '^TestPlacementActivation.*_RealPG$$'
	@./scripts/check-go-test-selection.sh api_control ./internal/database/commodoredb '$(COMMODORE_QUERY_CATALOG_REALPG_TESTS)' 'RealPG$$'
	@./scripts/check-go-test-selection.sh api_balancing ./internal/control '$(FOGHORN_CONTROL_REALPG_TESTS)' 'RealPG$$'
	@./scripts/check-go-test-selection.sh api_balancing ./internal/grpc '$(FOGHORN_GRPC_REALPG_TESTS)' 'RealPG$$'
	@./scripts/check-go-test-selection.sh api_balancing ./internal/jobs '$(FOGHORN_JOBS_REALPG_TESTS)' 'RealPG$$'
	@./scripts/check-go-test-selection.sh api_balancing ./internal/federation '$(FOGHORN_FEDERATION_REALPG_TESTS)' 'RealPG$$'
	@./scripts/check-go-test-selection.sh api_balancing ./internal/mediaauthority '$(FOGHORN_MEDIA_AUTHORITY_REALPG_TESTS)' 'RealPG$$'
	@./scripts/check-go-test-selection.sh api_balancing ./internal/mediaauthority '$(FOGHORN_MEDIA_AUTHORITY_REALYB_TESTS)' 'RealYugabyte$$'
	@./scripts/check-go-test-selection.sh api_balancing ./internal/database/foghorndb '$(FOGHORN_QUERY_CATALOG_REALPG_TESTS)' 'RealPG$$'
	@./scripts/check-go-test-selection.sh api_balancing ./internal/database/foghorndb '$(FOGHORN_QUERY_CATALOG_REALYB_TESTS)' 'RealYugabyte$$'
	@./scripts/check-go-test-selection.sh api_dns ./internal/database/navigatordb '$(NAVIGATOR_QUERY_CATALOG_REALYB_TESTS)' 'RealYugabyte$$'
	@./scripts/check-go-test-selection.sh api_control ./internal/database/commodoredb '$(COMMODORE_QUERY_CATALOG_REALYB_TESTS)' 'RealYugabyte$$'
	@FRAMEWORKS_TEST_SELECTION_TAGS=yugabyte_ha ./scripts/check-go-test-selection.sh pkg ./database '$(YUGABYTE_HA_TESTS)' '^Test' yugabyte_ha
	@./scripts/check-go-test-selection.sh api_dns ./internal/database/navigatordb '$(NAVIGATOR_QUERY_CATALOG_REALPG_TESTS)' 'RealPG$$'
	@./scripts/check-go-test-selection.sh api_dns ./internal/store '$(NAVIGATOR_STORE_REALPG_TESTS)' 'RealPG$$'
	@./scripts/check-go-test-selection.sh api_dns ./internal/store '$(NAVIGATOR_STORE_REALYB_TESTS)' 'RealYugabyte$$'
	@./scripts/check-go-test-selection.sh api_webhooks ./internal/integration '$(BOSUN_REALYB_TESTS)' 'RealYugabyte$$'
	@./scripts/check-go-test-selection.sh api_tenants ./internal/grpc '$(QUARTERMASTER_CAPABILITIES_REALYB_TESTS)' '^TestTenantClusterCapabilities.*_RealYugabyte$$'
	@./scripts/check-go-test-selection.sh api_balancing ./internal/state '$(FOGHORN_STATE_REALVALKEY_TESTS)' 'RealValkey$$'
	@./scripts/check-go-test-selection.sh api_balancing ./internal/control '$(FOGHORN_CONTROL_REALVALKEY_TESTS)' 'RealValkey$$'
	@./scripts/check-go-test-selection.sh api_balancing ./internal/federation '$(FOGHORN_FEDERATION_REALVALKEY_TESTS)' 'RealValkey$$'

verify-schema: verify-foghorn-test-selection
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-schema requires a running Docker daemon (real-engine tests must run, not skip)"; exit 1; }
	@echo "Verifying schema convergence + real-engine behavior tests (Docker)..."
	@# Explicit -run list so ONLY the real-engine schema tests execute — the schema_verify build tag is
	@# additive and would otherwise also run the package's ordinary (untagged) unit tests.
	@cd cli && FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG='$(SCHEMA_VERIFY_FROM_TAG)' go test -tags schema_verify -run '$(SCHEMA_VERIFY_TESTS)' -count=1 -timeout 1200s ./pkg/provisioner/
	@echo "Running real-engine production-path freeze + chapter-auth + thumbnail-foundation tests (Docker: claim concurrency, ledger atomicity, NULL-safe constraints, finalize-node binding, thumbnail publish/completion, deletion-saga tombstone fences, HA recovery lease)..."
	@cd api_balancing && go test -tags schema_verify -run '$(FOGHORN_CONTROL_REALPG_TESTS)' -count=1 -timeout 600s ./internal/control/
	@echo "Running real-engine admission-ledger to Redis membership-cleanup proof (Docker: tenant/revision anti-join and exact tombstone purge)..."
	@cd api_balancing && go test -tags schema_verify -run '$(FOGHORN_FEDERATION_REALPG_TESTS)' -count=1 -timeout 600s ./internal/federation/
	@cd api_balancing && go test -tags schema_verify -run '$(FOGHORN_MEDIA_AUTHORITY_REALPG_TESTS)' -count=1 -timeout 600s ./internal/mediaauthority/
	@echo "Running real-engine production-path cleanup + purge-ownership + stream-cleanup drainer + thumbnail-lifecycle integration tests (Docker: multipart-vs-stale-freeze cleanup, ownership filter NULL semantics, durable stream-cleanup convergence + local-alias routing, full publish→delete→drain lifecycle)..."
	@cd api_balancing && go test -tags schema_verify -run '$(FOGHORN_JOBS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/jobs/
	@echo "Running real-engine Commodore two-phase deletion saga test (Docker: outbox claim/lease/finalize converges a stream deletion through a Foghorn delivery outage — coordination only, Foghorn RPCs faked)..."
	@cd api_control && go test -tags schema_verify -run 'TestStreamCleanupOutboxLoop_DeliveryOutageConverges_RealPG|TestUpdateArtifactCatalogSnapshot_ServingClusterEqualRevisionRepair_RealPG|TestStreamThumbnailCleanup_DispatchesEveryOwningCell_RealPG|TestRecordStreamActiveCluster_ServiceOnly_DoesNotTouchServingSet_RealPG|TestClearStreamActiveCluster_ReleasesManagedClaimAfterSoftDelete_RealPG|TestDeleteStream_RoutesToEveryServingCell_RealPG|TestRegisterStreamThumbnailServingCell_FencesOnDeletion_RealPG|TestStreamThumbnailCleanup_HangingCellDoesNotStarveSiblings_RealPG|TestRegisterVsDeleteStream_Linearizes_RealPG|TestStreamCleanupOutbox_ThumbnailPhaseMarkedThenSkipped_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@echo "Running real-engine ingest placement-claim ownership tests (Docker: same-cluster theft, reserve-vs-refresh, lapse, owner-fenced renew/release, cross-writer isolation)..."
	@cd api_control && go test -tags schema_verify -run '$(COMMODORE_INGEST_CLAIM_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@echo "Running real-engine account email outbox test (Docker: failed SMTP send retries to delivery, only the delivered link verifies, expired requests abandon)..."
	@cd api_control && go test -tags schema_verify -run 'TestAccountEmailOutbox_RetriesUntilDelivered_RealPG' -count=1 -timeout 600s ./internal/grpc/

verify-schema-migrations: verify-foghorn-test-selection verify-schema-migrations-core verify-schema-yugabyte


verify-backup-restore-postgres:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-backup-restore-postgres requires a running Docker daemon"; exit 1; }
	@$(CONTRACT_GO_TEST) cli postgres/cli-backup-restore -tags schema_verify -run '^($(CLI_BACKUP_RESTORE_REALPG_TESTS))$$' -count=1 -timeout 600s ./pkg/provisioner/

verify-backup-restore-clickhouse:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-backup-restore-clickhouse requires a running Docker daemon"; exit 1; }
	@$(CONTRACT_GO_TEST) cli clickhouse/cli-backup-restore -tags schema_verify -run '^($(CLI_BACKUP_RESTORE_REALCH_TESTS))$$' -count=1 -timeout 600s ./pkg/provisioner/

# Runs its own single-node engine listening on localhost, as the production commands address YSQL; the shared
# contract fixture advertises the container address instead.
verify-backup-restore-yugabyte:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-backup-restore-yugabyte requires a running Docker daemon"; exit 1; }
	@env -u FRAMEWORKS_YUGABYTE_TEST_CONTAINER $(CONTRACT_GO_TEST) cli yugabyte/cli-backup-restore -tags schema_verify -run '^($(CLI_BACKUP_RESTORE_REALYB_TESTS))$$' -count=1 -timeout 900s ./pkg/provisioner/

verify-schema-migrations-core: verify-foghorn-test-selection
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-schema-migrations requires a running Docker daemon"; exit 1; }
	@$(MAKE) --no-print-directory verify-placement-db
	@echo "Verifying PostgreSQL tagged upgrades and current baseline replay on PostgreSQL/ClickHouse (Docker)..."
	@FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG='$(SCHEMA_VERIFY_FROM_TAG)' $(CONTRACT_GO_TEST) cli postgres/cli -tags schema_verify -run '$(SCHEMA_VERIFY_COMMON_TESTS)|$(SCHEMA_VERIFY_POSTGRES_TESTS)' -count=1 -timeout 1200s ./pkg/provisioner/
	@$(CONTRACT_GO_TEST) cli postgres/cli-cmd -tags schema_verify -run '^($(CLI_CMD_REALPG_TESTS))$$' -count=1 -timeout 600s ./cmd/
	@FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG='$(SCHEMA_VERIFY_FROM_TAG)' $(CONTRACT_GO_TEST) cli clickhouse/cli -tags schema_verify -run '$(SCHEMA_VERIFY_COMMON_TESTS)|$(SCHEMA_VERIFY_CLICKHOUSE_TESTS)' -count=1 -timeout 1200s ./pkg/provisioner/
	@$(MAKE) --no-print-directory verify-backup-restore-postgres
	@$(MAKE) --no-print-directory verify-backup-restore-clickhouse
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-ingest-claims -tags schema_verify -run '$(COMMODORE_INGEST_CLAIM_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-query-catalog -tags schema_verify -run '$(COMMODORE_QUERY_CATALOG_REALPG_TESTS)' -count=1 -timeout 600s ./internal/database/commodoredb/
	@$(CONTRACT_GO_TEST) api_analytics_ingest clickhouse/periscope-ingest-writers -tags schema_verify -run 'TestEveryTypedWriterAppendsToCurrentClickHouse' -count=1 -timeout 600s ./internal/database/periscopeingestdb/
	@$(CONTRACT_GO_TEST) api_analytics_ingest clickhouse/periscope-metering-chain-ingest -tags schema_verify -run 'TestSourceFactProjectionAndLedgerReplay_RealClickHouse|TestMirroredRawTriggerProjectsOnce_RealClickHouse' -count=1 -timeout 600s ./internal/handlers/
	@$(CONTRACT_GO_TEST) api_analytics_ingest clickhouse/periscope-domain-events -tags schema_verify -run '$(PERISCOPE_DOMAIN_EVENTS_REALCH_TESTS)' -count=1 -timeout 600s ./internal/handlers/
	@$(CONTRACT_GO_TEST) api_analytics_query clickhouse/periscope-query -tags schema_verify -run 'TestBillingCatalogExecutesAgainstCurrentClickHouse' -count=1 -timeout 600s ./internal/database/periscopequerydb/
	@$(CONTRACT_GO_TEST) api_analytics_query clickhouse/periscope-query-rpc -tags schema_verify -run 'TestEveryRPCQuerySiteExecutesAgainstCurrentClickHouse|TestClusterWorkloadSeparatesStorageFlowStockAndScope_RealClickHouse|TestFederationSummaryPreservesOperatorHistoryWithoutDualRollupDuplicates_RealClickHouse|TestTenantDailyStatsIncludesRestreamEgressWithoutInventingViewers_RealClickHouse|TestStreamDailyAudienceUsesSessionEndDayAndExcludesEmptyGeo_RealClickHouse|TestDashboardRefreshPreservesBothPlanesAcrossOneSidedCorrections_RealClickHouse' -count=1 -timeout 1200s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_analytics_query clickhouse/periscope-metering-chain -tags schema_verify -run 'TestCrossEngineMeteringReplayLateCorrectionAndFencing_RealEngines' -count=1 -timeout 600s ./internal/handlers/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-handlers -tags schema_verify -run 'TestProcessUsageSummaryAbsentDimensions_RealPG|TestV3UsageRowsRemainImmutableOnConflictingReplay_RealPG|TestV2UsageEnvelopePersistsIdempotentlyOnV3Schema_RealPG|TestMeteringSourceRegionRemainsAuthoritative_RealPG|TestPrepaidUsageSettlementMatchesAppliedBalanceTransactions_RealPG|TestProviderWebhookInboxRepository_RealPG|TestCryptoTaxDocuments_RealPG|TestCryptoTaxDocumentAnomalyReopensAndResolves_RealPG|TestEmbeddedFacilitatorSerializesRelayerNoncesAcrossReplicas_RealPG|TestInvoiceEmailOutboxLifecycleAndReads_RealPG|TestInvoiceEmailOverdueBalanceRead_RealPG|TestOperationalDatabaseGuards_RealPG|TestPrepaidBalanceCurrencyRepairMigration_RealPG|TestInvoiceCollectionMinimumSerializesAndPersists_RealPG|TestInvoiceRatingRepository_RealPG|TestInvoiceSettlementSurvivesConcurrentConfirmations_RealPG|TestInvoiceSettlementWaitsForFullCoverage_RealPG|TestInvoiceSettlementRecomputesOnAlreadyConfirmedReplay_RealPG|TestMonthlyInvoiceReplaysAfterSerializationFailure_RealPG' -count=1 -timeout 600s ./internal/handlers/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-grpc -tags schema_verify -run 'TestBillingTransitionsSerializeAndPreserveCredit_RealPG|TestBillingEventOutboxLifecycle_RealPG|TestTierCatalogReads_RealPG|TestSubscriptionLifecycleRepository_RealPG|TestAccountOnboardingConvergence_RealPG|TestPrepaidBalanceRepository_RealPG|TestGRPCQueryPack_RealPG|TestTenantAdmissionStatus_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG' -count=1 -timeout 600s ./internal/database/purserdb/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-eur-ledger -tags schema_verify -run 'TestFXLookupReferenceAge_RealPG|TestFXSyncerFeedSelectionAndAgeGauge_RealPG|TestEURLedgerConversionDataMigration_RealPG|TestMoneyFXAndSettlementConstraints_RealPG|TestPresentmentCurrencyDerivationAndLock_RealPG' -count=1 -timeout 600s ./internal/fx/ ./internal/datamigrations/ ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-presentment-grpc -tags schema_verify -run '$(PURSER_PRESENTMENT_GRPC_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-domain-events -tags schema_verify -run '$(PURSER_DOMAIN_EVENTS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-handlers-domain-events -tags schema_verify -run '$(PURSER_HANDLERS_DOMAIN_EVENTS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/handlers/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-presentment-handlers -tags schema_verify -run '$(PURSER_PRESENTMENT_HANDLERS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/handlers/
	@$(CONTRACT_GO_TEST) api_dns postgres/navigator-query-catalog -tags schema_verify -run '$(NAVIGATOR_QUERY_CATALOG_REALPG_TESTS)' -count=1 -timeout 600s ./internal/database/navigatordb/
	@$(CONTRACT_GO_TEST) api_dns postgres/navigator-store -tags schema_verify -run '$(NAVIGATOR_STORE_REALPG_TESTS)' -count=1 -timeout 600s ./internal/store/
	@$(CONTRACT_GO_TEST) api_incidents postgres/lookout-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG' -count=1 -timeout 600s ./internal/database/lookoutdb/
	@$(CONTRACT_GO_TEST) api_incidents postgres/lookout-incidents -tags schema_verify -run 'TestLookoutIngestStateMachine_RealPG|TestLookoutIncidentActions_RealPG|TestLookoutIncidentRescope_RealPG|TestLookoutOwnershipIngestRace_RealPG' -count=1 -timeout 600s ./internal/incidents/
	@$(CONTRACT_GO_TEST) api_incidents postgres/lookout-delivery -tags schema_verify -run 'TestLookoutDeliveryOutboxTokenFencing_RealPG|TestLookoutDeliveryTerminalFailure_RealPG|TestLookoutDeliveryRetention_RealPG|TestLookoutOperatorActivityOutbox_RealPG' -count=1 -timeout 600s ./internal/notify/
	@$(CONTRACT_GO_TEST) api_incidents postgres/lookout-grpc -tags schema_verify -run 'TestLookoutGRPCAuthorization_RealPG' -count=1 -timeout 600s ./internal/grpcserver/
	@$(CONTRACT_GO_TEST) api_incidents postgres/lookout-ownership -tags schema_verify -run 'TestLookoutClusterOwnershipEvent_RealPG|TestLookoutStartupOwnershipReconcile_RealPG|TestLookoutClusterCreatedOwnership_RealPG' -count=1 -timeout 600s ./internal/ownership/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestCrawlJobCatalog_RealPG' -count=1 -timeout 600s ./internal/database/skipperdb/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-conversations -tags schema_verify -run 'TestConversationQueryPack_RealPG' -count=1 -timeout 600s ./internal/chat/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-metering -tags schema_verify -run 'TestUsagePublicationRepository_RealPG' -count=1 -timeout 600s ./internal/metering/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-reports -tags schema_verify -run 'TestReportRepository_RealPG' -count=1 -timeout 600s ./internal/heartbeat/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-social -tags schema_verify -run 'TestSocialPostRepository_RealPG' -count=1 -timeout 600s ./internal/social/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-baselines -tags schema_verify -run 'TestBaselineRepository_RealPG' -count=1 -timeout 600s ./internal/diagnostics/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-pagecache -tags schema_verify -run 'TestPageCacheRepository_RealPG' -count=1 -timeout 600s ./internal/knowledge/
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-knowledge -tags schema_verify -run 'TestKnowledgeRepository_RealPG|TestReapAbandonedCrawlJobsUnblocksTheSitemap_RealPG|TestReapAbandonedCrawlJobsLeavesLiveCrawlsAlone_RealPG|TestReapAbandonedCrawlJobsLeavesSettledJobsAlone_RealPG' -count=1 -timeout 600s ./internal/knowledge/
	@$(CONTRACT_GO_TEST) api_analytics_query postgres/periscope-metering -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestMeteringStateTransitions_RealPG' -count=1 -timeout 600s ./internal/database/meteringdb/
	@$(CONTRACT_GO_TEST) pkg postgres/domain-event-outbox -tags schema_verify -run '$(DOMAIN_EVENT_OUTBOX_REALPG_TESTS)' -count=1 -timeout 600s ./events/outbox/
	@$(CONTRACT_GO_TEST) api_webhooks postgres/bosun -tags schema_verify -run '$(BOSUN_REALPG_TESTS)' -count=1 -timeout 900s ./internal/...
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-bootstrap -tags schema_verify -run 'TestBootstrapAccountsRepository_RealPG|TestBootstrapPullStreamsRepository_RealPG|TestBootstrapMistNativeRepository_RealPG' -count=1 -timeout 600s ./internal/bootstrap/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-query-catalog -tags schema_verify -run '$(COMMODORE_QUERY_CATALOG_REALPG_TESTS)' -count=1 -timeout 600s ./internal/database/commodoredb/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-field-encryption -tags schema_verify -run 'TestFieldEncryptionKeysetSweepAndVerify_RealPG' -count=1 -timeout 600s ./internal/datamigrations/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-outboxes -tags schema_verify -run 'TestDurableOutboxRepositories_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-domain-events -tags schema_verify -run '$(COMMODORE_DOMAIN_EVENTS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-stream-cleanup -tags schema_verify -run 'TestStreamCleanupOutboxLoop_DeliveryOutageConverges_RealPG|TestStreamThumbnailCleanup_DispatchesEveryOwningCell_RealPG|TestStreamThumbnailCleanup_HangingCellDoesNotStarveSiblings_RealPG|TestRecordStreamActiveCluster_ServiceOnly_DoesNotTouchServingSet_RealPG|TestClearStreamActiveCluster_ReleasesManagedClaimAfterSoftDelete_RealPG|TestRegisterStreamThumbnailServingCell_FencesOnDeletion_RealPG|TestRegisterVsDeleteStream_Linearizes_RealPG|TestStreamCleanupOutbox_ThumbnailPhaseMarkedThenSkipped_RealPG|TestDeleteStream_RoutesToEveryServingCell_RealPG|TestClaimStreamCleanupOutboxBatch_TenantFencedLease_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-signing-keys -tags schema_verify -run 'TestSigningKeyRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-playback-policy -tags schema_verify -run 'TestPlaybackPolicyRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-artifact-intents -tags schema_verify -run 'TestArtifactCreationIntentRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-artifact-catalog -tags schema_verify -run 'TestUpdateArtifactCatalogSnapshot_ServingClusterEqualRevisionRepair_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-media-retention -tags schema_verify -run 'TestMediaRetentionRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-pull-source-events -tags schema_verify -run 'TestPullSourceEventRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-api-tokens -tags schema_verify -run 'TestAPITokenRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-native-auth -tags schema_verify -run 'TestNativeAuthorizationRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_tenants postgres/quartermaster-bootstrap -tags schema_verify -run 'TestBootstrapRepositoryReplay_RealPG' -count=1 -timeout 600s ./internal/bootstrap/
	@$(CONTRACT_GO_TEST) api_tenants postgres/quartermaster-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestPeerDiscoveryCanonicalControlCells_RealPG|TestManualQueryAdapters_RealPG|TestConvertedRuntimeAdapters_RealPG|TestDNSEntitlementExpandPreservesPaidAliasesUntilObservation_RealPG|TestListTenantEffectiveAccessUsesCanonicalActiveGrantPredicate_RealPG|TestMediaPlacementInventory_RealPG|TestMediaCapacityConsent_RealPG|TestMediaAuthorityRefreshCoalescing_RealPG|TestPrivateClusterControlCellFoghorns_RealPG|TestClusterControlCellReassignment_RealPG|TestServiceEventOutboxScopeAndLeaseToken_RealPG' -count=1 -timeout 600s ./internal/database/quartermasterdb/
	@$(CONTRACT_GO_TEST) api_tenants postgres/quartermaster-data-migrations -tags schema_verify -run 'TestTenantDNSEntitlementsRunAndVerifyRealPG|TestNodeIdentityKeyGateUsesRemediationOwnershipRealPG' -count=1 -timeout 600s ./internal/datamigrations/
	@$(CONTRACT_GO_TEST) api_tenants postgres/quartermaster-dns-entitlement-handoff -tags schema_verify -run 'TestCompleteTenantDNSEntitlementHandoffRealPG|TestCapacityConsentManagement_RealPG|TestPrivateClusterOwnershipLimitSerializes_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_tenants postgres/quartermaster-capabilities -tags schema_verify -run '$(QUARTERMASTER_CAPABILITIES_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_tenants postgres/quartermaster-domain-events -tags schema_verify -run '$(QUARTERMASTER_DOMAIN_EVENTS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_balancing postgres/foghorn-query-catalog -tags schema_verify -run '$(FOGHORN_QUERY_CATALOG_REALPG_TESTS)' -count=1 -timeout 600s ./internal/database/foghorndb/
	@$(CONTRACT_GO_TEST) api_balancing postgres/foghorn-control -tags schema_verify -run '$(FOGHORN_CONTROL_REALPG_TESTS)' -count=1 -timeout 600s ./internal/control/
	@$(CONTRACT_GO_TEST) api_balancing postgres/foghorn-grpc -tags schema_verify -run '$(FOGHORN_GRPC_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_balancing postgres/foghorn-jobs -tags schema_verify -run '$(FOGHORN_JOBS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/jobs/
	@$(CONTRACT_GO_TEST) api_balancing postgres/foghorn-federation -tags schema_verify -run '$(FOGHORN_FEDERATION_REALPG_TESTS)' -count=1 -timeout 600s ./internal/federation/
	@$(CONTRACT_GO_TEST) api_balancing postgres/foghorn-media-authority -tags schema_verify -run '$(FOGHORN_MEDIA_AUTHORITY_REALPG_TESTS)' -count=1 -timeout 600s ./internal/mediaauthority/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-stripe -tags schema_verify -run 'TestStripeMeterEventRepository_RealPG' -count=1 -timeout 600s ./internal/stripe/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-billing -tags schema_verify -run 'TestLoadEffectiveTierPartialOverrides_RealPG|TestPlacementTariffSnapshot_RealPG|TestPlacementPriceBoundaries_RealPG|TestPlacementAllowanceUsage_RealPG' -count=1 -timeout 600s ./internal/billing/ ./internal/pricing/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-tieraccess -tags schema_verify -run 'TestTierAccessEligibilityQuery_RealPG|TestDNSEntitlementUpgradeCatalogAndSuspendedLookup_RealPG' -count=1 -timeout 600s ./internal/tieraccess/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-bootstrap -tags schema_verify -run 'TestBootstrapPricingRepositories_RealPG|TestBootstrapCustomerBillingRepository_RealPG|TestBootstrapTierCatalogRepository_RealPG|TestTierStripeSyncRepository_RealPG' -count=1 -timeout 600s ./internal/bootstrap/
	@$(CONTRACT_GO_TEST) api_billing postgres/purser-tier-features -tags schema_verify -run '$(PURSER_TIER_FEATURES_REALPG_TESTS)' -count=1 -timeout 600s ./internal/bootstrap/

# Granular subsets of the suite above, for iterating on one engine without the full run.
verify-navigator-db: verify-foghorn-test-selection
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-navigator-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Navigator's generated query catalog and store behavior on PostgreSQL and YugabyteDB (Docker)..."
	@$(CONTRACT_GO_TEST) api_dns postgres/navigator-query-catalog -tags schema_verify -run '$(NAVIGATOR_QUERY_CATALOG_REALPG_TESTS)' -count=1 -timeout 600s ./internal/database/navigatordb/
	@$(CONTRACT_GO_TEST) api_dns postgres/navigator-store -tags schema_verify -run '$(NAVIGATOR_STORE_REALPG_TESTS)' -count=1 -timeout 600s ./internal/store/
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-navigator-contracts

verify-lookout-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-lookout-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Lookout's incident state machine and delivery outbox on PostgreSQL (Docker)..."
	@$(CONTRACT_GO_TEST) api_incidents postgres/lookout-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG' -count=1 -timeout 600s ./internal/database/lookoutdb/
	@$(CONTRACT_GO_TEST) api_incidents postgres/lookout-incidents -tags schema_verify -run 'TestLookoutIngestStateMachine_RealPG|TestLookoutIncidentActions_RealPG|TestLookoutIncidentRescope_RealPG|TestLookoutOwnershipIngestRace_RealPG' -count=1 -timeout 600s ./internal/incidents/
	@$(CONTRACT_GO_TEST) api_incidents postgres/lookout-delivery -tags schema_verify -run 'TestLookoutDeliveryOutboxTokenFencing_RealPG|TestLookoutDeliveryTerminalFailure_RealPG|TestLookoutDeliveryRetention_RealPG|TestLookoutOperatorActivityOutbox_RealPG' -count=1 -timeout 600s ./internal/notify/
	@$(CONTRACT_GO_TEST) api_incidents postgres/lookout-grpc -tags schema_verify -run 'TestLookoutGRPCAuthorization_RealPG' -count=1 -timeout 600s ./internal/grpcserver/
	@$(CONTRACT_GO_TEST) api_incidents postgres/lookout-ownership -tags schema_verify -run 'TestLookoutClusterOwnershipEvent_RealPG|TestLookoutStartupOwnershipReconcile_RealPG|TestLookoutClusterCreatedOwnership_RealPG' -count=1 -timeout 600s ./internal/ownership/

verify-bosun-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-bosun-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Bosun's webhook ledger, delivery leases, and Kafka offset ordering on PostgreSQL (Docker)..."
	@$(CONTRACT_GO_TEST) api_webhooks postgres/bosun -tags schema_verify -run '$(BOSUN_REALPG_TESTS)' -count=1 -timeout 900s ./internal/...

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
	@$(CONTRACT_GO_TEST) api_consultant postgres/skipper-knowledge -tags schema_verify -run 'TestKnowledgeRepository_RealPG|TestReapAbandonedCrawlJobsUnblocksTheSitemap_RealPG|TestReapAbandonedCrawlJobsLeavesLiveCrawlsAlone_RealPG|TestReapAbandonedCrawlJobsLeavesSettledJobsAlone_RealPG' -count=1 -timeout 600s ./internal/knowledge/

verify-periscope-metering-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-periscope-metering-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Periscope Metering's generated PostgreSQL catalog and state transitions (Docker)..."
	@$(CONTRACT_GO_TEST) api_analytics_query postgres/periscope-metering -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestMeteringStateTransitions_RealPG' -count=1 -timeout 600s ./internal/database/meteringdb/

verify-periscope-ingest-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-periscope-ingest-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying every Periscope Ingest typed writer and the domain event projections against current ClickHouse (Docker)..."
	@$(CONTRACT_GO_TEST) api_analytics_ingest clickhouse/periscope-ingest-writers -tags schema_verify -run 'TestEveryTypedWriterAppendsToCurrentClickHouse' -count=1 -timeout 600s ./internal/database/periscopeingestdb/
	@$(CONTRACT_GO_TEST) api_analytics_ingest clickhouse/periscope-domain-events -tags schema_verify -run '$(PERISCOPE_DOMAIN_EVENTS_REALCH_TESTS)' -count=1 -timeout 600s ./internal/handlers/

verify-periscope-query-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-periscope-query-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Periscope Query's service-owned ClickHouse catalog (Docker)..."
	@$(CONTRACT_GO_TEST) api_analytics_query clickhouse/periscope-query -tags schema_verify -run 'TestBillingCatalogExecutesAgainstCurrentClickHouse' -count=1 -timeout 600s ./internal/database/periscopequerydb/
	@$(CONTRACT_GO_TEST) api_analytics_query clickhouse/periscope-query-rpc -tags schema_verify -run 'TestEveryRPCQuerySiteExecutesAgainstCurrentClickHouse|TestClusterWorkloadSeparatesStorageFlowStockAndScope_RealClickHouse|TestFederationSummaryPreservesOperatorHistoryWithoutDualRollupDuplicates_RealClickHouse|TestTenantDailyStatsIncludesRestreamEgressWithoutInventingViewers_RealClickHouse|TestStreamDailyAudienceUsesSessionEndDayAndExcludesEmptyGeo_RealClickHouse|TestDashboardRefreshPreservesBothPlanesAcrossOneSidedCorrections_RealClickHouse' -count=1 -timeout 1200s ./internal/grpc/

verify-periscope-metering-chain:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-periscope-metering-chain requires a running Docker daemon"; exit 1; }
	@echo "Verifying the source-fact, final-fact, ledger, billing, cursor, and reservation chain on real ClickHouse/PostgreSQL (Docker)..."
	@$(CONTRACT_GO_TEST) api_analytics_ingest clickhouse/periscope-metering-chain-ingest -tags schema_verify -run 'TestSourceFactProjectionAndLedgerReplay_RealClickHouse|TestMirroredRawTriggerProjectsOnce_RealClickHouse' -count=1 -timeout 600s ./internal/handlers/
	@$(CONTRACT_GO_TEST) api_analytics_query clickhouse/periscope-metering-chain -tags schema_verify -run 'TestCrossEngineMeteringReplayLateCorrectionAndFencing_RealEngines' -count=1 -timeout 600s ./internal/handlers/
	@$(CONTRACT_GO_TEST) api_billing postgres/periscope-metering-v2-compat -tags schema_verify -run 'TestV3UsageRowsRemainImmutableOnConflictingReplay_RealPG|TestV2UsageEnvelopePersistsIdempotentlyOnV3Schema_RealPG|TestMeteringSourceRegionRemainsAuthoritative_RealPG|TestPrepaidUsageSettlementMatchesAppliedBalanceTransactions_RealPG' -count=1 -timeout 600s ./internal/handlers/

verify-commodore-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-commodore-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Commodore's converted repositories on PostgreSQL (Docker)..."
	@$(MAKE) --no-print-directory verify-placement-db
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-bootstrap -tags schema_verify -run 'TestBootstrapAccountsRepository_RealPG|TestBootstrapPullStreamsRepository_RealPG|TestBootstrapMistNativeRepository_RealPG' -count=1 -timeout 600s ./internal/bootstrap/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-query-catalog -tags schema_verify -run '$(COMMODORE_QUERY_CATALOG_REALPG_TESTS)' -count=1 -timeout 600s ./internal/database/commodoredb/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-field-encryption -tags schema_verify -run 'TestFieldEncryptionKeysetSweepAndVerify_RealPG' -count=1 -timeout 600s ./internal/datamigrations/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-outboxes -tags schema_verify -run 'TestDurableOutboxRepositories_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-domain-events -tags schema_verify -run '$(COMMODORE_DOMAIN_EVENTS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-stream-cleanup -tags schema_verify -run 'TestStreamCleanupOutboxLoop_DeliveryOutageConverges_RealPG|TestStreamThumbnailCleanup_DispatchesEveryOwningCell_RealPG|TestStreamThumbnailCleanup_HangingCellDoesNotStarveSiblings_RealPG|TestRecordStreamActiveCluster_ServiceOnly_DoesNotTouchServingSet_RealPG|TestClearStreamActiveCluster_ReleasesManagedClaimAfterSoftDelete_RealPG|TestRegisterStreamThumbnailServingCell_FencesOnDeletion_RealPG|TestRegisterVsDeleteStream_Linearizes_RealPG|TestStreamCleanupOutbox_ThumbnailPhaseMarkedThenSkipped_RealPG|TestDeleteStream_RoutesToEveryServingCell_RealPG|TestClaimStreamCleanupOutboxBatch_TenantFencedLease_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-signing-keys -tags schema_verify -run 'TestSigningKeyRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-playback-policy -tags schema_verify -run 'TestPlaybackPolicyRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-artifact-intents -tags schema_verify -run 'TestArtifactCreationIntentRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-artifact-catalog -tags schema_verify -run 'TestUpdateArtifactCatalogSnapshot_ServingClusterEqualRevisionRepair_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-media-retention -tags schema_verify -run 'TestMediaRetentionRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-pull-source-events -tags schema_verify -run 'TestPullSourceEventRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-api-tokens -tags schema_verify -run 'TestAPITokenRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-native-auth -tags schema_verify -run 'TestNativeAuthorizationRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/

verify-quartermaster-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-quartermaster-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Quartermaster's converted repositories on PostgreSQL (Docker)..."
	@$(CONTRACT_GO_TEST) api_tenants postgres/quartermaster-bootstrap -tags schema_verify -run 'TestBootstrapRepositoryReplay_RealPG' -count=1 -timeout 600s ./internal/bootstrap/
	@$(CONTRACT_GO_TEST) api_tenants postgres/quartermaster-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestPeerDiscoveryCanonicalControlCells_RealPG|TestManualQueryAdapters_RealPG|TestConvertedRuntimeAdapters_RealPG|TestDNSEntitlementExpandPreservesPaidAliasesUntilObservation_RealPG|TestListTenantEffectiveAccessUsesCanonicalActiveGrantPredicate_RealPG|TestMediaPlacementInventory_RealPG|TestMediaCapacityConsent_RealPG|TestMediaAuthorityRefreshCoalescing_RealPG|TestPrivateClusterControlCellFoghorns_RealPG|TestClusterControlCellReassignment_RealPG|TestServiceEventOutboxScopeAndLeaseToken_RealPG' -count=1 -timeout 600s ./internal/database/quartermasterdb/
	@$(CONTRACT_GO_TEST) api_tenants postgres/quartermaster-data-migrations -tags schema_verify -run 'TestTenantDNSEntitlementsRunAndVerifyRealPG|TestNodeIdentityKeyGateUsesRemediationOwnershipRealPG' -count=1 -timeout 600s ./internal/datamigrations/
	@$(CONTRACT_GO_TEST) api_tenants postgres/quartermaster-dns-entitlement-handoff -tags schema_verify -run 'TestCompleteTenantDNSEntitlementHandoffRealPG|TestCapacityConsentManagement_RealPG|TestPrivateClusterOwnershipLimitSerializes_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_tenants postgres/quartermaster-capabilities -tags schema_verify -run '$(QUARTERMASTER_CAPABILITIES_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_tenants postgres/quartermaster-domain-events -tags schema_verify -run '$(QUARTERMASTER_DOMAIN_EVENTS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/

verify-quartermaster-yugabyte-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-quartermaster-yugabyte-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Quartermaster's converted repositories on Yugabyte (Docker)..."
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-quartermaster-contracts

verify-foghorn-db: verify-foghorn-test-selection
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-foghorn-db requires a running Docker daemon"; exit 1; }
	@echo "Verifying Foghorn's generated catalog and production-path repositories on PostgreSQL, plus catalog compatibility on Yugabyte (Docker)..."
	@$(CONTRACT_GO_TEST) api_balancing postgres/foghorn-query-catalog -tags schema_verify -run '$(FOGHORN_QUERY_CATALOG_REALPG_TESTS)' -count=1 -timeout 600s ./internal/database/foghorndb/
	@$(CONTRACT_GO_TEST) api_balancing postgres/foghorn-control -tags schema_verify -run '$(FOGHORN_CONTROL_REALPG_TESTS)' -count=1 -timeout 600s ./internal/control/
	@$(CONTRACT_GO_TEST) api_balancing postgres/foghorn-grpc -tags schema_verify -run '$(FOGHORN_GRPC_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_balancing postgres/foghorn-jobs -tags schema_verify -run '$(FOGHORN_JOBS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/jobs/
	@$(CONTRACT_GO_TEST) api_balancing postgres/foghorn-federation -tags schema_verify -run '$(FOGHORN_FEDERATION_REALPG_TESTS)' -count=1 -timeout 600s ./internal/federation/
	@$(CONTRACT_GO_TEST) api_balancing postgres/foghorn-media-authority -tags schema_verify -run '$(FOGHORN_MEDIA_AUTHORITY_REALPG_TESTS)' -count=1 -timeout 600s ./internal/mediaauthority/
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-foghorn-contracts-a
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-foghorn-contracts-b

verify-foghorn-valkey: verify-foghorn-test-selection
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-foghorn-valkey requires a running Docker daemon"; exit 1; }
	@echo "Verifying Foghorn state, registry, federation, relay grants, and persistence on release-pinned Valkey (Docker)..."
	@$(CONTRACT_GO_TEST) pkg valkey/redis-client -tags schema_verify -run 'TestNewUniversalClientSingle_RealValkey|TestSentinelFailover_RealValkey|TestChangelogReplayAndGap_RealValkey|TestClusterRouting_RealValkey' -count=1 -timeout 600s ./redis/
	@$(CONTRACT_GO_TEST) api_balancing valkey/foghorn-state -tags schema_verify -run '$(FOGHORN_STATE_REALVALKEY_TESTS)' -count=1 -timeout 600s ./internal/state/
	@$(CONTRACT_GO_TEST) api_balancing valkey/foghorn-control -tags schema_verify -run '$(FOGHORN_CONTROL_REALVALKEY_TESTS)' -count=1 -timeout 600s ./internal/control/
	@$(CONTRACT_GO_TEST) api_balancing valkey/foghorn-federation -tags schema_verify -run '$(FOGHORN_FEDERATION_REALVALKEY_TESTS)' -count=1 -timeout 600s ./internal/federation/

verify-schema-postgres: verify-foghorn-test-selection
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-schema-postgres requires a running Docker daemon"; exit 1; }
	@echo "Verifying Postgres current baseline and tagged-release upgrade convergence (Docker)..."
	@FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG='$(SCHEMA_VERIFY_FROM_TAG)' $(CONTRACT_GO_TEST) cli postgres/schema -tags schema_verify -run '$(SCHEMA_VERIFY_COMMON_TESTS)|$(SCHEMA_VERIFY_POSTGRES_TESTS)' -count=1 -timeout 1200s ./pkg/provisioner/
	@cd api_billing && go test -tags schema_verify -run 'TestProcessUsageSummaryAbsentDimensions_RealPG|TestV3UsageRowsRemainImmutableOnConflictingReplay_RealPG|TestV2UsageEnvelopePersistsIdempotentlyOnV3Schema_RealPG|TestMeteringSourceRegionRemainsAuthoritative_RealPG|TestPrepaidUsageSettlementMatchesAppliedBalanceTransactions_RealPG|TestProviderWebhookInboxRepository_RealPG|TestCryptoTaxDocuments_RealPG|TestCryptoTaxDocumentAnomalyReopensAndResolves_RealPG|TestEmbeddedFacilitatorSerializesRelayerNoncesAcrossReplicas_RealPG|TestInvoiceEmailOutboxLifecycleAndReads_RealPG|TestInvoiceEmailOverdueBalanceRead_RealPG|TestOperationalDatabaseGuards_RealPG|TestPrepaidBalanceCurrencyRepairMigration_RealPG|TestInvoiceCollectionMinimumSerializesAndPersists_RealPG|TestInvoiceRatingRepository_RealPG|TestInvoiceSettlementSurvivesConcurrentConfirmations_RealPG|TestInvoiceSettlementWaitsForFullCoverage_RealPG|TestInvoiceSettlementRecomputesOnAlreadyConfirmedReplay_RealPG|TestMonthlyInvoiceReplaysAfterSerializationFailure_RealPG' -count=1 -timeout 600s ./internal/handlers/
	@cd api_billing && go test -tags schema_verify -run 'TestBillingTransitionsSerializeAndPreserveCredit_RealPG|TestBillingEventOutboxLifecycle_RealPG|TestTierCatalogReads_RealPG|TestSubscriptionLifecycleRepository_RealPG|TestAccountOnboardingConvergence_RealPG|TestPrepaidBalanceRepository_RealPG|TestGRPCQueryPack_RealPG|TestTenantAdmissionStatus_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_billing && go test -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG' -count=1 -timeout 600s ./internal/database/purserdb/
	@cd api_billing && go test -tags schema_verify -run '$(PURSER_PRESENTMENT_GRPC_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@cd api_billing && go test -tags schema_verify -run '$(PURSER_DOMAIN_EVENTS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@cd api_billing && go test -tags schema_verify -run '$(PURSER_HANDLERS_DOMAIN_EVENTS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/handlers/
	@cd api_billing && go test -tags schema_verify -run '$(PURSER_PRESENTMENT_HANDLERS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/handlers/
	@cd api_dns && go test -tags schema_verify -run '$(NAVIGATOR_QUERY_CATALOG_REALPG_TESTS)' -count=1 -timeout 600s ./internal/database/navigatordb/
	@cd api_dns && go test -tags schema_verify -run '$(NAVIGATOR_STORE_REALPG_TESTS)' -count=1 -timeout 600s ./internal/store/
	@cd api_consultant && go test -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestCrawlJobCatalog_RealPG' -count=1 -timeout 600s ./internal/database/skipperdb/
	@cd api_consultant && go test -tags schema_verify -run 'TestConversationQueryPack_RealPG' -count=1 -timeout 600s ./internal/chat/
	@cd api_consultant && go test -tags schema_verify -run 'TestUsagePublicationRepository_RealPG' -count=1 -timeout 600s ./internal/metering/
	@cd api_consultant && go test -tags schema_verify -run 'TestReportRepository_RealPG' -count=1 -timeout 600s ./internal/heartbeat/
	@cd api_consultant && go test -tags schema_verify -run 'TestSocialPostRepository_RealPG' -count=1 -timeout 600s ./internal/social/
	@cd api_consultant && go test -tags schema_verify -run 'TestBaselineRepository_RealPG' -count=1 -timeout 600s ./internal/diagnostics/
	@cd api_consultant && go test -tags schema_verify -run 'TestPageCacheRepository_RealPG' -count=1 -timeout 600s ./internal/knowledge/
	@cd api_consultant && go test -tags schema_verify -run 'TestKnowledgeRepository_RealPG|TestReapAbandonedCrawlJobsUnblocksTheSitemap_RealPG|TestReapAbandonedCrawlJobsLeavesLiveCrawlsAlone_RealPG|TestReapAbandonedCrawlJobsLeavesSettledJobsAlone_RealPG' -count=1 -timeout 600s ./internal/knowledge/
	@cd api_analytics_query && go test -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestMeteringStateTransitions_RealPG' -count=1 -timeout 600s ./internal/database/meteringdb/
	@cd api_control && go test -tags schema_verify -run 'TestBootstrapAccountsRepository_RealPG|TestBootstrapPullStreamsRepository_RealPG|TestBootstrapMistNativeRepository_RealPG' -count=1 -timeout 600s ./internal/bootstrap/
	@cd api_control && go test -tags schema_verify -run '$(COMMODORE_QUERY_CATALOG_REALPG_TESTS)' -count=1 -timeout 600s ./internal/database/commodoredb/
	@cd api_control && go test -tags schema_verify -run 'TestDurableOutboxRepositories_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run '$(COMMODORE_DOMAIN_EVENTS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@cd pkg && go test -tags schema_verify -run '$(DOMAIN_EVENT_OUTBOX_REALPG_TESTS)' -count=1 -timeout 600s ./events/outbox/
	@cd api_webhooks && go test -tags schema_verify -run '$(BOSUN_REALPG_TESTS)' -count=1 -timeout 900s ./internal/...
	@cd api_control && go test -tags schema_verify -run 'TestStreamCleanupOutboxLoop_DeliveryOutageConverges_RealPG|TestStreamThumbnailCleanup_DispatchesEveryOwningCell_RealPG|TestStreamThumbnailCleanup_HangingCellDoesNotStarveSiblings_RealPG|TestRecordStreamActiveCluster_ServiceOnly_DoesNotTouchServingSet_RealPG|TestClearStreamActiveCluster_ReleasesManagedClaimAfterSoftDelete_RealPG|TestRegisterStreamThumbnailServingCell_FencesOnDeletion_RealPG|TestRegisterVsDeleteStream_Linearizes_RealPG|TestStreamCleanupOutbox_ThumbnailPhaseMarkedThenSkipped_RealPG|TestDeleteStream_RoutesToEveryServingCell_RealPG|TestClaimStreamCleanupOutboxBatch_TenantFencedLease_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestSigningKeyRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestPlaybackPolicyRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestArtifactCreationIntentRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestUpdateArtifactCatalogSnapshot_.*_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestMediaRetentionRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestPullSourceEventRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestAPITokenRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_control && go test -tags schema_verify -run 'TestNativeAuthorizationRepository_RealPG' -count=1 -timeout 600s ./internal/grpc/
	@cd api_tenants && go test -tags schema_verify -run 'TestBootstrapRepositoryReplay_RealPG' -count=1 -timeout 600s ./internal/bootstrap/
	@cd api_tenants && go test -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealPG|TestManualQueryAdapters_RealPG|TestConvertedRuntimeAdapters_RealPG|TestListTenantEffectiveAccessUsesCanonicalActiveGrantPredicate_RealPG|TestMediaPlacementInventory_RealPG|TestMediaCapacityConsent_RealPG|TestMediaAuthorityRefreshCoalescing_RealPG|TestPrivateClusterControlCellFoghorns_RealPG|TestClusterControlCellReassignment_RealPG|TestServiceEventOutboxScopeAndLeaseToken_RealPG' -count=1 -timeout 600s ./internal/database/quartermasterdb/
	@cd api_tenants && go test -tags schema_verify -run '$(QUARTERMASTER_CAPABILITIES_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@cd api_tenants && go test -tags schema_verify -run '$(QUARTERMASTER_DOMAIN_EVENTS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@cd api_billing && go test -tags schema_verify -run 'TestStripeMeterEventRepository_RealPG' -count=1 -timeout 600s ./internal/stripe/
	@cd api_billing && go test -tags schema_verify -run 'TestLoadEffectiveTierPartialOverrides_RealPG|TestPlacementTariffSnapshot_RealPG|TestPlacementPriceBoundaries_RealPG|TestPlacementAllowanceUsage_RealPG' -count=1 -timeout 600s ./internal/billing/ ./internal/pricing/
	@cd api_billing && go test -tags schema_verify -run 'TestTierAccessEligibilityQuery_RealPG' -count=1 -timeout 600s ./internal/tieraccess/
	@cd api_billing && go test -tags schema_verify -run 'TestBootstrapPricingRepositories_RealPG|TestBootstrapCustomerBillingRepository_RealPG|TestBootstrapTierCatalogRepository_RealPG|TestTierStripeSyncRepository_RealPG' -count=1 -timeout 600s ./internal/bootstrap/
	@cd api_billing && go test -tags schema_verify -run '$(PURSER_TIER_FEATURES_REALPG_TESTS)' -count=1 -timeout 600s ./internal/bootstrap/
	@echo "Verifying the real PostgreSQL admission-ledger proof drives exact Redis membership cleanup..."
	@cd api_balancing && go test -tags schema_verify -run 'TestMembershipTombstoneCleanup_PostgresProofToRedisPurge_RealPG' -count=1 -timeout 600s ./internal/federation/
	@cd api_balancing && go test -tags schema_verify -run '$(FOGHORN_DOMAIN_EVENTS_REALPG_TESTS)' -count=1 -timeout 600s ./internal/control/
	@cd api_balancing && go test -tags schema_verify -run '$(FOGHORN_GRPC_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/

verify-schema-yugabyte: verify-foghorn-test-selection
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-schema-yugabyte requires a running Docker daemon"; exit 1; }
	@$(MAKE) --no-print-directory verify-schema-yugabyte-schema-isolated
	@$(MAKE) --no-print-directory verify-yugabyte-services-isolated
	@$(MAKE) --no-print-directory verify-backup-restore-yugabyte

verify-schema-yugabyte-schema: verify-foghorn-test-selection
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-schema-yugabyte-schema requires a running Docker daemon"; exit 1; }
	@$(MAKE) --no-print-directory verify-schema-yugabyte-schema-isolated

verify-schema-yugabyte-schema-isolated:
	@$(MAKE) --no-print-directory verify-schema-yugabyte-selection-contracts
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-schema-yugabyte-engine-contracts
	@failed=0; \
	for database in $(YUGABYTE_SCHEMA_DATABASES); do \
		for group in compat completion preflight; do \
			echo "Verifying $$database Yugabyte $$group contract in a fresh engine..."; \
			coverage_name="schema-$$database"; \
			if [ "$$group" != compat ]; then coverage_name="$$coverage_name-$$group"; fi; \
			FRAMEWORKS_YUGABYTE_DATABASES="$$database" YUGABYTE_SCHEMA_TEST_GROUP="$$group" \
				$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory \
				verify-schema-yugabyte-schema-contracts YUGABYTE_SCHEMA_COVERAGE_NAME="$$coverage_name" || failed=1; \
		done; \
	done; \
	exit $$failed

verify-schema-yugabyte-engine-contracts:
	@test -n "$$FRAMEWORKS_YUGABYTE_TEST_CONTAINER" || { echo "ERROR: use make verify-schema-yugabyte so the engine contracts run in an isolated engine"; exit 1; }
	@echo "Verifying Yugabyte colocation engine contracts (Docker)..."
	@$(CONTRACT_GO_TEST) cli yugabyte/colocation-engine -tags schema_verify -run '$(SCHEMA_VERIFY_YUGABYTE_ENGINE_TESTS)' -count=1 -timeout 1200s ./pkg/provisioner/

verify-yugabyte-relayout-rehearsal:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-yugabyte-relayout-rehearsal requires a running Docker daemon"; exit 1; }
	@echo "Rehearsing a production-shaped relayout on a three-node RF3 Yugabyte cluster (Docker)..."
	@FRAMEWORKS_YUGABYTE_REHEARSAL=1 $(CONTRACT_GO_TEST) cli yugabyte/relayout-rehearsal -tags schema_verify -run '$(YUGABYTE_RELAYOUT_REHEARSAL_TESTS)' -count=1 -v -timeout 3600s ./pkg/provisioner/

verify-yugabyte-layout-benchmark:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-yugabyte-layout-benchmark requires a running Docker daemon"; exit 1; }
	@echo "Benchmarking colocated write_rate_benchmark tables against distributed placement (Docker)..."
	@FRAMEWORKS_YUGABYTE_BENCHMARK=1 $(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(CONTRACT_GO_TEST) cli yugabyte/layout-benchmark -tags schema_verify -run '$(YUGABYTE_LAYOUT_BENCHMARK_TESTS)' -count=1 -v -timeout 3600s ./pkg/provisioner/

verify-schema-yugabyte-selection-contracts:
	@echo "Verifying Yugabyte schema harness and database selection..."
	@FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG='$(SCHEMA_VERIFY_FROM_TAG)' $(CONTRACT_GO_TEST) cli yugabyte/schema-selection -tags schema_verify -run '$(SCHEMA_VERIFY_YUGABYTE_STATIC_TESTS)' -count=1 -timeout 1200s ./pkg/provisioner/

verify-yugabyte-services: verify-foghorn-test-selection
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-yugabyte-services requires a running Docker daemon"; exit 1; }
	@$(MAKE) --no-print-directory verify-yugabyte-services-isolated

verify-yugabyte-services-isolated:
	@$(MAKE) --no-print-directory verify-yugabyte-commodore-contracts
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-purser-contracts
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-navigator-contracts
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-skipper-contracts
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-lookout-contracts
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-quartermaster-contracts
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-periscope-metering-contracts
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-foghorn-contracts-a
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-foghorn-contracts-b
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-bosun-contracts

verify-schema-yugabyte-schema-contracts:
	@test -n "$$FRAMEWORKS_YUGABYTE_TEST_DSN" -a -n "$$FRAMEWORKS_YUGABYTE_TEST_CONTAINER" || { echo "ERROR: use make verify-schema-yugabyte so the contracts share one isolated engine"; exit 1; }
	@case "$$FRAMEWORKS_YUGABYTE_DATABASES" in bosun|commodore|foghorn|lookout|navigator|periscope|purser|quartermaster|skipper) ;; *) echo "ERROR: Yugabyte schema contracts require exactly one supported FRAMEWORKS_YUGABYTE_DATABASES value"; exit 2;; esac
	@case "$$YUGABYTE_SCHEMA_TEST_GROUP" in \
		compat) tests='$(SCHEMA_VERIFY_YUGABYTE_COMPAT_TESTS)' ;; \
		completion) tests='$(SCHEMA_VERIFY_YUGABYTE_COMPLETION_TESTS)' ;; \
		preflight) tests='$(SCHEMA_VERIFY_YUGABYTE_PREFLIGHT_TESTS)' ;; \
		*) echo "ERROR: unknown Yugabyte schema contract group $$YUGABYTE_SCHEMA_TEST_GROUP"; exit 2 ;; \
	esac; \
	echo "Verifying $$FRAMEWORKS_YUGABYTE_DATABASES Yugabyte $$YUGABYTE_SCHEMA_TEST_GROUP contract (Docker)..."; \
	FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG='$(SCHEMA_VERIFY_FROM_TAG)' $(CONTRACT_GO_TEST) cli yugabyte/$${YUGABYTE_SCHEMA_COVERAGE_NAME:-schema-$$FRAMEWORKS_YUGABYTE_DATABASES-$$YUGABYTE_SCHEMA_TEST_GROUP} -tags schema_verify -run "$$tests" -count=1 -timeout 1200s ./pkg/provisioner/

verify-yugabyte-shared-fixture:
	@test -n "$$FRAMEWORKS_YUGABYTE_TEST_DSN" -a -n "$$FRAMEWORKS_YUGABYTE_TEST_CONTAINER" || { echo "ERROR: invoke Yugabyte contracts through their public Make target"; exit 1; }

.PHONY: verify-yugabyte-commodore-contracts-a verify-yugabyte-commodore-contracts-b verify-yugabyte-commodore-contracts-c verify-yugabyte-commodore-contracts-d
# Retained test databases consume tablets until the fixture exits. Keep each
# group in its own engine rather than increasing Yugabyte's replica safety cap.
verify-yugabyte-commodore-contracts: verify-commodore-placement-test-selection
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-commodore-contracts-a
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-commodore-contracts-b
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-commodore-contracts-c
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-commodore-contracts-d

verify-yugabyte-commodore-contracts-a: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_control yugabyte/commodore-query-catalog -tags schema_verify -run '$(COMMODORE_QUERY_CATALOG_REALYB_TESTS)' -count=1 -timeout 1200s ./internal/database/commodoredb/
	@$(CONTRACT_GO_TEST) api_control yugabyte/commodore-placement-a -tags schema_verify -run '^($(COMMODORE_PLACEMENT_REALYB_TESTS_A))$$' -count=1 -timeout 600s ./internal/grpc/

verify-yugabyte-commodore-contracts-b: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_control yugabyte/commodore-placement-b -tags schema_verify -run '^($(COMMODORE_PLACEMENT_REALYB_TESTS_B))$$' -count=1 -timeout 600s ./internal/grpc/

verify-yugabyte-commodore-contracts-c: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_control yugabyte/commodore-media-authority-c -tags schema_verify -run '^($(COMMODORE_MEDIA_AUTHORITY_REALYB_TESTS_C))$$' -count=1 -timeout 900s ./internal/grpc/

verify-yugabyte-commodore-contracts-d: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_control yugabyte/commodore-media-authority-d -tags schema_verify -run '^($(COMMODORE_MEDIA_AUTHORITY_REALYB_TESTS_D))$$' -count=1 -timeout 900s ./internal/grpc/

.PHONY: verify-placement-yugabyte-db verify-placement-yugabyte-contracts verify-placement-yugabyte-control-contracts verify-placement-yugabyte-authority-contracts
verify-placement-yugabyte-db: verify-placement-yugabyte-contracts

verify-placement-yugabyte-contracts: verify-commodore-placement-test-selection
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-placement-yugabyte-control-contracts
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-commodore-contracts-b
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-commodore-contracts-c
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-placement-yugabyte-authority-contracts

verify-placement-yugabyte-control-contracts: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_control yugabyte/placement-query-catalog -tags schema_verify -run '^TestGeneratedQueryCatalogPrepares_RealYugabyte$$' -count=1 -timeout 600s ./internal/database/commodoredb/
	@$(CONTRACT_GO_TEST) api_control yugabyte/placement-control -tags schema_verify -run '^TestMediaPlacement(Options|Preview)_RealYugabyte$$' -count=1 -timeout 600s ./internal/grpc/

verify-placement-yugabyte-authority-contracts: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_balancing yugabyte/placement-authority -tags schema_verify -run '^Test(FoghornGeneratedQueryCatalogPrepares|PlacementAuthorityPairTenantAndVersionFences)_RealYugabyte$$' -count=1 -timeout 600s ./internal/database/foghorndb/

COMMODORE_PLACEMENT_REALPG_TESTS := ^TestMediaPlacement(Repository|Management|Options|Preview|CommercialPublication|CommercialParentLock|ObjectPolicy|ObjectPolicyRetainedDefault|TenantRefresh|DeadlineDelivery|DeliveryClaims|DeliveryBacklog|CellAttestation|FirstIssuance|SystemApply|PinMigration|PinMigrationConcurrentLocationEdit)_RealPG$$
# Flat list, not a compressed alternation: check-go-test-selection.sh splits a
# selector on | and compares literal test names, so the drift guard can only
# cover a family that is spelled out.
COMMODORE_PLACEMENT_ACTIVATION_REALPG_TESTS := TestPlacementActivationSurvivesConcurrentAcknowledgements_RealPG|TestPlacementActivationWaitsForEveryCell_RealPG|TestPlacementActivationSerializesConcurrentDerivation_RealPG|TestPlacementActivationBacklogConvergesStrandedChange_RealPG|TestPlacementActivationBacklogRespectsOutstandingCells_RealPG|TestPlacementActivationBacklogPagesPastBlockedScopes_RealPG

.PHONY: verify-placement-db
verify-placement-db:
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-placement-db requires a running Docker daemon"; exit 1; }
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-placement -tags schema_verify -run '$(COMMODORE_PLACEMENT_REALPG_TESTS)|$(COMMODORE_PLACEMENT_ACTIVATION_REALPG_TESTS)' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_control postgres/commodore-media-authority -tags schema_verify -run '^($(COMMODORE_MEDIA_AUTHORITY_REALPG_TESTS))$$' -count=1 -timeout 600s ./internal/grpc/

verify-yugabyte-purser-contracts: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_billing yugabyte/purser-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealYugabyte' -count=1 -timeout 1200s ./internal/database/purserdb/

verify-yugabyte-navigator-contracts: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_dns yugabyte/navigator-query-catalog -tags schema_verify -run '$(NAVIGATOR_QUERY_CATALOG_REALYB_TESTS)' -count=1 -timeout 1200s ./internal/database/navigatordb/
	@$(CONTRACT_GO_TEST) api_dns yugabyte/navigator-store -tags schema_verify -run '$(NAVIGATOR_STORE_REALYB_TESTS)' -count=1 -timeout 1200s ./internal/store/

verify-yugabyte-skipper-contracts: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_consultant yugabyte/skipper-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealYugabyte' -count=1 -timeout 1200s ./internal/database/skipperdb/

verify-yugabyte-lookout-contracts: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_incidents yugabyte/lookout-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealYugabyte' -count=1 -timeout 1200s ./internal/database/lookoutdb/
	@$(CONTRACT_GO_TEST) api_incidents yugabyte/lookout-incidents -tags schema_verify -run 'TestLookoutIngestStateMachine_RealYugabyte|TestLookoutIncidentActions_RealYugabyte|TestLookoutIncidentRescope_RealYugabyte|TestLookoutOwnershipIngestRace_RealYugabyte' -count=1 -timeout 1200s ./internal/incidents/
	@$(CONTRACT_GO_TEST) api_incidents yugabyte/lookout-delivery -tags schema_verify -run 'TestLookoutDeliveryOutboxTokenFencing_RealYugabyte|TestLookoutDeliveryTerminalFailure_RealYugabyte|TestLookoutDeliveryRetention_RealYugabyte|TestLookoutOperatorActivityOutbox_RealYugabyte' -count=1 -timeout 1200s ./internal/notify/
	@$(CONTRACT_GO_TEST) api_incidents yugabyte/lookout-ownership -tags schema_verify -run 'TestLookoutClusterOwnershipEvent_RealYugabyte|TestLookoutStartupOwnershipReconcile_RealYugabyte|TestLookoutClusterCreatedOwnership_RealYugabyte' -count=1 -timeout 1200s ./internal/ownership/

verify-yugabyte-quartermaster-contracts: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_tenants yugabyte/quartermaster-consent-management -tags schema_verify -run 'TestCapacityConsentManagement_RealYugabyte|TestPrivateClusterOwnershipLimitSerializes_RealYugabyte' -count=1 -timeout 600s ./internal/grpc/
	@$(CONTRACT_GO_TEST) api_tenants yugabyte/quartermaster-query-catalog -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealYugabyte|TestConvertedRuntimeAdapters_RealYugabyte|TestMediaPlacementInventory_RealYugabyte|TestMediaCapacityConsent_RealYugabyte|TestMediaAuthorityRefreshCoalescing_RealYugabyte' -count=1 -timeout 1200s ./internal/database/quartermasterdb/
	@$(CONTRACT_GO_TEST) api_tenants yugabyte/quartermaster-capabilities -tags schema_verify -run '^($(QUARTERMASTER_CAPABILITIES_REALYB_TESTS))$$' -count=1 -timeout 600s ./internal/grpc/

verify-yugabyte-bosun-contracts: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_webhooks yugabyte/bosun-ledger -tags schema_verify -run '^($(BOSUN_REALYB_TESTS))$$' -count=1 -timeout 1200s ./internal/integration/

verify-yugabyte-periscope-metering-contracts: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_analytics_query yugabyte/periscope-metering -tags schema_verify -run 'TestGeneratedQueryCatalogPrepares_RealYugabyte|TestMeteringStateTransitions_RealYugabyte' -count=1 -timeout 1200s ./internal/database/meteringdb/

verify-yugabyte-foghorn-contracts-a: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_balancing yugabyte/foghorn-query-catalog-a -tags schema_verify -run '$(FOGHORN_QUERY_CATALOG_REALYB_TESTS_A)' -count=1 -timeout 1200s ./internal/database/foghorndb/

verify-yugabyte-foghorn-contracts-b: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_balancing yugabyte/foghorn-query-catalog-b -tags schema_verify -run '$(FOGHORN_QUERY_CATALOG_REALYB_TESTS_B)' -count=1 -timeout 1200s ./internal/database/foghorndb/
	@$(MAKE) --no-print-directory verify-yugabyte-foghorn-authority-contracts

.PHONY: verify-yugabyte-foghorn-authority-contracts
verify-yugabyte-foghorn-authority-contracts: verify-yugabyte-shared-fixture
	@$(CONTRACT_GO_TEST) api_balancing yugabyte/foghorn-media-authority -tags schema_verify -run '^($(FOGHORN_MEDIA_AUTHORITY_REALYB_TESTS))$$' -count=1 -timeout 600s ./internal/mediaauthority/

verify-yugabyte-service: verify-foghorn-test-selection
	@case "$(SERVICE)" in commodore|purser|navigator|skipper|quartermaster|periscope-metering|foghorn|foghorn-authority|lookout|bosun) ;; *) echo "ERROR: SERVICE must be commodore, purser, navigator, skipper, quartermaster, periscope-metering, foghorn, foghorn-authority, lookout, or bosun"; exit 2;; esac
ifeq ($(SERVICE),foghorn)
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-foghorn-contracts-a
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-foghorn-contracts-b
else ifeq ($(SERVICE),commodore)
	@$(MAKE) --no-print-directory verify-yugabyte-commodore-contracts
else
	@$(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-$(SERVICE)-contracts
endif

verify-yugabyte-database: verify-foghorn-test-selection
	@case "$(DATABASE)" in bosun|commodore|foghorn|lookout|navigator|periscope|purser|quartermaster|skipper) ;; *) echo "ERROR: DATABASE must be bosun, commodore, foghorn, lookout, navigator, periscope, purser, quartermaster, or skipper"; exit 2;; esac
ifeq ($(DATABASE),foghorn)
	@FRAMEWORKS_YUGABYTE_DATABASES=foghorn $(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-schema-yugabyte-schema-contracts verify-yugabyte-foghorn-contracts-a YUGABYTE_SCHEMA_COVERAGE_NAME=schema-foghorn
	@FRAMEWORKS_YUGABYTE_DATABASES=foghorn $(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-yugabyte-foghorn-contracts-b
else ifeq ($(DATABASE),commodore)
	@FRAMEWORKS_YUGABYTE_DATABASES=commodore $(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-schema-yugabyte-schema-contracts YUGABYTE_SCHEMA_COVERAGE_NAME=schema-commodore
	@$(MAKE) --no-print-directory verify-yugabyte-commodore-contracts
else
	@service="$(DATABASE)"; if [ "$$service" = periscope ]; then service=periscope-metering; fi; FRAMEWORKS_YUGABYTE_DATABASES="$(DATABASE)" $(CURDIR)/scripts/run-yugabyte-contract-fixture.sh $(MAKE) --no-print-directory verify-schema-yugabyte-schema-contracts verify-yugabyte-$$service-contracts YUGABYTE_SCHEMA_COVERAGE_NAME=schema-$(DATABASE)
endif

verify-yugabyte-ha: verify-foghorn-test-selection
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-yugabyte-ha requires a running Docker daemon"; exit 1; }
	@echo "Verifying Yugabyte RF=3 smart-driver distribution, transaction retry, node loss, and recovery (Docker)..."
	@$(CONTRACT_GO_TEST) pkg yugabyte/ha -tags yugabyte_ha -run '$(YUGABYTE_HA_TESTS)' -count=1 -timeout 900s ./database/

verify-schema-clickhouse: verify-foghorn-test-selection
	@docker info >/dev/null 2>&1 || { echo "ERROR: verify-schema-clickhouse requires a running Docker daemon"; exit 1; }
	@echo "Verifying Replicated ClickHouse baseline == baseline + post-floor migrations (Docker)..."
	@FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG='$(SCHEMA_VERIFY_FROM_TAG)' $(CONTRACT_GO_TEST) cli clickhouse/schema -tags schema_verify -run '$(SCHEMA_VERIFY_COMMON_TESTS)|$(SCHEMA_VERIFY_CLICKHOUSE_TESTS)' -count=1 -timeout 1200s ./pkg/provisioner/

verify-feature-registry:
	@echo "Validating docs/platform-features.yaml and checking generated renderers..."
	@cd scripts/registry && go test -count=1 ./...
	@cd scripts/registry && go run . -check
	@echo "✓ Feature registry verified"

# Regenerate the feature-registry artifacts in place (registry.json, feature-matrix.mdx,
# platform-capabilities.mdx, pkg/platformfeatures/registry_gen.go). Run this after editing docs/platform-features.yaml, then commit.
generate-feature-registry:
	@cd scripts/registry && go run .

# Marketing pricing data (website_marketing/src/data/pricing-catalog.json) is generated from
# Purser's tier catalog. Run generate-pricing-catalog after editing billing_tiers.yaml.
generate-pricing-catalog:
	@cd scripts/pricingcatalog && go run .

verify-pricing-catalog:
	@cd scripts/pricingcatalog && go test -count=1 ./...
	@cd scripts/pricingcatalog && go run . -check

# Typed service configuration. generate-config-reference rewrites the operator configuration
# reference and the CLI config schema from api_*/internal/appconfig; verify-config-annotations
# fails when they are stale, an annotation is invalid, or a migrated command reads the
# environment outside its typed config.
generate-config-reference:
	@cd scripts/configref && go run . -repo ../..

verify-config-annotations:
	@cd scripts/configref && go test -count=1 ./...
	@cd scripts/configref && go run . -check -repo ../..

# Release channels have one classifier, pkg/version.ChannelForTag, shared by the CLI and the
# release workflow (through scripts/release-channel).
verify-release-channels:
	@cd pkg && go test ./version/ -run Channel -count=1
	@cd scripts/release-channel && go test ./... -count=1
	@cd cli && go test ./internal/releases/ -run Channel -count=1

verify-swift-gql:
	@./scripts/generate-swift-gql.sh
	@git diff --exit-code -- app_mac/Sources/Gateway/GeneratedQueries.swift || { echo "GeneratedQueries.swift is stale: run make graphql-tray and commit the result"; exit 1; }

# Draft the New/Hardened/Fixes/Build/Docs sections of a release note from conventional commits.
# Usage: make release-notes-draft SINCE=v0.3.4 [NOTES_VERSION=v0.3.5]
release-notes-draft:
	@test -n "$(SINCE)" || { echo "usage: make release-notes-draft SINCE=<previous tag> [NOTES_VERSION=<new tag>]"; exit 2; }
	@cd tools/release-notes && go run . --since "$(SINCE)" --version "$(NOTES_VERSION)" --repo ../..

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
	pnpm --filter frameworks-frontend exec svelte-kit sync
	pnpm --filter frameworks-frontend gql:codegen
	pnpm test:coverage
	pnpm --filter frameworks-frontend test:components
	pnpm build

REPORTS_DIR := reports

dead-code-install:
	@echo "Installing Go dead code analysis tools..."
	go install golang.org/x/tools/cmd/deadcode@latest
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
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
