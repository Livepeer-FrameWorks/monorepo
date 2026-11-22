package control

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestR9ProcessingResultRequiresTenant(t *testing.T) {
	for _, status := range []string{"cache_update", "completed", "failed", "retryable"} {
		t.Run(status, func(t *testing.T) {
			mock, _, _ := setupArtifactTestDeps(t)
			err := processProcessingJobResult(&ipcpb.ProcessingJobResult{JobId: "job", Status: status, OutputPath: "/data/out.mkv", Outputs: map[string]string{"artifact_hash": "asset", "processes_json": "[]"}}, "edge", logging.NewLogger())
			if err == nil || !strings.HasPrefix(err.Error(), "processing result requires tenant_id") {
				t.Fatalf("missing tenant must be rejected before querying: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
