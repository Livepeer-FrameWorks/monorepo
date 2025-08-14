.PHONY: build build-all proto clean version install-tools test test-all docker-build docker-build-all

# Version information
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

# Build flags for version injection (matching pkg/version/version.go variable names)
LDFLAGS = -ldflags "-X frameworks/pkg/version.Version=$(VERSION) \
					-X frameworks/pkg/version.GitCommit=$(GIT_COMMIT) \
					-X frameworks/pkg/version.BuildDate=$(BUILD_DATE)"

# All microservices (only services with actual binaries)
SERVICES = commodore quartermaster purser decklog foghorn helmsman periscope-ingest periscope-query signalman

# All Go modules (including pkg for testing)
GO_SERVICES = $(shell find . -name "go.mod" -exec dirname {} \;)

# Generate proto files first
proto:
	cd pkg/proto && make proto

# Build all services
build-all: proto bin
	@echo "Building all services with version: $(VERSION)"
	@for service in $(SERVICES); do \
		echo "Building $$service..."; \
		$(MAKE) build-$$service; \
	done

# Test all Go modules (consolidates scripts/test-builds.sh functionality)
test-all: proto
	@echo "Testing all Go modules..."
	@failed=0; \
	for service_dir in $(GO_SERVICES); do \
		service_name=$$(basename $$service_dir); \
		echo "Testing $$service_name..."; \
		(cd $$service_dir && \
			go mod tidy && \
			go fmt ./... && \
			go build ./...) || failed=1; \
		if [ -f "$$service_dir/Dockerfile" ]; then \
			echo "Building Docker image for $$service_name..."; \
			docker build -t frameworks-$$service_name:test -f $$service_dir/Dockerfile . || failed=1; \
		fi; \
	done; \
	if [ $$failed -eq 0 ]; then \
		echo "✓ All tests passed!"; \
	else \
		echo "✗ Some tests failed!"; \
		exit 1; \
	fi

# Docker build all services
docker-build-all: proto
	@echo "Building Docker images for all services..."
	@for service in $(SERVICES); do \
		$(MAKE) docker-build-$$service 2>/dev/null || echo "Skipping $$service (no Dockerfile)"; \
	done

# Individual Docker builds
docker-build-commodore: proto
	docker build -t frameworks-commodore:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_control/Dockerfile .

docker-build-quartermaster: proto
	docker build -t frameworks-quartermaster:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_tenants/Dockerfile .

docker-build-purser: proto
	docker build -t frameworks-purser:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_billing/Dockerfile .

docker-build-decklog: proto
	docker build -t frameworks-decklog:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_firehose/Dockerfile .

docker-build-foghorn: proto
	docker build -t frameworks-foghorn:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_balancing/Dockerfile .

docker-build-helmsman: proto
	docker build -t frameworks-helmsman:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_sidecar/Dockerfile .

docker-build-periscope-ingest: proto
	docker build -t frameworks-periscope-ingest:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_analytics_ingest/Dockerfile .

docker-build-periscope-query: proto
	docker build -t frameworks-periscope-query:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_analytics_query/Dockerfile .

docker-build-signalman: proto
	docker build -t frameworks-signalman:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f api_realtime/Dockerfile .

# Individual service builds
build-commodore: proto
	cd api_control && go build $(LDFLAGS) -o ../bin/commodore cmd/commodore/main.go

build-quartermaster: proto
	cd api_tenants && go build $(LDFLAGS) -o ../bin/quartermaster cmd/quartermaster/main.go

build-purser: proto
	cd api_billing && go build $(LDFLAGS) -o ../bin/purser cmd/purser/main.go

build-decklog: proto
	cd api_firehose && go build $(LDFLAGS) -o ../bin/decklog cmd/decklog/main.go

build-foghorn: proto
	cd api_balancing && go build $(LDFLAGS) -o ../bin/foghorn cmd/foghorn/main.go

build-helmsman: proto
	cd api_sidecar && go build $(LDFLAGS) -o ../bin/helmsman cmd/helmsman/main.go

build-periscope-ingest: proto
	cd api_analytics_ingest && go build $(LDFLAGS) -o ../bin/periscope-ingest cmd/periscope/main.go

build-periscope-query: proto
	cd api_analytics_query && go build $(LDFLAGS) -o ../bin/periscope-query cmd/periscope/main.go

build-signalman: proto
	cd api_realtime && go build $(LDFLAGS) -o ../bin/signalman cmd/signalman/main.go

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
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Create bin directory
bin:
	mkdir -p bin