package loaders

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"

	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
)

// A registered page larger than one Bosun batch is fetched in bounded
// chunks, and a failed fetch leaves its IDs pending for the next field.
func TestWebhookAttemptsLoaderChunksAndRetries(t *testing.T) {
	var sizes []int
	fail := true
	fake := &clientstest.FakeBosun{
		ListAttemptsForDeliveriesFn: func(_ context.Context, ids []string) (*bosunpb.ListAttemptsForDeliveriesResponse, error) {
			sizes = append(sizes, len(ids))
			if fail {
				fail = false
				return nil, errors.New("bosun down")
			}
			resp := &bosunpb.ListAttemptsForDeliveriesResponse{}
			for _, id := range ids {
				resp.Deliveries = append(resp.Deliveries, &bosunpb.DeliveryAttempts{DeliveryId: id, Attempts: []*bosunpb.WebhookDeliveryAttempt{{Id: "a-" + id}}})
			}
			return resp, nil
		},
	}
	l := NewWebhookAttemptsLoader(fake)
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = fmt.Sprintf("d%03d", i)
	}
	l.Register(ids...)
	if _, err := l.Load(context.Background(), ids[0]); err == nil {
		t.Fatal("load during a Bosun failure succeeded")
	}
	got, err := l.Load(context.Background(), ids[0])
	if err != nil || len(got) != 1 || got[0].GetId() != "a-d000" {
		t.Fatalf("retried load = (%v, %v)", got, err)
	}
	last, err := l.Load(context.Background(), ids[500])
	if err != nil || len(last) != 1 {
		t.Fatalf("last delivery = (%v, %v)", last, err)
	}
	if fmt.Sprint(sizes) != "[500 500 1]" {
		t.Fatalf("batch sizes = %v, want a failed 500, then 500 and 1", sizes)
	}
}
