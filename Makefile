.PHONY: build build-images build-bin-commodore build-bin-quartermaster build-bin-purser build-bin-decklog build-bin-foghorn build-bin-helmsman build-bin-periscope-ingest build-bin-periscope-query build-bin-signalman build-bin-bridge \
	build-image-commodore build-image-quartermaster build-image-purser build-image-decklog build-image-foghorn build-image-helmsman build-image-periscope-ingest build-image-periscope-query build-image-signalman build-image-bridge \
	proto graphql clean version install-tools verify test coverage

# Version information
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

# Build flags for version injection (matching pkg/version/version.go variable names)
LDFLAGS = -ldflags "-X frameworks/pkg/version.Version=$(VERSION) \
					-X frameworks/pkg/version.GitCommit=$(GIT_COMMIT) \
					-X frameworks/pkg/version.BuildDate=$(BUILD_DATE)"

# All microservices (only services with actual binaries)
SERVICES = commodore quartermaster purser decklog foghorn helmsman periscope-ingest periscope-query signalman bridge

# All Go modules (including pkg for testing)
GO_SERVICES = $(shell find . -name "go.mod" -exec dirname {} \;)

# Generate proto files first
proto:
	cd pkg/proto && make proto

# Generate GraphQL files
graphql:
	cd api_gateway && make graphql

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
			go vet ./... && \
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
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

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

# Generate a single combined coverage report at ./coverage
coverage: proto graphql
	@echo "Generating combined coverage for all Go modules..."
	@rm -rf coverage && mkdir -p coverage
	@echo "mode: atomic" > coverage/coverage.out
	@for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "==> $$service_name"; \
		( cd $$service_dir && \
			go mod tidy >/dev/null 2>&1 && \
			tmpfile=$$(mktemp); \
			if go test ./... -coverpkg=./... -coverprofile="$$tmpfile" -covermode=atomic -count=1 >/dev/null 2>&1; then \
				if [ -s "$$tmpfile" ]; then \
					tail -n +2 "$$tmpfile" >> "../coverage/coverage.out"; \
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
	@echo "Combined coverage saved to coverage/coverage.out"
