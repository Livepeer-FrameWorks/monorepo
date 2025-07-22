.PHONY: proto build run clean

proto:
	protoc --go_out=. \
		--go_opt=paths=source_relative \
		--go-grpc_out=. \
		--go-grpc_opt=paths=source_relative \
		proto/decklog.proto

build: proto
	go build -o bin/decklog cmd/decklog/main.go

run: build
	./bin/decklog

clean:
	rm -f bin/decklog
	rm -f proto/*.pb.go 