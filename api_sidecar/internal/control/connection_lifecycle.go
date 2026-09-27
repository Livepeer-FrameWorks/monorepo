package control

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// Foghorn places publishers on a node only on that node's own telemetry, and a Foghorn that has
// not heard from the node (a fresh replica, or one that dropped the node while it was away) has
// none. A newly registered connection therefore carries a full node lifecycle report before it is
// published: blocking triggers, the durable outbox and the trigger WAL all start after it.

// connectionLifecycleBudget bounds building and sending the first report. It is half of a blocking
// trigger's budget, so an admission already waiting for this connection keeps the other half.
const connectionLifecycleBudget = blockingTriggerTimeout / 2

var (
	connectionLifecycleReporterMu sync.Mutex
	connectionLifecycleReporter   func(context.Context) (*ipcpb.MistTrigger, error)

	// controlRegistrationPending is true from a successful Register until that connection is
	// published, the window in which its first node lifecycle report is being sent.
	controlRegistrationPending atomic.Bool
)

// SetConnectionNodeLifecycleReporter installs the builder of the node lifecycle report every new
// control connection sends first.
func SetConnectionNodeLifecycleReporter(fn func(context.Context) (*ipcpb.MistTrigger, error)) {
	connectionLifecycleReporterMu.Lock()
	connectionLifecycleReporter = fn
	connectionLifecycleReporterMu.Unlock()
}

// sendConnectionNodeLifecycle sends the first node lifecycle report on a registered, not yet
// published connection. Every way it can fail is logged; the connection is published regardless,
// and Foghorn then learns this node's telemetry from the next periodic report.
func sendConnectionNodeLifecycle(connection *streamConn, logger logging.Logger) {
	connectionLifecycleReporterMu.Lock()
	reporter := connectionLifecycleReporter
	connectionLifecycleReporterMu.Unlock()
	if reporter == nil {
		logger.Warn("New control connection sends no first node lifecycle report: no reporter is installed")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectionLifecycleBudget)
	defer cancel()
	started := time.Now()
	trigger, err := reporter(ctx)
	if err != nil || trigger == nil {
		logger.WithError(err).Warn("New control connection sends no first node lifecycle report: the report could not be built")
		return
	}
	msg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_MistTrigger{MistTrigger: trigger}}
	if contextStream, ok := connection.stream.(interface {
		SendContext(context.Context, *ipcpb.ControlMessage) error
	}); ok {
		err = contextStream.SendContext(ctx, msg)
	} else {
		err = connection.stream.Send(msg)
	}
	if err != nil {
		logger.WithError(err).Warn("New control connection's first node lifecycle report was not sent")
		return
	}
	logger.WithField("took", time.Since(started).String()).Info("Sent the first node lifecycle report on the new control connection")
}
