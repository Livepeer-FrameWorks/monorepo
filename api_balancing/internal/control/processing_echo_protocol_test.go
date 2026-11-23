package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestProcessingDispatchRequiresTenantEchoDuringRollingUpgrade(t *testing.T) {
	previous := registry
	t.Cleanup(func() { registry = previous })
	for _, version := range []int32{12, 13} {
		stream := &mockStream{}
		registry = &Registry{conns: map[string]*conn{"edge": {
			stream: stream, rawNodeID: "edge", canonicalID: "edge", protocolVersion: version,
		}}, log: logging.NewLogger()}
		request := &ipcpb.ProcessingJobRequest{JobId: "job", TenantId: "tenant"}
		// v0.3.15 returns this shape even when the request supplies its tenant.
		legacyResult := &ipcpb.ProcessingJobResult{JobId: request.JobId, Status: "completed", OutputPath: "/vod/result.mkv"}
		if legacyResult.GetOutputs()[mist.ProcessingResultTenantID] != "" {
			t.Fatal("legacy fixture unexpectedly echoes the tenant")
		}
		err := SendLocalProcessingJob("edge", request)
		if version == 12 {
			if status.Code(err) != codes.FailedPrecondition || len(stream.sent) != 0 {
				t.Fatalf("pre-echo edge received processing whose legacy result would be dropped: error=%v sent=%d", err, len(stream.sent))
			}
		} else if err != nil || len(stream.sent) != 1 {
			t.Fatalf("upgraded edge did not receive processing: error=%v sent=%d", err, len(stream.sent))
		}
	}
}
