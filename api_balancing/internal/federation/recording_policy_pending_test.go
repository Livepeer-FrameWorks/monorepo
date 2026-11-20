package federation

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/dvrpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
)

func TestPendingRecordingPolicyCannotEnableOfflineAdmission(t *testing.T) {
	err := grpcutil.SanitizeError(dvrpolicy.RecordingPolicyPendingError())
	if !dvrpolicy.IsRecordingPolicyPending(err) {
		t.Fatalf("service boundary dropped recording policy state: %v", err)
	}
	if CentralUnreachable(err) {
		t.Fatal("a reachable owner with unpublished settings enabled offline admission")
	}
}
