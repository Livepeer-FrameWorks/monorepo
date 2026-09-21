package middleware

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// recordingDecklog captures service events and fails the first failFirst sends.
type recordingDecklog struct {
	decklog.Interface

	mu        sync.Mutex
	failFirst int
	attempts  []*ipcpb.ServiceEvent
	delivered []*ipcpb.ServiceEvent
}

func (d *recordingDecklog) SendServiceEvent(event *ipcpb.ServiceEvent) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attempts = append(d.attempts, event)
	if len(d.attempts) <= d.failFirst {
		return errors.New("decklog unavailable")
	}
	d.delivered = append(d.delivered, event)
	return nil
}

func (d *recordingDecklog) SendServiceEventContext(_ context.Context, event *ipcpb.ServiceEvent) error {
	return d.SendServiceEvent(event)
}

func TestUsageTrackerFlushWithNilDecklogDoesNotResetAggregates(t *testing.T) {
	tracker := NewUsageTracker(UsageTrackerConfig{
		Decklog:       nil,
		FlushInterval: time.Hour,
	})
	defer tracker.Stop()

	startedAt := time.Now()
	tracker.Record(startedAt, "tenant-1", "jwt", "query", "GetStreams", []string{"streams"}, "user-1", 0, 100, 5, 0)

	tracker.flush()

	var requestCount uint32
	tracker.aggregates.Range(func(_, value any) bool {
		agg := value.(*aggregate)
		agg.mu.Lock()
		requestCount = agg.RequestCount
		agg.mu.Unlock()
		return false
	})

	if requestCount != 1 {
		t.Fatalf("expected request_count to remain 1 when Decklog is nil, got %d", requestCount)
	}
}

func flushedAggregates(t *testing.T, event *ipcpb.ServiceEvent) []*ipcpb.APIRequestAggregate {
	t.Helper()
	aggs := event.GetApiRequestBatch().GetAggregates()
	sort.Slice(aggs, func(i, j int) bool {
		return rootFieldSignature(aggs[i].GetRootFields()) < rootFieldSignature(aggs[j].GetRootFields())
	})
	return aggs
}

// The usage key includes the root-field signature: the same client-chosen
// operation name (here anonymous) resolving different root fields yields
// separate aggregates, and field order or repetition does not split one.
func TestUsageTrackerAggregatesByRootFieldSignature(t *testing.T) {
	sink := &recordingDecklog{}
	tracker := NewUsageTracker(UsageTrackerConfig{Decklog: sink, FlushInterval: time.Hour, ServiceTenantID: "owner"})
	defer tracker.Stop()

	now := time.Now()
	tracker.Record(now, "tenant-1", "api_token", "query", "", []string{"streams", "clips"}, "user-1", 7, 10, 1, 0)
	tracker.Record(now, "tenant-1", "api_token", "query", "", []string{"clips", "streams", "clips"}, "user-1", 7, 20, 1, 0)
	tracker.Record(now, "tenant-1", "api_token", "query", "", []string{"invoices"}, "user-1", 7, 30, 1, 1)
	tracker.flush()

	if len(sink.delivered) != 1 {
		t.Fatalf("delivered %d batches, want 1", len(sink.delivered))
	}
	aggs := flushedAggregates(t, sink.delivered[0])
	if len(aggs) != 2 {
		t.Fatalf("got %d aggregates, want 2 (one per signature): %v", len(aggs), aggs)
	}
	if got := aggs[0].GetRootFields(); !reflect.DeepEqual(got, []string{"clips", "streams"}) {
		t.Fatalf("first signature = %v, want [clips streams]", got)
	}
	if aggs[0].GetRequestCount() != 2 || aggs[0].GetTotalDurationMs() != 30 {
		t.Fatalf("clips+streams aggregate = %d requests / %d ms, want 2 / 30", aggs[0].GetRequestCount(), aggs[0].GetTotalDurationMs())
	}
	if got := aggs[1].GetRootFields(); !reflect.DeepEqual(got, []string{"invoices"}) {
		t.Fatalf("second signature = %v, want [invoices]", got)
	}
	if aggs[1].GetRequestCount() != 1 || aggs[1].GetErrorCount() != 1 {
		t.Fatalf("invoices aggregate = %d requests / %d errors, want 1 / 1", aggs[1].GetRequestCount(), aggs[1].GetErrorCount())
	}
}

// A batch whose send fails is retried on the next flush with the event ID it
// was first sent with, so Periscope's api_requests keys dedupe the copies.
func TestUsageTrackerRetriesBatchWithStableEventID(t *testing.T) {
	sink := &recordingDecklog{failFirst: 1}
	tracker := NewUsageTracker(UsageTrackerConfig{Decklog: sink, FlushInterval: time.Hour, ServiceTenantID: "owner"})
	defer tracker.Stop()

	tracker.Record(time.Now(), "tenant-1", "jwt", "mutation", "CreateStream", []string{"createStream"}, "user-1", 0, 5, 1, 0)
	tracker.flush()
	tracker.flush()

	if len(sink.attempts) != 2 || len(sink.delivered) != 1 {
		t.Fatalf("attempts=%d delivered=%d, want 2 and 1", len(sink.attempts), len(sink.delivered))
	}
	first, retry := sink.attempts[0].GetEventId(), sink.attempts[1].GetEventId()
	if first == "" || first != retry {
		t.Fatalf("event IDs first=%q retry=%q, want one non-empty ID", first, retry)
	}
	if id, err := uuid.Parse(first); err != nil || id.Version() != 7 {
		t.Fatalf("event ID %q is not a UUIDv7", first)
	}
}

const rootFieldsTestSchema = `
type Query { streams: [String] clips: [String] invoices: [String] }
type Mutation { createStream: String }
`

func operationContext(t *testing.T, query string, vars map[string]any) context.Context {
	t.Helper()
	schema := gqlparser.MustLoadSchema(&ast.Source{Input: rootFieldsTestSchema})
	doc, errs := gqlparser.LoadQueryWithRules(schema, query, nil)
	if errs != nil {
		t.Fatalf("parse %q: %v", query, errs)
	}
	opCtx := &graphql.OperationContext{Doc: doc, Operation: doc.Operations[0], Variables: vars}
	return graphql.WithOperationContext(context.Background(), opCtx)
}

// Root fields are field names after fragments and directives: aliases do not
// hide the field, a skipped field is not counted, and introspection is not API
// surface.
func TestGraphQLRootFieldsResolvesNamesNotAliases(t *testing.T) {
	ctx := operationContext(t, `
		query($skip: Boolean!) {
			mine: streams
			...F
			invoices @skip(if: $skip)
			__typename
		}
		fragment F on Query { clips }`, map[string]any{"skip": true})

	got := GraphQLRootFields(ctx)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"clips", "streams"}) {
		t.Fatalf("root fields = %v, want [clips streams]", got)
	}

	if got := GraphQLRootFields(operationContext(t, `mutation { createStream }`, nil)); !reflect.DeepEqual(got, []string{"createStream"}) {
		t.Fatalf("mutation root fields = %v, want [createStream]", got)
	}
	if got := GraphQLRootFields(context.Background()); got != nil {
		t.Fatalf("root fields without an operation = %v, want nil", got)
	}
}
