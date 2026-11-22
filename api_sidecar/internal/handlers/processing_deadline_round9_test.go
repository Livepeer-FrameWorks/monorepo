package handlers

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestR9ExpiredDeadlineAppliesBeforeEveryJobHandler(t *testing.T) {
	for _, kind := range []string{"process", "clip", mist.ProcessingJobTypeThumbnail, "dvr_chapter_finalize"} {
		t.Run(kind, func(t *testing.T) {
			h := &ProcessingJobHandler{logger: discardLog().Logger, storagePath: t.TempDir()}
			req := &ipcpb.ProcessingJobRequest{JobId: "expired", JobType: kind, TenantId: "tenant", DeadlineUnixMs: time.Now().Add(-time.Second).UnixMilli()}
			if kind == "clip" {
				req.Params = map[string]string{"source_kind": "live"}
			}
			var result *ipcpb.ProcessingJobResult
			h.Handle(req, func(message *ipcpb.ControlMessage) {
				if r := message.GetProcessingJobResult(); r != nil {
					result = r
				}
			})
			if result == nil || result.GetStatus() != "failed" || result.GetError() != "processing timed out after 24 hours" || result.GetOutputs()[mist.ProcessingResultTenantID] != "tenant" {
				t.Fatalf("expired %s reached a specialized handler: %v", kind, result)
			}
		})
	}
}
