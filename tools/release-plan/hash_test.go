package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCanonicalGoListEnvPinsTargetPlatform(t *testing.T) {
	env := canonicalGoListEnv([]string{
		"GOOS=windows",
		"GOARCH=386",
		"CGO_ENABLED=0",
		"GOFLAGS=-tags=local",
		"PATH=/bin",
	}, true, "linux", "amd64")

	for _, forbidden := range []string{"GOOS=windows", "GOARCH=386", "GOFLAGS=-tags=local"} {
		if slices.Contains(env, forbidden) {
			t.Fatalf("env contains caller override %q: %v", forbidden, env)
		}
	}
	for _, want := range []string{"PATH=/bin", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=1", "GOFLAGS="} {
		if !slices.Contains(env, want) {
			t.Fatalf("env missing %q: %v", want, env)
		}
	}
}

func TestCanonicalGoListEnvDisablesCGOForPureGo(t *testing.T) {
	env := canonicalGoListEnv(nil, false, "darwin", "arm64")
	got := strings.Join(env, "\n")
	if !strings.Contains(got, "CGO_ENABLED=0") {
		t.Fatalf("CGO_ENABLED not disabled for pure-Go component: %v", env)
	}
}

// TestCollectProtoSourceClosure exercises the helper directly: a generated
// package seeds its `// source:` proto, and the BFS pulls in transitively
// imported protos even when the importing package's own generated dir is the
// only one in the Go closure. A vendored well-known-type (timestamp.proto, which
// exists under pkg/proto) is included; an import that resolves nowhere in the
// repo (duration.proto) is treated as external and skipped. Unrelated protos
// (ipc) are excluded.
func TestCollectProtoSourceClosure(t *testing.T) {
	root := t.TempDir()
	protoDir := filepath.Join(root, "pkg", "proto")
	write := func(rel, content string) {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("pkg/proto/common.proto", "syntax = \"proto3\";\n")
	write("pkg/proto/geo.proto", "syntax = \"proto3\";\nimport \"common.proto\";\n")
	// foghorn imports geo, a vendored WKT (timestamp, created below), and an
	// external WKT (duration, NOT created; must be skipped).
	write("pkg/proto/foghorn.proto", "syntax = \"proto3\";\nimport \"geo.proto\";\nimport \"google/protobuf/timestamp.proto\";\nimport \"google/protobuf/duration.proto\";\n")
	write("pkg/proto/ipc.proto", "syntax = \"proto3\";\nimport \"common.proto\";\n")
	// Vendored well-known-type fixture present in the repo must be hashed.
	write("pkg/proto/google/protobuf/timestamp.proto", "syntax = \"proto3\";\n")
	// Generated stubs carry the protoc source header.
	write("pkg/proto/foghorn/foghorn.pb.go", "// source: foghorn.proto\npackage foghornpb\n")
	write("pkg/proto/ipc/ipc.pb.go", "// source: ipc.proto\npackage ipcpb\n")

	// A closure that compiles only foghornpb: the proto closure must be
	// {common, foghorn, geo, google/protobuf/timestamp} via BFS, including the
	// vendored WKT but not the external duration.proto or the unrelated ipc.proto.
	closure := []string{
		filepath.Join(root, "pkg", "proto", "foghorn", "foghorn.pb.go"),
		filepath.Join(root, "api_balancing", "cmd", "foghorn", "main.go"), // non-proto, ignored
	}
	got, err := collectProtoSourceClosure(root, closure)
	if err != nil {
		t.Fatalf("collectProtoSourceClosure: %v", err)
	}
	want := []string{
		filepath.Join(protoDir, "common.proto"),
		filepath.Join(protoDir, "foghorn.proto"),
		filepath.Join(protoDir, "geo.proto"),
		filepath.Join(protoDir, "google", "protobuf", "timestamp.proto"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("closure = %v, want %v", got, want)
	}
}
