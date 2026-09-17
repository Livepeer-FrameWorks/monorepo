package ownership

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"frameworks/api_incidents/internal/incidents"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
)

type fakeReconciler struct {
	clusters     []string
	clusterErrs  []error
	startupCalls int
	startupErrs  []error
}

func (f *fakeReconciler) ReconcileClusterScope(_ context.Context, clusterID string) (int, error) {
	f.clusters = append(f.clusters, clusterID)
	if len(f.clusterErrs) > 0 {
		err := f.clusterErrs[0]
		f.clusterErrs = f.clusterErrs[1:]
		if err != nil {
			return 0, err
		}
	}
	return 1, nil
}

func (f *fakeReconciler) ReconcileOpenIncidentScopes(context.Context) (int, error) {
	f.startupCalls++
	if len(f.startupErrs) > 0 {
		err := f.startupErrs[0]
		f.startupErrs = f.startupErrs[1:]
		return 0, err
	}
	return 0, nil
}

func serviceEventMessage(t *testing.T, event kafka.ServiceEvent) kafka.Message {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return kafka.Message{Topic: "service_events", Value: raw}
}

func unverified(clusterID string) error {
	return fmt.Errorf("%w: cluster %s", incidents.ErrScopeUnverified, clusterID)
}

func TestHandleReconcilesClusterCreatedAndUpdated(t *testing.T) {
	reconciler := &fakeReconciler{}
	consumer := &Consumer{Reconciler: reconciler}
	ctx := context.Background()
	messages := []kafka.Message{
		serviceEventMessage(t, kafka.ServiceEvent{EventType: "cluster_created", ResourceType: "cluster", ResourceID: "created", Data: map[string]any{"cluster_id": "created"}}),
		serviceEventMessage(t, kafka.ServiceEvent{EventType: "api_request_batch", Data: map[string]any{}}),
		serviceEventMessage(t, kafka.ServiceEvent{EventType: "incident_updated", Data: map[string]any{"cluster_id": "incident-cluster"}}),
		serviceEventMessage(t, kafka.ServiceEvent{EventType: "cluster_updated", ResourceType: "cluster", ResourceID: "from-data", Data: map[string]any{"cluster_id": "from-data"}}),
		serviceEventMessage(t, kafka.ServiceEvent{EventType: "cluster_updated", ResourceType: "cluster", ResourceID: "from-resource", Data: map[string]any{}}),
		serviceEventMessage(t, kafka.ServiceEvent{EventType: "cluster_updated", Data: map[string]any{}}),
		{Topic: "service_events", Value: []byte("{not json")},
	}
	for _, msg := range messages {
		if err := consumer.Handle(ctx, msg); err != nil {
			t.Fatalf("handle %s: %v", msg.Value, err)
		}
	}
	if want := []string{"created", "from-data", "from-resource"}; !reflect.DeepEqual(reconciler.clusters, want) {
		t.Fatalf("reconciled clusters = %v, want %v", reconciler.clusters, want)
	}
}

func TestHandleReturnsUnverifiedOwner(t *testing.T) {
	reconciler := &fakeReconciler{clusterErrs: []error{unverified("c1")}}
	consumer := &Consumer{Reconciler: reconciler}
	msg := serviceEventMessage(t, kafka.ServiceEvent{EventType: "cluster_updated", Data: map[string]any{"cluster_id": "c1"}})
	if err := consumer.Handle(context.Background(), msg); !errors.Is(err, incidents.ErrScopeUnverified) {
		t.Fatalf("handle = %v, want ErrScopeUnverified", err)
	}
}

func TestHandleUntilAppliedRetriesWithBackoff(t *testing.T) {
	reconciler := &fakeReconciler{clusterErrs: []error{unverified("c1"), errors.New("database down"), unverified("c1"), nil}}
	var delays []time.Duration
	consumer := &Consumer{
		Reconciler: reconciler,
		RetryBase:  time.Second,
		RetryMax:   3 * time.Second,
		Sleep: func(_ context.Context, d time.Duration) error {
			delays = append(delays, d)
			return nil
		},
	}
	msg := serviceEventMessage(t, kafka.ServiceEvent{EventType: "cluster_updated", Data: map[string]any{"cluster_id": "c1"}})
	if err := consumer.HandleUntilApplied(context.Background(), msg); err != nil {
		t.Fatalf("handle until applied = %v", err)
	}
	if len(reconciler.clusters) != 4 {
		t.Fatalf("attempts = %d, want 4", len(reconciler.clusters))
	}
	if want := []time.Duration{time.Second, 2 * time.Second, 3 * time.Second}; !reflect.DeepEqual(delays, want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
}

func TestHandleUntilAppliedStopsWhenContextEnds(t *testing.T) {
	reconciler := &fakeReconciler{clusterErrs: []error{unverified("c1")}}
	ctx, cancel := context.WithCancel(context.Background())
	consumer := &Consumer{
		Reconciler: reconciler,
		Sleep: func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		},
	}
	msg := serviceEventMessage(t, kafka.ServiceEvent{EventType: "cluster_updated", Data: map[string]any{"cluster_id": "c1"}})
	err := consumer.HandleUntilApplied(ctx, msg)
	if !errors.Is(err, incidents.ErrScopeUnverified) || !errors.Is(err, context.Canceled) {
		t.Fatalf("handle until applied = %v, want unverified and cancelled", err)
	}
}

func TestReconcileAtStartupRetriesUntilVerified(t *testing.T) {
	reconciler := &fakeReconciler{startupErrs: []error{unverified("c1"), unverified("c1")}}
	sleeps := 0
	consumer := &Consumer{
		Reconciler: reconciler,
		Sleep: func(context.Context, time.Duration) error {
			sleeps++
			return nil
		},
	}
	consumer.ReconcileAtStartup(context.Background())
	if reconciler.startupCalls != 3 || sleeps != 2 {
		t.Fatalf("startup calls = %d sleeps = %d, want 3 and 2", reconciler.startupCalls, sleeps)
	}
}
