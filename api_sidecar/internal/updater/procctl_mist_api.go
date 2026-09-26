package updater

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_sidecar/internal/appconfig"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

var (
	mistAPIReadyTimeout = 60 * time.Second
	mistAPIAttemptLimit = 5 * time.Second
	mistAPIReadyPoll    = time.Second
)

// waitMistAPIReady proves the controller serves its API. A controller can
// hold the listen socket while never accepting, so a TCP connect is not
// enough: an authenticated call must return.
func waitMistAPIReady(ctx context.Context) error {
	client := mist.NewClient(logging.NewLoggerWithService("helmsman-updater"), appconfig.MistClient())
	deadline := time.Now().Add(mistAPIReadyTimeout)
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, mistAPIAttemptLimit)
		_, err := client.GetActiveStreamsContext(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("MistServer API did not answer within %s: %w", mistAPIReadyTimeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(mistAPIReadyPoll):
		}
	}
}
