package main

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

type blockingBootstrapper struct{}

func (b *blockingBootstrapper) BootstrapService(ctx context.Context, req *pb.BootstrapServiceRequest) (*pb.BootstrapServiceResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestBootstrapSignalmanServiceTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := bootstrapSignalmanService(ctx, &blockingBootstrapper{}, &pb.BootstrapServiceRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestEventToProtoDataLogsMarshalFailure(t *testing.T) {
	logger, hook := logrustest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)

	data := map[string]interface{}{
		"bad": func() {},
	}
	eventData := eventToProtoData(data, logger)

	if eventData == nil {
		t.Fatalf("expected event data, got nil")
	}
	if eventData.Payload != nil {
		t.Fatalf("expected empty payload on marshal failure")
	}

	if len(hook.Entries) == 0 {
		t.Fatalf("expected debug log for marshal failure")
	}

	entry := hook.LastEntry()
	if entry.Message != "Failed to marshal event data" {
		t.Fatalf("unexpected log message: %s", entry.Message)
	}
	if entry.Data["error"] == nil {
		t.Fatalf("expected error field on log entry")
	}
}
