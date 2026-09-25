package provisioner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// relayoutTransientBackoff is the wait before each retry of an idempotent relayout query that failed with a
// transient YugabyteDB error; its length bounds the retries.
var relayoutTransientBackoff = []time.Duration{250 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}

// relayoutSleep waits d or until ctx ends; tests replace it.
var relayoutSleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isRelayoutTransient reports whether a relayout query failed in a way YugabyteDB expects the client to retry.
// ysqlsh prints server errors without their SQLSTATE ("ERROR:  Restart read required"), so the client text forms
// are matched in addition to the typed classification the services use.
func isRelayoutTransient(err error) bool {
	if err == nil {
		return false
	}
	if database.IsRetryablePostgresError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "restart read required") ||
		strings.Contains(msg, "could not serialize access") ||
		strings.Contains(msg, "try again")
}

// queryIdempotent runs sql on node, retrying transient YugabyteDB errors. Only statements whose repetition has the
// same effect as one successful run may use it: reads, and whole-database resets such as emptyCopy.
func queryIdempotent(ctx context.Context, node YugabyteNode, database, sql string) (string, error) {
	var err error
	for attempt := 0; ; attempt++ {
		var out string
		out, err = node.Query(ctx, database, sql)
		if err == nil || !isRelayoutTransient(err) || attempt >= len(relayoutTransientBackoff) {
			if err != nil && attempt > 0 {
				err = fmt.Errorf("after %d attempts: %w", attempt+1, err)
			}
			return out, err
		}
		if sleepErr := relayoutSleep(ctx, relayoutTransientBackoff[attempt]); sleepErr != nil {
			return "", fmt.Errorf("%w (retrying after: %w)", sleepErr, err)
		}
	}
}
