package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func grantMessages(sent []*ipcpb.ControlMessage) []*ipcpb.PlaybackGrant {
	var out []*ipcpb.PlaybackGrant
	for _, msg := range sent {
		if g := msg.GetPlaybackGrant(); g != nil {
			out = append(out, g)
		}
	}
	return out
}

func installGrantTestConn(t *testing.T, nodeID string) *mockStream {
	t.Helper()
	oldRegistry := registry
	stream := &mockStream{}
	registry = &Registry{conns: map[string]*conn{nodeID: {stream: stream, last: time.Now()}}, log: logging.NewLogger()}
	t.Cleanup(func() {
		registry = oldRegistry
		forgetPlaybackGrantsSent(nodeID)
	})
	return stream
}

func testGrant(names ...string) *ipcpb.PlaybackGrant {
	return &ipcpb.PlaybackGrant{
		InternalName: "live+s1", RequestedNames: names, TenantId: "tenant-a",
		Policy:     &ipcpb.PlaybackGrantPolicy{Kind: ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC},
		ValidUntil: timestamppb.New(time.Unix(1_900_000_000, 0)),
	}
}

// The admission push goes out once per stream per connection; a new name, a
// changed grant, or a new connection sends it again.
func TestOfferPlaybackGrantSendsOncePerConnectionAndChange(t *testing.T) {
	stream := installGrantTestConn(t, "edge-1")
	for range 3 {
		if _, err := OfferPlaybackGrant("edge-1", testGrant("pb1")); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(grantMessages(stream.sent)); got != 1 {
		t.Fatalf("identical admissions pushed %d grants, want 1", got)
	}
	if _, err := OfferPlaybackGrant("edge-1", testGrant("live+s1")); err != nil {
		t.Fatal(err)
	}
	grants := grantMessages(stream.sent)
	if len(grants) != 2 || len(grants[1].GetRequestedNames()) != 2 {
		t.Fatalf("a new requested name was not merged into the pushed grant: %+v", grants)
	}
	renewed := testGrant("pb1", "live+s1")
	held := time.Unix(1_900_000_000, 0)
	renewed.ValidUntil = timestamppb.New(held.Add(time.Until(held) + time.Hour))
	if _, err := OfferPlaybackGrant("edge-1", renewed); err != nil {
		t.Fatal(err)
	}
	if got := len(grantMessages(stream.sent)); got != 3 {
		t.Fatalf("a renewed grant was not pushed (%d pushes)", got)
	}
	if holders := PlaybackGrantHolders("s1"); len(holders) != 1 || holders[0] != "edge-1" {
		t.Fatalf("holders = %v", holders)
	}
	if streams := PlaybackGrantStreams("", "tenant-a"); len(streams) != 1 || streams[0] != "live+s1" {
		t.Fatalf("tenant streams = %v", streams)
	}
	forgetPlaybackGrantsSent("edge-1")
	if _, err := OfferPlaybackGrant("edge-1", renewed); err != nil {
		t.Fatal(err)
	}
	if got := len(grantMessages(stream.sent)); got != 4 {
		t.Fatalf("a new connection did not get the grant again (%d pushes)", got)
	}
}

// A tenant authority is re-signed whenever its allowances or billing state
// move, which bumps the grant's authority versions and extends its validity by
// minutes without changing what the edge enforces. Such a reissue is not pushed
// while the edge's grant still holds at least half of the new validity; a
// changed policy, a shortened validity, or a held grant past half its renewal
// is.
func TestOfferPlaybackGrantSkipsVersionOnlyReissues(t *testing.T) {
	stream := installGrantTestConn(t, "edge-1")
	now := time.Now()
	issue := func(tenantVersion int64, validFor time.Duration) *ipcpb.PlaybackGrant {
		g := testGrant("pb1")
		g.ObjectAuthorityVersion = 1
		g.TenantAuthorityVersion = tenantVersion
		g.ValidUntil = timestamppb.New(now.Add(validFor))
		return g
	}
	pushes := func() int { return len(grantMessages(stream.sent)) }
	offer := func(g *ipcpb.PlaybackGrant) {
		t.Helper()
		if _, err := OfferPlaybackGrant("edge-1", g); err != nil {
			t.Fatal(err)
		}
	}

	offer(issue(321, 24*time.Hour))
	for v := int64(322); v < 330; v++ {
		offer(issue(v, 24*time.Hour+time.Duration(v-321)*5*time.Minute))
	}
	if got := pushes(); got != 1 {
		t.Fatalf("version-only tenant reissues pushed %d grants, want 1", got)
	}

	offer(issue(330, 2*time.Hour))
	if got := pushes(); got != 2 {
		t.Fatal("a shortened validity was not pushed")
	}
	offer(issue(331, 24*time.Hour))
	if got := pushes(); got != 3 {
		t.Fatal("a renewal of a grant with under half the new validity left was not pushed")
	}
	changed := issue(332, 24*time.Hour)
	changed.Policy = &ipcpb.PlaybackGrantPolicy{Kind: ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_CONNECTED}
	offer(changed)
	if got := pushes(); got != 4 {
		t.Fatal("a policy change was not pushed")
	}
}

func TestPlaybackGrantRequestIsAnsweredAndRecorded(t *testing.T) {
	stream := installGrantTestConn(t, "edge-1")
	SetPlaybackGrantBuilder(func(_ context.Context, nodeID, internalName string) (*ipcpb.PlaybackGrant, error) {
		if internalName != "live+s1" {
			return nil, errors.New("no ready signed media authority for this stream in this cell")
		}
		return testGrant("pb1"), nil
	})
	t.Cleanup(func() { SetPlaybackGrantBuilder(nil) })

	processPlaybackGrantRequest(&ipcpb.PlaybackGrantRequest{RequestId: "r1", InternalName: "live+s1"}, "edge-1", stream, logging.NewLogger())
	processPlaybackGrantRequest(&ipcpb.PlaybackGrantRequest{RequestId: "r2", InternalName: "live+unknown"}, "edge-1", stream, logging.NewLogger())
	if len(stream.sent) != 2 {
		t.Fatalf("sent %d responses", len(stream.sent))
	}
	if resp := stream.sent[0].GetPlaybackGrantResponse(); resp.GetRequestId() != "r1" || resp.GetGrant().GetInternalName() != "live+s1" {
		t.Fatalf("grant response = %+v", resp)
	}
	if resp := stream.sent[1].GetPlaybackGrantResponse(); resp.GetGrant() != nil || resp.GetError() == "" {
		t.Fatalf("refused grant response = %+v", resp)
	}
	if holders := PlaybackGrantHolders("live+s1"); len(holders) != 1 {
		t.Fatalf("a fetched grant does not make the edge a holder: %v", holders)
	}
}
