package triggers

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/federation"
)

// contractPeerNotifier records offline broadcasts and applies the real PeerManager's stream
// untracking contract, so a caller that can never satisfy it fails exactly as in a cell.
type contractPeerNotifier struct {
	recordingPeerNotifier
	real *federation.PeerManager
}

func (c *contractPeerNotifier) UntrackStream(ctx context.Context, name, tenantID, generation string, revision int64) error {
	if err := c.real.UntrackStream(ctx, name, tenantID, generation, revision); err != nil {
		return err
	}
	return c.recordingPeerNotifier.UntrackStream(ctx, name, tenantID, generation, revision)
}

// Federation membership is only ever tracked for a tenant-owned publisher generation, so an
// offline effect without one (a pull buffer, or a source whose owner is unresolved) has
// nothing to untrack. Its broadcast leg must still settle instead of retrying forever.
func TestGenerationlessStreamOfflineSettlesWithoutUntracking(t *testing.T) {
	for _, tc := range []struct {
		name, tenant string
		broadcasts   int
	}{
		{"pull buffer without a publisher generation", "8d3f0f34-f4dd-46b8-b48d-ace2354c445e", 1},
		{"source with unresolved ownership", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notifier := &contractPeerNotifier{real: &federation.PeerManager{}}
			p := &Processor{peerNotifier: notifier}
			if err := p.broadcastStreamOffline(context.Background(), "RxuIQDSPNtHz6RfQHww1NFau4gbRoZDW", tc.tenant, "", 4503599627370502); err != nil {
				t.Fatalf("generationless offline broadcast leg cannot settle: %v", err)
			}
			if offline, untracked := notifier.counts(); offline != tc.broadcasts || untracked != 0 {
				t.Fatalf("offline broadcasts=%d untracks=%d, want %d and 0", offline, untracked, tc.broadcasts)
			}
		})
	}
}

// A tracked publisher generation is still untracked after its offline broadcast.
func TestGenerationStreamOfflineUntracksAfterBroadcast(t *testing.T) {
	notifier := &recordingPeerNotifier{}
	p := &Processor{peerNotifier: notifier}
	if err := p.broadcastStreamOffline(context.Background(), "stream", "8d3f0f34-f4dd-46b8-b48d-ace2354c445e", "804407cc-6931-43de-b38b-72ac185fc0c1", 7); err != nil {
		t.Fatal(err)
	}
	if offline, untracked := notifier.counts(); offline != 1 || untracked != 1 {
		t.Fatalf("offline broadcasts=%d untracks=%d, want 1 and 1", offline, untracked)
	}
}
