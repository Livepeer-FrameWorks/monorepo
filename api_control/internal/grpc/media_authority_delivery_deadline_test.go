package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func deliveryDeadlineFixture(t *testing.T, expiry time.Time) commodoredb.ClaimMediaAuthorityDeliveriesRow {
	t.Helper()
	encoded, err := proto.Marshal(&mediapb.SignedAuthorityEnvelope{Envelope: &mediapb.AuthorityEnvelope{Kind: mediapb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, AuthorityId: "live_stream:stream", AuthorityVersion: 1, AudienceCellId: "cell", ValidUntil: timestamppb.New(expiry)}})
	if err != nil {
		t.Fatal(err)
	}
	return commodoredb.ClaimMediaAuthorityDeliveriesRow{AuthorityKind: "media_object", AuthorityID: "live_stream:stream", AuthorityVersion: 1, CellID: "cell", SignedEnvelope: encoded}
}

func TestMediaAuthorityDeliveryRejectsExpiredAndUnboundBeforeNetwork(t *testing.T) {
	for _, scenario := range []string{"expired", "cell", "version", "kind", "unknown kind", "object", "missing envelope", "missing expiry", "invalid expiry", "invalid protobuf"} {
		t.Run(scenario, func(t *testing.T) {
			row := deliveryDeadlineFixture(t, time.Now().Add(time.Minute))
			switch scenario {
			case "expired":
				row = deliveryDeadlineFixture(t, time.Now().Add(-time.Second))
			case "cell":
				row.CellID = "other"
			case "version":
				row.AuthorityVersion++
			case "kind":
				row.AuthorityKind = "tenant"
			case "unknown kind":
				row.AuthorityKind = "other"
			case "object":
				row.AuthorityID = "live_stream:other"
			case "missing envelope":
				row.SignedEnvelope = nil
			case "missing expiry", "invalid expiry":
				signed := &mediapb.SignedAuthorityEnvelope{}
				if err := proto.Unmarshal(row.SignedEnvelope, signed); err != nil {
					t.Fatal(err)
				}
				signed.Envelope.ValidUntil = nil
				if scenario == "invalid expiry" {
					signed.Envelope.ValidUntil = &timestamppb.Timestamp{Nanos: -1}
				}
				var err error
				row.SignedEnvelope, err = proto.Marshal(signed)
				if err != nil {
					t.Fatal(err)
				}
			case "invalid protobuf":
				row.SignedEnvelope = []byte{0xff}
			}
			err := runSignedMediaAuthorityDelivery(context.Background(), row, func(context.Context, commodoredb.ClaimMediaAuthorityDeliveriesRow, *mediapb.SignedAuthorityEnvelope) error {
				t.Fatal("invalid delivery performed network work")
				return nil
			})
			if err == nil {
				t.Fatal("invalid queued authority accepted")
			}
		})
	}
}

func TestMediaAuthorityDeliveryUsesEarlierDeadlineAndRejectsLateSuccess(t *testing.T) {
	for _, shorterCaller := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		expiry := time.Now().Add(time.Second)
		if shorterCaller {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
		}
		row := deliveryDeadlineFixture(t, expiry)
		wantDeadline := expiry
		if callerDeadline, _ := ctx.Deadline(); callerDeadline.Before(wantDeadline) {
			wantDeadline = callerDeadline
		}
		err := runSignedMediaAuthorityDelivery(ctx, row, func(deliveryCtx context.Context, received commodoredb.ClaimMediaAuthorityDeliveriesRow, signed *mediapb.SignedAuthorityEnvelope) error {
			deadline, ok := deliveryCtx.Deadline()
			if !ok || !deadline.Equal(wantDeadline) || received.CellID != "cell" || signed.GetEnvelope().GetAuthorityVersion() != 1 {
				t.Fatal("delivery lost its exact deadline or envelope binding")
			}
			<-deliveryCtx.Done()
			return nil
		})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("late transport success became acknowledgment: %v", err)
		}
	}
	row := deliveryDeadlineFixture(t, time.Now().Add(time.Minute))
	if err := runSignedMediaAuthorityDelivery(context.Background(), row, func(context.Context, commodoredb.ClaimMediaAuthorityDeliveriesRow, *mediapb.SignedAuthorityEnvelope) error {
		return nil
	}); err != nil {
		t.Fatalf("timely delivery rejected: %v", err)
	}
}
