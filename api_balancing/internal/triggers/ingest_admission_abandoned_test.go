package triggers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/ingesterrors"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func abandonedAdmissionTrigger(nodeID, triggerUUID string, pid int64) *ipcpb.MistTrigger {
	return &ipcpb.MistTrigger{
		TriggerType: "INGEST_ADMISSION_ABANDONED",
		NodeId:      nodeID,
		TriggerPayload: &ipcpb.MistTrigger_IngestAdmissionAbandoned{IngestAdmissionAbandoned: &ipcpb.IngestAdmissionAbandoned{
			TriggerUuid: triggerUUID, ConnectorPid: pid,
		}},
	}
}

func stubAbandonIngestAdmission(t *testing.T, fn func(ctx context.Context, nodeID, triggerUUID string, pid int64, logger logging.Logger) (int, error)) {
	t.Helper()
	prev := abandonIngestAdmission
	abandonIngestAdmission = fn
	t.Cleanup(func() { abandonIngestAdmission = prev })
}

// The report ends the execution on the authenticated node it arrived from, and a database failure
// is returned so Helmsman's WAL redelivers it.
func TestProcessTypedTrigger_IngestAdmissionAbandonedEndsExecutionOnReportingNode(t *testing.T) {
	type call struct {
		node, execution string
		pid             int64
	}
	var calls []call
	failure := errors.New("database unavailable")
	fail := true
	stubAbandonIngestAdmission(t, func(_ context.Context, nodeID, triggerUUID string, pid int64, _ logging.Logger) (int, error) {
		calls = append(calls, call{nodeID, triggerUUID, pid})
		if fail {
			return 0, failure
		}
		return 1, nil
	})
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)

	if _, _, err := p.ProcessTypedTrigger(abandonedAdmissionTrigger("node-1", "exec-1", 41)); !errors.Is(err, failure) {
		t.Fatalf("database failure: err=%v, want it returned for redelivery", err)
	}
	fail = false
	response, abort, err := p.ProcessTypedTrigger(abandonedAdmissionTrigger("node-1", "exec-1", 41))
	if err != nil || abort || response != "" {
		t.Fatalf("report: response=%q abort=%v err=%v", response, abort, err)
	}
	if len(calls) != 2 || calls[1] != (call{"node-1", "exec-1", 41}) {
		t.Fatalf("abandonment calls = %+v", calls)
	}
}

// A report without the execution identity can never succeed; it is refused as terminal so the WAL
// dead-letters it instead of redelivering it forever.
func TestProcessTypedTrigger_IngestAdmissionAbandonedWithoutIdentityIsTerminal(t *testing.T) {
	stubAbandonIngestAdmission(t, func(context.Context, string, string, int64, logging.Logger) (int, error) {
		t.Fatal("an incomplete report reached the database")
		return 0, nil
	})
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	for name, trigger := range map[string]*ipcpb.MistTrigger{
		"no node":      abandonedAdmissionTrigger("", "exec-1", 41),
		"no execution": abandonedAdmissionTrigger("node-1", " ", 41),
		"no pid":       abandonedAdmissionTrigger("node-1", "exec-1", 0),
	} {
		_, _, err := p.ProcessTypedTrigger(trigger)
		ingestErr, ok := errors.AsType[*ingesterrors.IngestError](err)
		if !ok {
			t.Fatalf("%s: err=%v, want an ingest error", name, err)
		}
		if retryable, explicit := ingestErr.RetryableOverride(); !explicit || retryable {
			t.Fatalf("%s: retryable=%v explicit=%v, want terminal", name, retryable, explicit)
		}
	}
}
