package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// valueProtos are the Layer-0/1 contract packages every proto may import: pure
// value types and cross-service contract messages with no service API of their
// own. Keeping these importable is what lets service protos share types without
// pulling each other's full surface into the release-hash / compile closure.
var valueProtos = map[string]bool{
	"common":            true,
	"shared":            true,
	"cluster_peer":      true,
	"tenant_limits":     true,
	"metering_contract": true,
	"x402":              true,
	"foghorn_control":   true,
}

// allowedServiceEdges are the only permitted imports of one Layer-2 service /
// event proto by another: genuine consumers of the published IPC media-plane
// event contract. Any edge not listed here fails the test.
var allowedServiceEdges = map[string]bool{
	"periscope -> ipc":     true,
	"signalman -> ipc":     true,
	"foghorn_relay -> ipc": true,
}

// TestProtoLayering enforces that no Layer-2 service proto imports another
// Layer-2 service proto except via the explicit allowlist. New legitimate edges
// must be added to allowedServiceEdges deliberately; that friction is the point.
func TestProtoLayering(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	protoDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "pkg", "proto")
	entries, err := os.ReadDir(protoDir)
	if err != nil {
		t.Fatalf("read %s: %v", protoDir, err)
	}

	var violations []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".proto") {
			continue
		}
		importer := strings.TrimSuffix(e.Name(), ".proto")
		imports, err := readProtoImports(filepath.Join(protoDir, e.Name()))
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range imports {
			if strings.HasPrefix(imp, "google/protobuf/") {
				continue
			}
			imported := strings.TrimSuffix(imp, ".proto")
			if valueProtos[imported] {
				continue
			}
			edge := importer + " -> " + imported
			if allowedServiceEdges[edge] {
				continue
			}
			violations = append(violations, edge)
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("Layer-2 service protos must not import each other (move shared types to a value proto, or add a deliberate entry to allowedServiceEdges):\n  %s",
			strings.Join(violations, "\n  "))
	}
}
