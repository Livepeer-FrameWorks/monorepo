package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
)

type fakeClickhouse struct {
	batch      *fakeBatch
	prepareErr error
}

func (f *fakeClickhouse) PrepareBatch(_ context.Context, _ string) (clickhouseBatch, error) {
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	return f.batch, nil
}

func (f *fakeClickhouse) Query(_ context.Context, _ string, _ ...interface{}) (clickhouseRows, error) {
	return &fakeRows{}, nil
}

type fakeBatch struct {
	appendErrAt int
	appendCalls int
	sendErr     error
	sendCalled  bool
}

func (f *fakeBatch) Append(_ ...interface{}) error {
	f.appendCalls++
	if f.appendErrAt > 0 && f.appendCalls == f.appendErrAt {
		return errors.New("append failed")
	}
	return nil
}

func (f *fakeBatch) Send() error {
	f.sendCalled = true
	return f.sendErr
}

type fakeRows struct{}

func (f *fakeRows) Next() bool   { return false }
func (f *fakeRows) Close() error { return nil }

func TestProcessAPIRequestBatchAppendFailureReturnsError(t *testing.T) {
	batch := &fakeBatch{appendErrAt: 2}
	handler := &AnalyticsHandler{
		clickhouse: &fakeClickhouse{batch: batch},
		logger:     logging.NewLoggerWithService("test"),
	}

	event := buildAPIRequestBatchEvent(t, []*pb.APIRequestAggregate{
		{TenantId: uuid.NewString(), AuthType: "jwt", OperationType: "query", RequestCount: 1},
		{TenantId: uuid.NewString(), AuthType: "jwt", OperationType: "query", RequestCount: 1},
	})

	if err := handler.processAPIRequestBatch(context.Background(), event); err == nil {
		t.Fatal("expected append error, got nil")
	}
	if batch.sendCalled {
		t.Fatal("expected batch.Send to be skipped after append failure")
	}
}

func TestProcessAPIRequestBatchSendFailureReturnsError(t *testing.T) {
	batch := &fakeBatch{sendErr: errors.New("send failed")}
	handler := &AnalyticsHandler{
		clickhouse: &fakeClickhouse{batch: batch},
		logger:     logging.NewLoggerWithService("test"),
	}

	event := buildAPIRequestBatchEvent(t, []*pb.APIRequestAggregate{
		{TenantId: uuid.NewString(), AuthType: "jwt", OperationType: "query", RequestCount: 1},
	})

	if err := handler.processAPIRequestBatch(context.Background(), event); err == nil {
		t.Fatal("expected send error, got nil")
	}
	if !batch.sendCalled {
		t.Fatal("expected batch.Send to be called")
	}
}

func TestProcessServiceAPIRequestBatchAppendFailureReturnsError(t *testing.T) {
	batch := &fakeBatch{appendErrAt: 1}
	handler := &AnalyticsHandler{
		clickhouse: &fakeClickhouse{batch: batch},
		logger:     logging.NewLoggerWithService("test"),
	}

	event := kafka.ServiceEvent{
		EventID:   "evt",
		EventType: "api_request_batch",
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"timestamp":   float64(time.Now().Unix()),
			"source_node": "node",
			"aggregates": []interface{}{
				map[string]interface{}{
					"tenant_id":         uuid.NewString(),
					"auth_type":         "jwt",
					"operation_type":    "query",
					"operation_name":    "GetStreams",
					"request_count":     uint64(1),
					"error_count":       uint64(0),
					"total_duration_ms": uint64(10),
					"total_complexity":  uint64(2),
				},
			},
		},
	}

	if err := handler.processServiceAPIRequestBatch(context.Background(), event); err == nil {
		t.Fatal("expected append error, got nil")
	}
	if batch.sendCalled {
		t.Fatal("expected batch.Send to be skipped after append failure")
	}
}

func buildAPIRequestBatchEvent(t *testing.T, aggregates []*pb.APIRequestAggregate) kafka.AnalyticsEvent {
	t.Helper()

	mt := &pb.MistTrigger{
		TriggerPayload: &pb.MistTrigger_ApiRequestBatch{
			ApiRequestBatch: &pb.APIRequestBatch{
				Timestamp:  time.Now().Unix(),
				SourceNode: "node",
				Aggregates: aggregates,
			},
		},
	}

	payload, err := protojson.Marshal(mt)
	if err != nil {
		t.Fatalf("failed to marshal proto: %v", err)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(payload, &data); err != nil {
		t.Fatalf("failed to unmarshal proto JSON: %v", err)
	}

	return kafka.AnalyticsEvent{
		EventID:   "evt",
		EventType: "api_request_batch",
		Timestamp: time.Now(),
		Data:      data,
	}
}
