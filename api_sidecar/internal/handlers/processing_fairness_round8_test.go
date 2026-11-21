package handlers

import (
	"testing"
	"time"

	"frameworks/api_sidecar/internal/config"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestOldMemoryWaiterProtectsRoomUntilAdmission(t *testing.T) {
	isolateProcessingOverrideState(t)
	dir := t.TempDir()
	first := true
	apply := func(job string, bytes, version uint64) {
		t.Helper()
		if _, err := config.ApplyProcessingMemoryReservation(dir, &ipcpb.ProcessingMemoryReservation{JobId: job, Bytes: bytes, Version: version}, first); err != nil {
			t.Fatal(err)
		}
		first = false
	}
	t.Cleanup(func() {
		_, _ = config.ApplyProcessingMemoryReservation(dir, &ipcpb.ProcessingMemoryReservation{}, true)
	})
	total := uint64(8*1024) * memoryMiB
	budget := processingMemoryBudget(total)
	held := budget / 2
	apply("old", held, 1)
	for _, job := range []string{"old", "small", "young"} {
		if err := setProcessingProcessOverride("processing+"+job, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := reserveProcessingMemory("processing+young", "young", "[]", held+memoryMiB, total, total); err != nil || ok {
		t.Fatalf("younger job stole protected memory: admitted=%v err=%v", ok, err)
	}
	if ok, err := reserveProcessingMemory("processing+small", "small", "[]", held-memoryMiB, total, total); err != nil || !ok {
		t.Fatalf("smaller job could not leave room: admitted=%v err=%v", ok, err)
	}
	if ok, err := reserveProcessingMemory("processing+old", "old", "[]", held, total, total); err != nil || !ok {
		t.Fatalf("owner counted its own protection: admitted=%v err=%v", ok, err)
	}
	apply("", 0, 2)
	// A delayed protection frame must not reinstate the reservation after clear.
	apply("old", held, 1)
	if got := config.ProcessingProtectedMemory("young"); got != 0 {
		t.Fatalf("stale update resurrected protection: %d", got)
	}
}
