package metering

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type captureUsagePublisher struct{ events []*ipcpb.ServiceEvent }

func (p *captureUsagePublisher) SendServiceEvent(event *ipcpb.ServiceEvent) error {
	p.events = append(p.events, event)
	return nil
}

func TestUsageTrackerPersistsTenantUsage(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tracker := NewUsageTracker(UsageTrackerConfig{
		DB:    db,
		Model: "gpt-test",
	})

	tracker.RecordLLMCall("tenant-a", 10, 5)

	mock.ExpectExec("INSERT INTO skipper\\.skipper_usage").WithArgs(
		"tenant-a",
		"llm_call",
		1,
		10,
		5,
		sqlmock.AnyArg(),
		sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))

	tracker.Flush(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestUsageTrackerRetriesFailedPersistence(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tracker := NewUsageTracker(UsageTrackerConfig{
		DB:    db,
		Model: "gpt-test",
	})

	tracker.RecordLLMCall("tenant-a", 10, 5)

	mock.ExpectExec("INSERT INTO skipper\\.skipper_usage").WithArgs(
		"tenant-a",
		"llm_call",
		1,
		10,
		5,
		sqlmock.AnyArg(),
		sqlmock.AnyArg(),
	).WillReturnError(sqlmock.ErrCancelled)

	tracker.Flush(context.Background())

	mock.ExpectExec("INSERT INTO skipper\\.skipper_usage").WithArgs(
		"tenant-a",
		"llm_call",
		1,
		10,
		5,
		sqlmock.AnyArg(),
		sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))

	tracker.Flush(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestUsageTrackerPublishesPersistedUsageWithStableIdentity(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	publisher := &captureUsagePublisher{}
	tracker := NewUsageTracker(UsageTrackerConfig{DB: db, Publisher: publisher})
	createdAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	id := "11111111-1111-4111-8111-111111111111"

	mock.ExpectBegin()
	mock.ExpectQuery("FROM skipper\\.skipper_usage").WillReturnRows(sqlmock.NewRows([]string{
		"id", "tenant_id", "event_type", "event_count", "tokens_input", "tokens_output", "model", "provider", "created_at",
	}).AddRow(id, "22222222-2222-4222-8222-222222222222", "llm_call", 2, 30, 40, "gpt-test", "openai", createdAt))
	mock.ExpectExec("SET claimed_at = NOW").WithArgs("{\"" + id + "\"}").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec("SET published_at = NOW").WithArgs(id).WillReturnResult(sqlmock.NewResult(0, 1))

	if err := tracker.publishPending(context.Background()); err != nil {
		t.Fatalf("publish pending: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(publisher.events))
	}
	event := publisher.events[0]
	if event.GetEventId() != id {
		t.Fatalf("event id = %q, want stable row id %q", event.GetEventId(), id)
	}
	agg := event.GetApiRequestBatch().GetAggregates()[0]
	if agg.GetRequestCount() != 2 || agg.GetLlmInputTokens() != 30 || agg.GetLlmOutputTokens() != 40 {
		t.Fatalf("unexpected aggregate: %#v", agg)
	}
	if agg.GetModel() != "gpt-test" || agg.GetProvider() != "openai" {
		t.Fatalf("missing model/provider: %#v", agg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
