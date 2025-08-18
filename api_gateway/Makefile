.PHONY: graphql clean install-tools

# GraphQL generation using gqlgen
graphql: clean
	gqlgen generate

# Clean generated files
clean:
	rm -f graph/generated/*.go
	rm -f graph/model/*.go
	rm -f graph/*.resolvers.go

# Install required tools
install-tools:
	go install github.com/99designs/gqlgen@latest

# Generate GraphQL and display info
generate-all: graphql
	@echo "GraphQL files generated in api_gateway/graph/"
	@echo "Generated files:"
	@echo "  - graph/generated/generated.go (server code)"
	@echo "  - graph/model/models_gen.go (generated models)"
	@echo "  - graph/*.resolvers.go (resolver interfaces)"