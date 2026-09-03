package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	streamWSInitialBackoff = time.Second
	streamWSMaxBackoff     = 30 * time.Second
	streamWSHealthyAfter   = 60 * time.Second
	streamWSWarmupWindow   = 500 * time.Millisecond
	streamWSBatchDelay     = 10 * time.Millisecond
	streamWSMinRefresh     = 2 * time.Second
	streamWSFullSweepDelay = 2 * time.Second
	streamWSSweepBusyDelay = 100 * time.Millisecond
	streamWSPongWait       = 60 * time.Second
	streamWSPingInterval   = 25 * time.Second
	streamWSWriteWait      = 10 * time.Second
	streamWSDialer         = websocket.DefaultDialer
)

const (
	maxStreamWSStateEntries      = 8192
	maxStreamWSRefreshBatchNames = 64
	maxStreamWSRefreshBatchBytes = 3500
)

var (
	streamWSConnections = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "helmsman",
		Name:      "stream_ws_connections",
		Help:      "Whether the MistController stream change WebSocket is connected.",
	})
	streamWSReconnects = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "helmsman",
		Name:      "stream_ws_reconnects_total",
		Help:      "MistController stream WebSocket reconnect attempts.",
	})
	streamWSNudges = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "helmsman",
		Name:      "stream_ws_nudges_total",
		Help:      "Relevant stream changes queued for a targeted Mist API refresh.",
	})
	streamWSRefreshes = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "helmsman",
		Name:      "stream_ws_refreshes_total",
		Help:      "Batched targeted active-stream refresh requests.",
	})
)

type streamWSChangeKey struct {
	status  int
	inputs  int
	outputs int
}

type streamWSObserved struct {
	key        streamWSChangeKey
	observedAt time.Time
}

type streamWSRefreshQueue struct {
	pm         *PrometheusMonitor
	generation uint64
	runtime    *mistNodeRuntime

	mu                   sync.Mutex
	pending              map[string]time.Time
	lastRefresh          map[string]time.Time
	fullSweep            bool
	fullSweepRequestedAt time.Time
	lastFullSweep        time.Time
	observed             map[string]streamWSObserved
	observedOverflow     bool
	wake                 chan struct{}
	bootstrapExpired     atomic.Bool
	bootstrapSynced      atomic.Bool
}

type streamRefreshResult struct {
	completed      bool
	needsFullSweep bool
	refreshed      []string
}

func (pm *PrometheusMonitor) stopStreamWS() {
	done := pm.detachStreamWS()
	if done != nil {
		<-done
	}
}

func (pm *PrometheusMonitor) detachStreamWS() <-chan struct{} {
	pm.streamWSMu.Lock()
	pm.streamWSGeneration++
	cancel := pm.streamWSCancel
	done := pm.streamWSDone
	pm.streamWSCancel = nil
	pm.streamWSDone = nil
	pm.streamWSQueue = nil
	pm.streamWSMu.Unlock()
	if cancel != nil {
		cancel()
	}
	streamWSConnections.Set(0)
	return done
}

func (pm *PrometheusMonitor) startStreamWS(runtime *mistNodeRuntime) {
	if runtime == nil || strings.TrimSpace(runtime.nodeID) == "" || strings.TrimSpace(runtime.baseURL) == "" {
		return
	}

	ctx, cancel := context.WithCancel(runtime.ctx)
	done := make(chan struct{})
	pm.streamWSMu.Lock()
	pm.streamWSGeneration++
	generation := pm.streamWSGeneration
	queue := &streamWSRefreshQueue{
		pm:          pm,
		generation:  generation,
		runtime:     runtime,
		pending:     make(map[string]time.Time),
		lastRefresh: make(map[string]time.Time),
		observed:    make(map[string]streamWSObserved),
		wake:        make(chan struct{}, 1),
	}
	pm.streamWSCancel = cancel
	pm.streamWSDone = done
	pm.streamWSQueue = queue
	pm.streamWSMu.Unlock()
	go func() {
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			queue.run(ctx)
		}()
		go func() {
			defer workers.Done()
			defer cancel()
			pm.runStreamWS(ctx, generation, runtime, queue)
		}()
		workers.Wait()
		close(done)
	}()
}

func (pm *PrometheusMonitor) streamWSRuntimeCurrent(generation uint64, runtime *mistNodeRuntime) bool {
	return pm.streamWSCurrent(generation) && pm.nodeRuntimeCurrent(runtime)
}

func (pm *PrometheusMonitor) streamWSCurrent(generation uint64) bool {
	pm.streamWSMu.Lock()
	defer pm.streamWSMu.Unlock()
	return pm.streamWSGeneration == generation && pm.streamWSCancel != nil
}

func (pm *PrometheusMonitor) setStreamWSConnected(generation uint64, connected bool) {
	if !pm.streamWSCurrent(generation) {
		return
	}
	if connected {
		streamWSConnections.Set(1)
	} else {
		streamWSConnections.Set(0)
	}
}

func (pm *PrometheusMonitor) runStreamWS(
	ctx context.Context,
	generation uint64,
	runtime *mistNodeRuntime,
	queue *streamWSRefreshQueue,
) {
	backoff := streamWSInitialBackoff
	firstAttempt := true
	for {
		if ctx.Err() != nil || !pm.streamWSRuntimeCurrent(generation, runtime) {
			return
		}
		if !firstAttempt {
			streamWSReconnects.Inc()
		}
		firstAttempt = false

		wsURL, err := mistControllerStreamWSURL(runtime.baseURL)
		if err != nil {
			monitorLogger.WithError(err).WithField("node_id", runtime.nodeID).Error("Invalid MistController WebSocket URL")
			return
		}
		conn, response, err := streamWSDialer.DialContext(ctx, wsURL, http.Header{})
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			monitorLogger.WithError(err).WithField("node_id", runtime.nodeID).Warn("MistController stream WebSocket dial failed")
			if !waitStreamWSBackoff(ctx, backoff) {
				return
			}
			backoff = nextStreamWSBackoff(backoff)
			continue
		}

		connectedAt := time.Now()
		stopCloser := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = conn.Close()
			case <-stopCloser:
			}
		}()
		err = pm.consumeStreamWS(ctx, generation, runtime, conn, queue)
		close(stopCloser)
		_ = conn.Close()
		pm.setStreamWSConnected(generation, false)
		if ctx.Err() != nil || !pm.streamWSRuntimeCurrent(generation, runtime) {
			return
		}
		if err != nil {
			monitorLogger.WithError(err).WithField("node_id", runtime.nodeID).Warn("MistController stream WebSocket closed")
		}
		if time.Since(connectedAt) >= streamWSHealthyAfter {
			backoff = streamWSInitialBackoff
		}
		if !waitStreamWSBackoff(ctx, backoff) {
			return
		}
		backoff = nextStreamWSBackoff(backoff)
	}
}

func mistControllerStreamWSURL(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported MistController URL scheme %q", u.Scheme)
	}
	u.Path = "/ws"
	u.RawPath = ""
	query := u.Query()
	query.Set("streams", "1")
	u.RawQuery = query.Encode()
	u.Fragment = ""
	return u.String(), nil
}

func (pm *PrometheusMonitor) consumeStreamWS(
	ctx context.Context,
	generation uint64,
	runtime *mistNodeRuntime,
	conn *websocket.Conn,
	queue *streamWSRefreshQueue,
) error {
	if streamWSPongWait > 0 {
		if err := conn.SetReadDeadline(time.Now().Add(streamWSPongWait)); err != nil {
			return fmt.Errorf("set auth read deadline: %w", err)
		}
	}
	if err := authenticateStreamWS(conn, pm.mistUsername, pm.mistAPIPassword); err != nil {
		return err
	}
	queue.beginConnectionBootstrap()
	stopLiveness, err := startStreamWSLiveness(ctx, conn)
	if err != nil {
		return err
	}
	defer stopLiveness()
	pm.setStreamWSConnected(generation, true)

	if streamWSWarmupWindow <= 0 {
		queue.bootstrapExpired.Store(true)
		queue.bootstrapSynced.Store(true)
	} else {
		bootstrapTimer := time.AfterFunc(streamWSWarmupWindow, func() {
			queue.bootstrapExpired.Store(true)
			queue.requestFullSweep()
		})
		defer bootstrapTimer.Stop()
	}
	for {
		if ctx.Err() != nil || !pm.streamWSRuntimeCurrent(generation, runtime) {
			return ctx.Err()
		}
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		streamName, key, ok := parseStreamWSFrame(payload)
		if !ok || isInternalMistRuntimeStream(streamName) {
			continue
		}
		_, changed, tracked := queue.observe(streamName, key)
		if !tracked {
			queue.requestFullSweep()
			continue
		}
		if !changed {
			continue
		}
		if !queue.bootstrapExpired.Load() || !queue.bootstrapSynced.Load() {
			continue
		}
		if !pm.streamWSRuntimeCurrent(generation, runtime) {
			return context.Canceled
		}
		if streamWSStatusRequiresFullSweep(key.status) {
			// Offline authority belongs to the authoritative poll's vanish diff.
			// An end frame must not amplify into an all-stream lifecycle replay.
			continue
		}
		queue.nudge(streamName)
	}
}

func (q *streamWSRefreshQueue) beginConnectionBootstrap() {
	q.bootstrapExpired.Store(false)
	q.bootstrapSynced.Store(false)
}

func startStreamWSLiveness(ctx context.Context, conn *websocket.Conn) (func(), error) {
	if streamWSPongWait <= 0 || streamWSPingInterval <= 0 {
		return func() {}, nil
	}
	refreshDeadline := func() error {
		return conn.SetReadDeadline(time.Now().Add(streamWSPongWait))
	}
	if err := refreshDeadline(); err != nil {
		return nil, fmt.Errorf("set stream WebSocket read deadline: %w", err)
	}
	conn.SetPongHandler(func(string) error { return refreshDeadline() })

	pingCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(streamWSPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-ticker.C:
				deadline := time.Now().Add(streamWSWriteWait)
				if err := conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}, nil
}

func authenticateStreamWS(conn *websocket.Conn, username, password string) error {
	for attempts := 0; attempts < 4; attempts++ {
		var frame []any
		if err := conn.ReadJSON(&frame); err != nil {
			return fmt.Errorf("read auth frame: %w", err)
		}
		if len(frame) < 2 || !strings.EqualFold(fmt.Sprint(frame[0]), "auth") {
			return fmt.Errorf("unexpected pre-auth frame")
		}
		switch auth := frame[1].(type) {
		case bool:
			if auth {
				return nil
			}
			if err := writeStreamWSAuth(conn, username, ""); err != nil {
				return err
			}
		case map[string]any:
			status, ok := auth["status"].(string)
			if !ok {
				return fmt.Errorf("MistController auth status is not a string")
			}
			switch status {
			case "OK":
				return nil
			case "CHALL":
				challenge, ok := auth["challenge"].(string)
				if !ok || challenge == "" {
					return fmt.Errorf("MistController auth challenge is empty")
				}
				if err := writeStreamWSAuth(conn, username, mist.ControllerPasswordHash(password, challenge)); err != nil {
					return err
				}
			default:
				return fmt.Errorf("MistController auth status %q", status)
			}
		default:
			return fmt.Errorf("unexpected MistController auth payload")
		}
	}
	return fmt.Errorf("MistController authentication did not complete")
}

func writeStreamWSAuth(conn *websocket.Conn, username, password string) error {
	payload := []any{"auth", map[string]string{"username": username, "password": password}}
	if err := conn.WriteJSON(payload); err != nil {
		return fmt.Errorf("write auth frame: %w", err)
	}
	return nil
}

func parseStreamWSFrame(payload []byte) (string, streamWSChangeKey, bool) {
	var frame []any
	if err := json.Unmarshal(payload, &frame); err != nil || len(frame) < 2 {
		return "", streamWSChangeKey{}, false
	}
	kind, kindOK := frame[0].(string)
	values, valuesOK := frame[1].([]any)
	if !kindOK || !valuesOK || kind != "stream" || len(values) < 5 {
		return "", streamWSChangeKey{}, false
	}
	name, ok := values[0].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return "", streamWSChangeKey{}, false
	}
	status, okStatus := streamWSInt(values[1])
	inputs, okInputs := streamWSInt(values[3])
	outputs, okOutputs := streamWSInt(values[4])
	if !okStatus || !okInputs || !okOutputs {
		return "", streamWSChangeKey{}, false
	}
	return name, streamWSChangeKey{status: status, inputs: inputs, outputs: outputs}, true
}

func streamWSInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), typed == float64(int(typed))
	case int:
		return typed, true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func streamWSStatusRequiresFullSweep(status int) bool {
	return status == 0 || status == 5 || status == 6
}

func waitStreamWSBackoff(ctx context.Context, backoff time.Duration) bool {
	jitter := 0.8 + rand.Float64()*0.4
	timer := time.NewTimer(time.Duration(float64(backoff) * jitter))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextStreamWSBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > streamWSMaxBackoff {
		return streamWSMaxBackoff
	}
	return next
}

func (q *streamWSRefreshQueue) nudge(streamName string) {
	if !q.pm.streamWSRuntimeCurrent(q.generation, q.runtime) || isInternalMistRuntimeStream(streamName) {
		return
	}
	streamWSNudges.Inc()
	queuedAt := time.Now()
	q.mu.Lock()
	if _, pending := q.pending[streamName]; !pending {
		if _, throttled := q.lastRefresh[streamName]; !throttled && len(q.pending)+len(q.lastRefresh) >= maxStreamWSStateEntries {
			clear(q.pending)
			clear(q.lastRefresh)
			q.fullSweep = true
			q.fullSweepRequestedAt = queuedAt
		} else {
			q.pending[streamName] = queuedAt
		}
	} else {
		q.pending[streamName] = queuedAt
	}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *streamWSRefreshQueue) requestFullSweep() {
	if !q.pm.streamWSRuntimeCurrent(q.generation, q.runtime) {
		return
	}
	q.mu.Lock()
	q.fullSweep = true
	q.fullSweepRequestedAt = time.Now()
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *streamWSRefreshQueue) run(ctx context.Context) {
	fullSweepBackoff := streamWSInitialBackoff
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
			if streamWSBatchDelay > 0 {
				timer := time.NewTimer(streamWSBatchDelay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}

		for {
			fullSweep, fullSweepWait := q.takeFullSweep(time.Now())
			if fullSweep {
				sweepObservedAt := time.Now()
				result := streamLifecyclePollFailed
				usedTestOverride := q.pm.streamWSFullSweep != nil
				if q.pm.streamWSFullSweep != nil {
					if q.pm.streamWSFullSweep(q.runtime.nodeID, q.runtime.baseURL) {
						result = streamLifecyclePollSucceeded
					}
				} else {
					result = q.pm.emitStreamLifecycleWithClient(
						ctx,
						q.runtime.nodeID,
						q.runtime.baseURL,
						q.runtime.acceleratorClient,
						&q.runtime.acceleratorMu,
						func() bool { return q.pm.streamWSRuntimeCurrent(q.generation, q.runtime) },
					)
				}
				if result == streamLifecyclePollContended && ctx.Err() == nil && q.pm.streamWSRuntimeCurrent(q.generation, q.runtime) {
					q.requestFullSweep()
					timer := time.NewTimer(streamWSSweepBusyDelay)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				} else if result != streamLifecyclePollSucceeded && ctx.Err() == nil && q.pm.streamWSRuntimeCurrent(q.generation, q.runtime) {
					q.requestFullSweep()
					if !waitStreamWSBackoff(ctx, fullSweepBackoff) {
						return
					}
					fullSweepBackoff = nextStreamWSBackoff(fullSweepBackoff)
				} else if result == streamLifecyclePollSucceeded {
					fullSweepBackoff = streamWSInitialBackoff
					// Production polling records the authoritative inventory with its
					// own request-start timestamp. Test overrides have no poller, so
					// complete their equivalent bookkeeping here.
					if usedTestOverride {
						q.recordAuthoritativeInventory(nil, sweepObservedAt, time.Now())
					}
				}
				continue
			}
			if fullSweepWait > 0 {
				timer := time.NewTimer(fullSweepWait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-q.wake:
					if !timer.Stop() {
						<-timer.C
					}
				case <-timer.C:
				}
				continue
			}
			names, wait := q.takeReady(time.Now())
			if len(names) > 0 {
				result := q.pm.refreshStreams(ctx, q.generation, q.runtime, names)
				if result.needsFullSweep {
					q.requestFullSweep()
				}
				if result.completed {
					q.markAttempted(names, time.Now())
				}
				continue
			}
			if wait <= 0 {
				break
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-q.wake:
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
			}
		}
	}
}

func (q *streamWSRefreshQueue) takeFullSweep(now time.Time) (bool, time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.fullSweep {
		return false, 0
	}
	due := q.lastFullSweep.Add(streamWSFullSweepDelay)
	if !q.lastFullSweep.IsZero() && streamWSFullSweepDelay > 0 && now.Before(due) {
		return false, time.Until(due)
	}
	q.fullSweep = false
	return true, 0
}

func (q *streamWSRefreshQueue) recordAuthoritativeInventory(
	present map[string]struct{},
	observedAt time.Time,
	completedAt time.Time,
) {
	q.mu.Lock()
	if present != nil {
		for name, observed := range q.observed {
			if _, exists := present[name]; !exists && !observed.observedAt.After(observedAt) {
				delete(q.observed, name)
			}
		}
		for name, queuedAt := range q.pending {
			if _, exists := present[name]; !exists && !queuedAt.After(observedAt) {
				delete(q.pending, name)
			}
		}
		for name, attemptedAt := range q.lastRefresh {
			if _, exists := present[name]; !exists && !attemptedAt.After(observedAt) {
				delete(q.lastRefresh, name)
			}
		}
		// Above the fixed dedup bound, stream frames are advisory and the
		// authoritative poll remains the only state producer. Re-enable targeted
		// refreshes automatically after a later bounded inventory.
		q.observedOverflow = len(present) > maxStreamWSStateEntries
	}
	if q.fullSweep && !q.fullSweepRequestedAt.After(observedAt) {
		q.fullSweep = false
	}
	q.lastFullSweep = completedAt
	q.mu.Unlock()
	q.bootstrapSynced.Store(true)
}

func (pm *PrometheusMonitor) recordAuthoritativeStreamInventory(
	nodeID string,
	baseURL string,
	present map[string]struct{},
	observedAt time.Time,
) {
	pm.streamWSMu.Lock()
	queue := pm.streamWSQueue
	if queue == nil || queue.runtime.nodeID != nodeID || queue.runtime.baseURL != baseURL {
		pm.streamWSMu.Unlock()
		return
	}
	pm.streamWSMu.Unlock()
	queue.recordAuthoritativeInventory(present, observedAt, time.Now())
}

func (q *streamWSRefreshQueue) observe(streamName string, key streamWSChangeKey) (existed, changed, tracked bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	observedAt := time.Now()
	previous, existed := q.observed[streamName]
	if q.observedOverflow {
		return existed, false, true
	}
	if existed {
		changed := previous.key != key
		q.observed[streamName] = streamWSObserved{key: key, observedAt: observedAt}
		return true, changed, true
	}
	if len(q.observed) >= maxStreamWSStateEntries {
		q.observedOverflow = true
		return false, false, false
	}
	q.observed[streamName] = streamWSObserved{key: key, observedAt: observedAt}
	return false, true, true
}

func (q *streamWSRefreshQueue) takeReady(now time.Time) ([]string, time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var ready []string
	var earliest time.Time
	for name := range q.pending {
		due := q.lastRefresh[name].Add(streamWSMinRefresh)
		if q.lastRefresh[name].IsZero() || !now.Before(due) {
			ready = append(ready, name)
			continue
		}
		if earliest.IsZero() || due.Before(earliest) {
			earliest = due
		}
	}
	sort.Strings(ready)
	names := make([]string, 0, min(len(ready), maxStreamWSRefreshBatchNames))
	encodedBytes := 0
	for _, name := range ready {
		cost := len(url.QueryEscape(name)) + 6
		if len(names) >= maxStreamWSRefreshBatchNames ||
			(len(names) > 0 && encodedBytes+cost > maxStreamWSRefreshBatchBytes) {
			break
		}
		names = append(names, name)
		encodedBytes += cost
		delete(q.pending, name)
	}
	if len(names) > 0 || earliest.IsZero() {
		return names, 0
	}
	return nil, time.Until(earliest)
}

func (q *streamWSRefreshQueue) markAttempted(names []string, refreshedAt time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, name := range names {
		q.lastRefresh[name] = refreshedAt
	}
}

func (pm *PrometheusMonitor) refreshStreams(
	ctx context.Context,
	generation uint64,
	runtime *mistNodeRuntime,
	names []string,
) streamRefreshResult {
	if !pm.streamWSRuntimeCurrent(generation, runtime) {
		return streamRefreshResult{}
	}
	unique := make(map[string]struct{}, len(names))
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) == "" || isInternalMistRuntimeStream(name) {
			continue
		}
		if _, exists := unique[name]; exists {
			continue
		}
		unique[name] = struct{}{}
		filtered = append(filtered, name)
	}
	if len(filtered) == 0 {
		return streamRefreshResult{completed: true}
	}
	sort.Strings(filtered)
	observation := pm.beginStreamObservation()
	streamWSRefreshes.Inc()
	if !pm.streamWSRuntimeCurrent(generation, runtime) || runtime.acceleratorClient == nil {
		return streamRefreshResult{}
	}
	runtime.acceleratorMu.Lock()
	response, err := runtime.acceleratorClient.GetActiveStreamsFilteredContext(ctx, filtered)
	runtime.acceleratorMu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return streamRefreshResult{}
		}
		monitorLogger.WithError(err).WithFields(logging.Fields{
			"node_id": runtime.nodeID,
			"streams": filtered,
		}).Warn("Targeted active-stream refresh failed")
		return streamRefreshResult{needsFullSweep: true}
	}
	if !pm.streamWSRuntimeCurrent(generation, runtime) {
		return streamRefreshResult{}
	}

	active, authoritative := activeStreamsFromResponse(response)
	if !authoritative {
		// The queue owns full sweeps so node replacement and shutdown join them.
		return streamRefreshResult{needsFullSweep: true}
	}
	requiresFullSweep := false
	type refreshableStream struct {
		name string
		data map[string]any
	}
	refreshable := make([]refreshableStream, 0, len(filtered))
	for _, name := range filtered {
		raw, exists := active[name]
		if !exists {
			// The stream may end between its WS nudge and this filtered fetch.
			// The periodic authoritative inventory owns the offline edge; normal
			// stream churn must not amplify into an immediate all-stream replay.
			continue
		}
		streamData, ok := raw.(map[string]any)
		if !ok {
			requiresFullSweep = true
			continue
		}
		status, statusOK := streamAPIStatus(streamData["status"])
		if !statusOK {
			requiresFullSweep = true
			continue
		}
		if streamWSStatusRequiresFullSweep(status) {
			continue
		}
		refreshable = append(refreshable, refreshableStream{name: name, data: streamData})
	}
	if len(refreshable) == 0 {
		return streamRefreshResult{completed: true, needsFullSweep: requiresFullSweep}
	}
	refreshableNames := make([]string, 0, len(refreshable))
	for _, stream := range refreshable {
		refreshableNames = append(refreshableNames, stream.name)
	}
	// Register only successful, refreshable rows, but retain the observation's
	// request-order position. A response that began before a newer authoritative
	// poll must not revive a stream that poll already removed.
	if !pm.issueTargetedStreamObservation(observation, refreshableNames) {
		return streamRefreshResult{needsFullSweep: true}
	}
	refreshed := make([]string, 0, len(refreshable))
	for _, stream := range refreshable {
		if !pm.streamWSRuntimeCurrent(generation, runtime) {
			continue
		}
		applied := pm.applyStreamObservation(stream.name, observation, func() {
			if pm.streamWSRuntimeCurrent(generation, runtime) {
				pm.recordTargetedStreamPresence(stream.name, observation)
				pm.processActiveStreamDataContext(ctx, runtime.nodeID, stream.name, stream.data)
			}
		})
		if applied {
			refreshed = append(refreshed, stream.name)
		}
	}
	return streamRefreshResult{completed: true, needsFullSweep: requiresFullSweep, refreshed: refreshed}
}

func streamAPIStatus(value any) (int, bool) {
	if status, ok := streamWSInt(value); ok {
		return status, true
	}
	status, ok := value.(string)
	if !ok {
		return 0, false
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "offline":
		return 0, true
	case "initializing":
		return 1, true
	case "input booting":
		return 2, true
	case "waiting for data":
		return 3, true
	case "online":
		return 4, true
	case "shutting down":
		return 5, true
	default:
		return 0, false
	}
}
