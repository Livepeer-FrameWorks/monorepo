.PHONY: proto clean install-tools

# Proto generation for all services
# - common.proto: Shared types (cursor pagination, time ranges)
# - shared.proto: Shared types between Foghorn and Commodore (clips, DVR, viewer)
# - ipc.proto: Media plane (Helmsman ↔ Foghorn ↔ Decklog)
# - dns.proto: DNS management (Navigator)
# - commodore.proto: Control plane - user auth, streams, API tokens
# - quartermaster.proto: Control plane - tenants, clusters, nodes
# - purser.proto: Control plane - billing, subscriptions, usage
# - periscope.proto: Data plane - analytics query service
# - signalman.proto: Realtime events (bidirectional streaming)
# - foghorn.proto: Control plane - Foghorn clip/DVR/viewer operations
proto: clean
	protoc --go_out=. \
		--go_opt=paths=source_relative \
		--go-grpc_out=. \
		--go-grpc_opt=paths=source_relative \
		common.proto shared.proto ipc.proto dns.proto commodore.proto quartermaster.proto purser.proto periscope.proto signalman.proto foghorn.proto

# Clean generated files
clean:
	rm -f *.pb.go *_grpc.pb.go

# Install required tools
install-tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Generate proto and copy to legacy location for backward compatibility
generate-all: proto
	@echo "Proto files generated in pkg/proto/"
	@echo "All services should import from: frameworks/pkg/proto"