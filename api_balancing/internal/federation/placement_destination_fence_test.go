package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"google.golang.org/protobuf/proto"
)

func TestPreparedSourceReconnectRequiresFreshAuthorizationButReusesPull(t *testing.T) {
	destination, media, _, _, fed, req := pushRuntimeFixture(t)
	ctx := context.Background()
	fence := int64(9007199254740993)
	media.DestinationFence = func(context.Context, string, string) (int64, error) { return fence, nil }
	if _, err := destination.PreparePlacement(ctx, req); err != nil {
		t.Fatal(err)
	}
	identity := pushSourceIdentity(req)
	first, err := media.ResolvePreparedSource(ctx, destination.Receipts, identity)
	if err != nil {
		t.Fatal(err)
	}
	fence++
	if _, err = destination.PreparePlacement(ctx, req); err == nil {
		t.Fatal("reconnect replayed old preparation")
	}
	if _, err = media.ResolvePreparedSource(ctx, destination.Receipts, identity); err == nil {
		t.Fatal("retired connection consumed old authorization")
	}
	identity.DestinationFence = fence
	if _, err = media.ResolvePreparedSource(ctx, destination.Receipts, identity); err == nil {
		t.Fatal("new connection consumed old authorization")
	}
	next := proto.CloneOf(req)
	next.AttemptId, err = placement.NewPreparationAttemptID(destination.now().Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = destination.PreparePlacement(ctx, next); err != nil {
		t.Fatal(err)
	}
	second, err := media.ResolvePreparedSource(ctx, destination.Receipts, identity)
	if err != nil || second.AttemptID != first.AttemptID || second.DTSCURL != first.DTSCURL || len(fed.calls) != 1 {
		t.Fatalf("fresh authorization failed to reuse physical pull: %+v, %v, notifications=%d", second, err, len(fed.calls))
	}
	if _, err = media.ResolvePreparedSource(ctx, destination.Receipts, pushSourceIdentity(req)); err == nil {
		t.Fatal("fresh authorization revived retired connection")
	}
}

func TestPushPreparationRequiresKnownDestinationFence(t *testing.T) {
	for _, outcome := range []string{"missing", "zero", "negative", "unavailable", "reconnect-during-arrangement"} {
		t.Run(outcome, func(t *testing.T) {
			destination, media, _, _, fed, req := pushRuntimeFixture(t)
			fence := int64(9007199254740993)
			media.DestinationFence = func(context.Context, string, string) (int64, error) { return fence, nil }
			switch outcome {
			case "missing":
				media.DestinationFence = nil
			case "zero":
				fence = 0
			case "negative":
				fence = -1
			case "unavailable":
				media.DestinationFence = func(context.Context, string, string) (int64, error) { return 0, errors.New("ownership unavailable") }
			case "reconnect-during-arrangement":
				fed.mutateAck = func(ack *federationpb.OriginPullAck) {
					ack.DtscUrl = "dtsc://eu.example:14200/live+internal"
					fence++
				}
			}
			if result, err := destination.PreparePlacement(context.Background(), req); err == nil || result != nil {
				t.Fatalf("unbound destination accepted: %v, %v", result, err)
			}
			if outcome != "reconnect-during-arrangement" && len(fed.calls) != 0 {
				t.Fatal("source notified without destination ownership")
			}
		})
	}
}

func TestPreparedSourceRechecksDestinationOwnershipAfterResolution(t *testing.T) {
	destination, media, _, _, _, req := pushRuntimeFixture(t)
	ctx := context.Background()
	if _, err := destination.PreparePlacement(ctx, req); err != nil {
		t.Fatal(err)
	}
	reads := 0
	media.DestinationFence = func(context.Context, string, string) (int64, error) {
		reads++
		if reads == 1 {
			return 9007199254740993, nil
		}
		return 9007199254740994, nil
	}
	if result, err := media.ResolvePreparedSource(ctx, destination.Receipts, pushSourceIdentity(req)); err == nil || result.DTSCURL != "" || reads != 2 {
		t.Fatalf("connection changed after revalidation but source escaped: %+v, %v, reads=%d", result, err, reads)
	}
}
