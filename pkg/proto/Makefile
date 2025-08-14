.PHONY: proto clean install-tools

# Proto generation for all services
proto: clean
	protoc --go_out=. \
		--go_opt=paths=source_relative \
		--go-grpc_out=. \
		--go-grpc_opt=paths=source_relative \
		decklog.proto

# Clean generated files
clean:
	rm -f *.pb.go

# Install required tools
install-tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Generate proto and copy to legacy location for backward compatibility
generate-all: proto
	@echo "Proto files generated in pkg/proto/"
	@echo "All services should import from: frameworks/pkg/proto"