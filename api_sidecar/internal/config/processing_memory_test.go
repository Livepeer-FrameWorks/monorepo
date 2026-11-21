package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestProcessingProtectionSurvivesRestartAndFencesDelayedFrames(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { _, _ = ApplyProcessingMemoryReservation(dir, &ipcpb.ProcessingMemoryReservation{}, true) })
	original := &ipcpb.ProcessingMemoryReservation{JobId: "oldest", Bytes: 2 << 30, Version: 7}
	if ok, err := ApplyProcessingMemoryReservation(dir, original, true); !ok || err != nil {
		t.Fatalf("apply: %v %v", ok, err)
	}
	processingProtectionState.Lock()
	processingProtectionState.path = ""
	processingProtectionState.value = processingProtection{}
	processingProtectionState.Unlock()
	if err := LoadProcessingMemoryReservation(dir); err != nil {
		t.Fatal(err)
	}
	if ProcessingProtectedMemory("younger") != 2<<30 || ProcessingProtectedMemory("oldest") != 0 {
		t.Fatal("restart forgot memory protection or owner exemption")
	}
	if ok, err := ApplyProcessingMemoryReservation(dir, &ipcpb.ProcessingMemoryReservation{Version: 8}, false); !ok || err != nil {
		t.Fatalf("clear: %v %v", ok, err)
	}
	if ok, err := ApplyProcessingMemoryReservation(dir, original, false); ok || err != nil {
		t.Fatalf("stale set not ignored: %v %v", ok, err)
	}
	if got := ProcessingProtectedMemory("younger"); got != 0 {
		t.Fatalf("clear was undone: %d", got)
	}
	if _, err := ApplyProcessingMemoryReservation(dir, &ipcpb.ProcessingMemoryReservation{JobId: "different", Bytes: 1, Version: 8}, false); err == nil {
		t.Fatal("conflicting same revision accepted")
	}
}

func TestProcessingProtectionUnreadableStateWaitsForTrustedSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "processing-reservation", "node.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = ApplyProcessingMemoryReservation(dir, &ipcpb.ProcessingMemoryReservation{}, true) })
	if err := LoadProcessingMemoryReservation(dir); err == nil {
		t.Fatal("corrupt persisted protection accepted")
	}
	if got := ProcessingProtectedMemory("any"); got != math.MaxUint64 {
		t.Fatalf("unknown persisted protection allowed admission: %d", got)
	}
	if ok, err := ApplyProcessingMemoryReservation(dir, &ipcpb.ProcessingMemoryReservation{Version: 1}, true); !ok || err != nil {
		t.Fatalf("authoritative repair: %v %v", ok, err)
	}
	if got := ProcessingProtectedMemory("any"); got != 0 {
		t.Fatalf("trusted clear could not repair unknown state: %d", got)
	}
	if _, err := ApplyProcessingMemoryReservation("", &ipcpb.ProcessingMemoryReservation{}, true); err == nil {
		t.Fatal("empty directory allowed unowned persistence")
	}
}
