.PHONY: proto clean install-tools

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
proto: clean
	protoc -I. \
		--go_out=. \
		--go_opt=module=github.com/Livepeer-FrameWorks/monorepo/pkg/proto \
		--go-grpc_out=. \
		--go-grpc_opt=module=github.com/Livepeer-FrameWorks/monorepo/pkg/proto \
		common.proto shared.proto cluster_peer.proto tenant_limits.proto metering_contract.proto x402.proto ipc.proto dns.proto commodore.proto quartermaster.proto purser.proto periscope.proto signalman.proto foghorn_control.proto foghorn.proto foghorn_federation.proto foghorn_relay.proto deckhand.proto skipper.proto

# Clean generated files. Removes the old flat-package output at the root AND the
# per-package subdirectory output, so a stale flat *.pb.go can never linger.
clean:
	rm -f *.pb.go *_grpc.pb.go
	rm -f common/*.pb.go shared/*.pb.go cluster_peer/*.pb.go tenant_limits/*.pb.go metering_contract/*.pb.go x402/*.pb.go ipc/*.pb.go dns/*.pb.go commodore/*.pb.go quartermaster/*.pb.go purser/*.pb.go periscope/*.pb.go signalman/*.pb.go foghorn_control/*.pb.go foghorn/*.pb.go foghorn_federation/*.pb.go foghorn_relay/*.pb.go deckhand/*.pb.go skipper/*.pb.go

# Install required tools
install-tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
