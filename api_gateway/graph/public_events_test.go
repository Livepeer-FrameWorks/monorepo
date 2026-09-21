package graph

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPublicEventTimestampFieldsAreNilWhenUnset(t *testing.T) {
	r := &Resolver{Resolver: &resolvers.Resolver{}}
	ctx := context.Background()

	if got, err := r.ApiTokenCreated().ExpiresAt(ctx, &publicv1.ApiTokenCreated{TokenId: "tok"}); err != nil || got != nil {
		t.Fatalf("ExpiresAt unset = (%v, %v), want (nil, nil)", got, err)
	}
	invoice := &publicv1.InvoiceCreated{InvoiceId: "inv"}
	if got, err := r.InvoiceCreated().PeriodStart(ctx, invoice); err != nil || got != nil {
		t.Fatalf("PeriodStart unset = (%v, %v), want (nil, nil)", got, err)
	}
	if got, err := r.InvoiceCreated().PeriodEnd(ctx, invoice); err != nil || got != nil {
		t.Fatalf("PeriodEnd unset = (%v, %v), want (nil, nil)", got, err)
	}
	if got, err := r.InvoiceCreated().DueAt(ctx, invoice); err != nil || got != nil {
		t.Fatalf("DueAt unset = (%v, %v), want (nil, nil)", got, err)
	}

	due := timestamppb.Now()
	set := &publicv1.InvoiceCreated{InvoiceId: "inv", DueAt: due}
	if got, err := r.InvoiceCreated().DueAt(ctx, set); err != nil || got == nil || !got.Equal(due.AsTime()) {
		t.Fatalf("DueAt set = (%v, %v), want %v", got, err, due.AsTime())
	}
	if _, err := r.PublicEvent().Time(ctx, &signalmanpb.TenantEvent{Id: "no-time"}); err == nil {
		t.Fatal("PublicEvent.time without a time must fail: the field is non-null")
	}
}

func TestPublicEventDataRefusesInternalMessages(t *testing.T) {
	r := &Resolver{Resolver: &resolvers.Resolver{}}
	internal, err := anypb.New(&internalv1.RecordingChapterReady{ChapterId: "chapter-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{"recording.chapter_ready", "recording.ready"} {
		msg, err := r.PublicEvent().Data(context.Background(), &signalmanpb.TenantEvent{Id: "e", Type: eventType, Data: internal})
		if !errors.Is(err, resolvers.ErrNotPublicEvent) || msg != nil {
			t.Fatalf("Data(%s with internal payload) = (%v, %v), want ErrNotPublicEvent", eventType, msg, err)
		}
	}
}

// TestTenantEventsSubscriptionExecutesInDemoMode runs the subscription through
// the generated schema, so the union binding of proto payloads is exercised.
func TestTenantEventsSubscriptionExecutesInDemoMode(t *testing.T) {
	harness := newPlaygroundTestServer()
	raw := executeGraphQL(t, harness, `subscription {
		tenantEvents(types: ["clip.ready"], streamId: "`+demoStreamGlobalID+`") {
			id
			type
			time
			subject
			data {
				__typename
				... on ClipReady { artifact { artifactId kind streamId } sizeBytes }
			}
		}
	}`, nil)
	if len(raw.Errors) > 0 {
		t.Fatalf("tenantEvents errors: %s", formatGraphQLErrors(raw.Errors))
	}

	var resp struct {
		TenantEvents struct {
			ID      string
			Type    string
			Time    string
			Subject string
			Data    struct {
				Typename string `json:"__typename"`
				Artifact struct {
					ArtifactID string
					Kind       string
					StreamID   string
				}
				SizeBytes int64
			}
		}
	}
	if err := json.Unmarshal(raw.Data, &resp); err != nil {
		t.Fatalf("decode first event: %v\n%s", err, raw.Data)
	}
	got := resp.TenantEvents
	if got.Type != "clip.ready" || got.Time == "" || got.Data.Typename != "ClipReady" {
		t.Fatalf("event = %+v, want a clip.ready ClipReady with a time", got)
	}
	if got.Data.Artifact.ArtifactID != demo.DemoClipHash || got.Data.Artifact.StreamID != demo.DemoStreamID || got.Data.Artifact.Kind != "ARTIFACT_KIND_CLIP" {
		t.Fatalf("artifact = %+v", got.Data.Artifact)
	}
}
