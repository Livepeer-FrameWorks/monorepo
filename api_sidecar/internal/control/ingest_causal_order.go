package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// Per-runtime causal order between the durable end triggers of a Mist runtime (PUSH_INPUT_CLOSE,
// STREAM_END, forwarded through the WAL) and the next PUSH_REWRITE admitting a publisher into that
// runtime (forwarded synchronously). Foghorn refuses a second publisher on the same node while the
// previous connection's session is open, so the new admission must not reach Foghorn before the old
// connection's close. PUSH_REWRITE carries the stream key, not the runtime; the runtime a key was
// last admitted into is remembered at acceptance and persisted with the generation record.

// pushKeyRuntimes maps an admission key digest to the runtime Foghorn admitted it into.
var pushKeyRuntimes = struct {
	sync.Mutex
	byKey map[string]string
}{byKey: make(map[string]string)}

// priorityRuntimes counts, per runtime, the admissions waiting on its pending end triggers; the
// forwarder delivers those entries ahead of the rest of the WAL while the count is positive.
var priorityRuntimes = struct {
	sync.Mutex
	waiters map[string]int
}{waiters: make(map[string]int)}

// pushAdmissionKey is the one-way digest under which a PUSH_REWRITE stream key is remembered.
func pushAdmissionKey(streamKey string) string {
	streamKey = strings.TrimSpace(streamKey)
	if streamKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("frameworks:push-rewrite-key:" + streamKey))
	return hex.EncodeToString(sum[:])
}

func rememberAdmittedRuntime(admissionKey, runtimeName string) {
	if admissionKey == "" || runtimeName == "" {
		return
	}
	pushKeyRuntimes.Lock()
	pushKeyRuntimes.byKey[admissionKey] = runtimeName
	pushKeyRuntimes.Unlock()
}

func admittedRuntimeForKey(admissionKey string) (string, bool) {
	pushKeyRuntimes.Lock()
	defer pushKeyRuntimes.Unlock()
	runtime, ok := pushKeyRuntimes.byKey[admissionKey]
	return runtime, ok
}

func forgetAdmittedRuntimes(runtimeNames []string) {
	if len(runtimeNames) == 0 {
		return
	}
	evicted := make(map[string]struct{}, len(runtimeNames))
	for _, name := range runtimeNames {
		evicted[name] = struct{}{}
	}
	pushKeyRuntimes.Lock()
	for key, runtime := range pushKeyRuntimes.byKey {
		if _, gone := evicted[runtime]; gone {
			delete(pushKeyRuntimes.byKey, key)
		}
	}
	pushKeyRuntimes.Unlock()
}

func requestRuntimeFlush(runtime string) {
	priorityRuntimes.Lock()
	priorityRuntimes.waiters[runtime]++
	priorityRuntimes.Unlock()
	wakeupTriggerForwarder()
}

func releaseRuntimeFlush(runtime string) {
	priorityRuntimes.Lock()
	if priorityRuntimes.waiters[runtime] <= 1 {
		delete(priorityRuntimes.waiters, runtime)
	} else {
		priorityRuntimes.waiters[runtime]--
	}
	priorityRuntimes.Unlock()
}

// awaitPushRewriteCausalOrder holds a PUSH_REWRITE until the WAL has delivered every pending end
// trigger of the runtime its stream key was last admitted into. Those entries are delivered ahead of
// the rest of the WAL. A runtime with nothing pending, or a key never admitted here, proceeds at once.
// If the entries are still pending at deadline Foghorn has not received them and the admission would
// be refused as a duplicate of the old connection, so the PUSH_REWRITE fails with an error.
func awaitPushRewriteCausalOrder(ctx context.Context, deadline time.Time, trigger *ipcpb.MistTrigger, logger logging.Logger) error {
	wal := triggerWAL
	if wal == nil {
		return nil
	}
	admissionKey := pushAdmissionKey(trigger.GetPushRewrite().GetStreamName())
	if admissionKey == "" {
		return nil
	}
	runtime, known := admittedRuntimeForKey(admissionKey)
	if !known {
		return nil
	}
	pending, changed := wal.PendingForRuntime(runtime)
	if pending == 0 {
		return nil
	}
	started := time.Now()
	fields := logging.Fields{"runtime_name": runtime, "pending_end_triggers": pending}
	logger.WithFields(fields).Info("PUSH_REWRITE waits for the runtime's undelivered end triggers to reach Foghorn first")
	requestRuntimeFlush(runtime)
	defer releaseRuntimeFlush(runtime)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for pending > 0 {
		select {
		case <-changed:
			pending, changed = wal.PendingForRuntime(runtime)
		case <-ctx.Done():
			fields["waited"] = time.Since(started).String()
			logger.WithFields(fields).Warn("Refusing PUSH_REWRITE: request ended while the runtime's previous end triggers were undelivered")
			return fmt.Errorf("runtime %s end triggers undelivered: %w", runtime, ctx.Err())
		case <-timer.C:
			fields["pending_end_triggers"] = pending
			fields["waited"] = time.Since(started).String()
			logger.WithFields(fields).Warn("Refusing PUSH_REWRITE: the runtime's previous end triggers could not be delivered to Foghorn within the admission budget")
			return fmt.Errorf("runtime %s still has %d undelivered end triggers", runtime, pending)
		}
	}
	logger.WithFields(logging.Fields{"runtime_name": runtime, "waited": time.Since(started).String()}).
		Info("Runtime's end triggers delivered; forwarding PUSH_REWRITE")
	return nil
}

// priorityRuntimeNames lists the runtimes an admission waits on; the forwarder delivers their
// pending end triggers ahead of every other entry.
func priorityRuntimeNames() []string {
	priorityRuntimes.Lock()
	defer priorityRuntimes.Unlock()
	runtimes := make([]string, 0, len(priorityRuntimes.waiters))
	for runtime := range priorityRuntimes.waiters {
		runtimes = append(runtimes, runtime)
	}
	return runtimes
}
