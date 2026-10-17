package grpc

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

// Staging rc10: deleting a URL import whose source never loaded logged "Failed
// to delete a deleted artifact's objects; the purge job retries", although
// nothing was ever stored and the purge job drops such a row without a delete.
// An artifact with no recorded object key has nothing to delete.
func TestDeleteArtifactBytesWithoutStoredObjectIsNotAFailure(t *testing.T) {
	logger, hook := logrustest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	s := &FoghornGRPCServer{logger: logger}
	s.SetArtifactCleaner(&artifacts.Cleaner{LocalCluster: "staging-media-eu", LocalBackendID: "backend-eu"})

	s.deleteArtifactBytesAfterCommit(artifacts.ArtifactRef{
		Hash: "20261016151018dfee49d5251757f6", Type: "vod", TenantID: "0dd30dec-a860-4d69-9f46-fc95251a556c", BackendID: "backend-eu",
	})

	deadline := time.Now().Add(5 * time.Second)
	for len(hook.AllEntries()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	entries := hook.AllEntries()
	if len(entries) == 0 {
		t.Fatal("the background delete logged nothing")
	}
	for _, e := range entries {
		if e.Level <= logrus.WarnLevel {
			t.Fatalf("delete of an artifact with no stored object logged %s %q (%v)", e.Level, e.Message, e.Data)
		}
	}
}
