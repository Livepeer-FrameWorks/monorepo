.PHONY: proto proto-check clean install-tools

BUF_VERSION := v1.72.0
BUF := go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)

# Proto generation for all services
# - common.proto: Shared types (cursor pagination, time ranges)
# - shared.proto: Shared types between Foghorn and Commodore (clips, DVR, viewer)
# - cluster_peer.proto: TenantClusterPeer value type (cross-service routing envelope)
# - tenant_limits.proto: TenantResourceLimits value type (admission caps)
# - metering_contract.proto: MeterAllowance value type (billing allowance snapshot)
# - x402.proto: x402 client payment payload (Gateway/Commodore/Purser shared contract)
# - ipc.proto: Media plane (Helmsman ↔ Foghorn ↔ Decklog)
# - dns.proto: DNS management (Navigator)
# - commodore.proto: Control plane - user auth, streams, API tokens
# - quartermaster.proto: Control plane - tenants, clusters, nodes
# - purser.proto: Control plane - billing, subscriptions, usage
# - periscope.proto: Data plane - analytics query service
# - signalman.proto: Realtime events (bidirectional streaming)
# - foghorn_control.proto: Foghorn control msgs Commodore proxies (DVR chapters, node/tenant ops)
# - foghorn.proto: Control plane - Foghorn clip/DVR/viewer operations
# - deckhand.proto: Support messaging (Chatwoot integration)
# - skipper.proto: AI consultant chat service
# - foghorn_federation.proto: Cross-cluster federation (Foghorn ↔ Foghorn)
# - foghorn_relay.proto: Intra-cluster HA command relay (Foghorn ↔ Foghorn)
# Buf owns compilation and invokes versioned Go plugins from buf.gen.yaml.
# Local protoc and protoc-gen-* binaries are intentionally ignored so macOS
# and Linux generate byte-identical committed output.
proto: clean
	$(BUF) build
	$(BUF) generate

proto-check: proto
	@git diff --exit-code -- .
	@test -z "$$(git ls-files --others --exclude-standard .)" || { \
		echo "ERROR: protobuf generation produced untracked files:"; \
		git ls-files --others --exclude-standard .; \
		exit 1; \
	}

# Clean generated files. Removes the old flat-package output at the root AND the
# per-package subdirectory output, so a stale flat *.pb.go can never linger.
clean:
	rm -f *.pb.go *_grpc.pb.go
	rm -f common/*.pb.go shared/*.pb.go cluster_peer/*.pb.go tenant_limits/*.pb.go metering_contract/*.pb.go x402/*.pb.go ipc/*.pb.go dns/*.pb.go commodore/*.pb.go quartermaster/*.pb.go purser/*.pb.go periscope/*.pb.go signalman/*.pb.go foghorn_control/*.pb.go foghorn/*.pb.go foghorn_federation/*.pb.go foghorn_relay/*.pb.go deckhand/*.pb.go skipper/*.pb.go

# Warm the pinned Buf binary in the Go build cache. Kept as part of the root
# install-tools workflow for developers who want to prefetch all tooling.
install-tools:
	$(BUF) --version
