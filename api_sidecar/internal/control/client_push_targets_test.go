package control

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
)

var restreamActivationTestConnectionMu sync.Mutex

func activatePushTargetsForTest(t *testing.T, logger logging.Logger, req *ipcpb.ActivatePushTargets, respond func(*ipcpb.ControlMessage)) {
	t.Helper()
	restreamActivationTestConnectionMu.Lock()
	connection := getConnection()
	if connection == nil || !strings.HasPrefix(connection.epoch, "restream-test:") {
		previous := connection
		connection = &streamConn{epoch: "restream-test:" + t.Name()}
		activeConn.Store(connection)
		t.Cleanup(func() {
			if getConnection() == connection {
				activeConn.Store(previous)
			}
		})
	}
	restreamActivationTestConnectionMu.Unlock()
	handleActivatePushTargets(logger, req, connection.epoch, restreamActivationDispatchSequence.Add(1), respond)
}

func TestHandleActivatePushTargets(t *testing.T) {
	t.Run("nil request is a no-op", func(t *testing.T) {
		activatePushTargetsForTest(t, logging.NewLogger(), nil, nil)
	})

	t.Run("empty stream name returns a bounded failure", func(t *testing.T) {
		var result *ipcpb.ActivatePushTargetsResult
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{ActivationAttempt: "attempt-empty-stream"}, func(message *ipcpb.ControlMessage) {
			result = message.GetActivatePushTargetsResult()
		})
		if result == nil || result.GetConverged() || result.GetError() == "" {
			t.Fatalf("empty stream name did not produce a correlated failure: %+v", result)
		}
		if result.GetActivationAttempt() != "attempt-empty-stream" {
			t.Fatalf("failure dropped activation attempt: %+v", result)
		}
	})

	t.Run("no targets is a no-op", func(t *testing.T) {
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{StreamName: "live+a"}, nil)
	})

	t.Run("missing config is a no-op", func(t *testing.T) {
		withConfig(t, nil)
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName:        "live+a",
			SourceGeneration:  "gen-a",
			ActivationAttempt: "attempt-success",
			Targets:           []*ipcpb.PushTargetSpec{{TargetId: "t1", TargetUri: "rtmp://1.1.1.1/app"}},
		}, nil)
	})

	t.Run("starts a push per target and registers the stream", func(t *testing.T) {
		// Stateful mock: push_list reflects pushes started so far, so the handler's post-start
		// confirmation (process creation, not just command parsing) can pass.
		var pmu sync.Mutex
		var startedPushes [][]any
		var startCalls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cmd := r.URL.Query().Get("command")
			var parsed map[string]any
			if cmd != "" {
				_ = json.Unmarshal([]byte(cmd), &parsed)
			} else {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &parsed)
			}
			w.Header().Set("Content-Type", "application/json")
			if _, ok := parsed["authorize"]; ok {
				_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
				return
			}
			if ps, ok := parsed["push_start"].(map[string]any); ok {
				pmu.Lock()
				startCalls++
				startedPushes = append(startedPushes, []any{float64(startCalls), ps["stream"], ps["target"], ps["target"]})
				pmu.Unlock()
				_, _ = w.Write([]byte(`{}`))
				return
			}
			if _, ok := parsed["push_list"]; ok {
				pmu.Lock()
				resp, _ := json.Marshal(map[string]any{"push_list": startedPushes})
				pmu.Unlock()
				_, _ = w.Write(resp)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		}))
		t.Cleanup(srv.Close)
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: srv.URL})
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)

		var acks []*ipcpb.ActivatePushTargetsResult
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName:        "live+a",
			SourceGeneration:  "gen-a",
			ActivationAttempt: "attempt-success",
			Targets: []*ipcpb.PushTargetSpec{
				{TargetId: "t1", Name: "yt", TargetUri: "rtmp://1.1.1.1/app"},
				{TargetId: "t2", Name: "tw", TargetUri: "rtmp://8.8.8.8/app"},
			},
		}, func(m *ipcpb.ControlMessage) { acks = append(acks, m.GetActivatePushTargetsResult()) })

		pmu.Lock()
		gotStarts := startCalls
		pmu.Unlock()
		if gotStarts != 2 {
			t.Fatalf("expected 2 push_start calls, got %d", gotStarts)
		}
		if len(acks) != 1 || !acks[0].GetConverged() {
			t.Fatalf("a fully started activation must acknowledge converged=true, got %+v", acks)
		}
		if acks[0].GetActivationAttempt() != "attempt-success" {
			t.Fatalf("successful acknowledgement dropped activation attempt: %+v", acks[0])
		}
	})

	t.Run("a failing target is logged and does not abort the rest", func(t *testing.T) {
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: errMistServer(t)})
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)
		// The Mist API is down (500s): the push list cannot be read, so the
		// handler fails closed — no blind PushStart — and acknowledges
		// converged=false so the durable obligation retries. The stream is
		// still registered so a later deactivate can reconcile.
		var acks []*ipcpb.ActivatePushTargetsResult
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName:       "live+a",
			SourceGeneration: "gen-a",
			Targets: []*ipcpb.PushTargetSpec{
				{TargetId: "t1", TargetUri: "rtmp://1.1.1.1/app"},
				{TargetId: "t2", TargetUri: "rtmp://8.8.8.8/app"},
			},
		}, func(m *ipcpb.ControlMessage) { acks = append(acks, m.GetActivatePushTargetsResult()) })
		if len(acks) != 1 || acks[0].GetConverged() {
			t.Fatalf("a failed activation must acknowledge converged=false for the obligation retry, got %+v", acks)
		}
		if len(acks[0].GetTargets()) != 2 {
			t.Fatalf("inventory failure outcomes=%d, want one retryable outcome per requested target", len(acks[0].GetTargets()))
		}
		for _, outcome := range acks[0].GetTargets() {
			if outcome.GetActive() || outcome.GetReason() != ipcpb.RestreamReason_RESTREAM_REASON_PROCESS_ERROR {
				t.Fatalf("inventory failure outcome=%+v, want retryable process error", outcome)
			}
		}
	})

	t.Run("inventory failure cannot settle healthy sibling of rejected target", func(t *testing.T) {
		t.Setenv("RESTREAM_ALLOW_PRIVATE_DESTINATIONS", "false")
		t.Setenv("RESTREAM_ALLOWED_PRIVATE_CIDRS", "")
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: errMistServer(t)})
		RecordAdmittedIngestGeneration("live+inventory-mixed", "gen-inventory-mixed", 201)

		var result *ipcpb.ActivatePushTargetsResult
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName: "live+inventory-mixed", SourceGeneration: "gen-inventory-mixed", TargetRevision: 4,
			Targets: []*ipcpb.PushTargetSpec{
				{TargetId: "rejected", TargetUri: "rtmp://127.0.0.1/live/key"},
				{TargetId: "healthy", TargetUri: "rtmp://8.8.8.8/live/key"},
			},
		}, func(message *ipcpb.ControlMessage) { result = message.GetActivatePushTargetsResult() })

		if result == nil || result.GetConverged() || len(result.GetTargets()) != 2 {
			t.Fatalf("mixed inventory failure did not report the complete target set: %+v", result)
		}
		byID := make(map[string]*ipcpb.PushTargetConvergence)
		for _, outcome := range result.GetTargets() {
			byID[outcome.GetTargetId()] = outcome
		}
		if got := byID["rejected"]; got == nil || got.GetReason() != ipcpb.RestreamReason_RESTREAM_REASON_DESTINATION_REJECTED {
			t.Fatalf("rejected target outcome=%+v", got)
		}
		if got := byID["healthy"]; got == nil || got.GetReason() != ipcpb.RestreamReason_RESTREAM_REASON_PROCESS_ERROR {
			t.Fatalf("healthy target outcome=%+v, want retryable inventory failure", got)
		}
	})

	t.Run("invalid node policy stays retryable", func(t *testing.T) {
		t.Setenv("RESTREAM_DENIED_CIDRS", "not-a-cidr")
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: errMistServer(t)})
		RecordAdmittedIngestGeneration("live+bad-policy", "gen-bad-policy", 201)
		var result *ipcpb.ActivatePushTargetsResult
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName: "live+bad-policy", SourceGeneration: "gen-bad-policy", TargetRevision: 5,
			Targets: []*ipcpb.PushTargetSpec{{TargetId: "target", TargetUri: "rtmp://8.8.8.8/live/key"}},
		}, func(message *ipcpb.ControlMessage) { result = message.GetActivatePushTargetsResult() })
		if result == nil || len(result.GetTargets()) != 1 || result.GetTargets()[0].GetReason() != ipcpb.RestreamReason_RESTREAM_REASON_PROCESS_ERROR {
			t.Fatalf("invalid node policy was not reported as retryable: %+v", result)
		}
	})

	t.Run("private destination returns a bounded terminal outcome", func(t *testing.T) {
		t.Setenv("RESTREAM_ALLOW_PRIVATE_DESTINATIONS", "false")
		t.Setenv("RESTREAM_ALLOWED_PRIVATE_CIDRS", "")
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: "http://127.0.0.1:65535"})
		RecordAdmittedIngestGeneration("live+private-target", "gen-private", 202)

		var result *ipcpb.ActivatePushTargetsResult
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName: "live+private-target", SourceGeneration: "gen-private",
			TenantId: "tenant-a", StreamId: "stream-a", TargetRevision: 4,
			Targets: []*ipcpb.PushTargetSpec{{TargetId: "target-private", TargetUri: "rtmp://127.0.0.1/live/key"}},
		}, func(message *ipcpb.ControlMessage) { result = message.GetActivatePushTargetsResult() })

		if result == nil || result.GetConverged() || len(result.GetTargets()) != 1 {
			t.Fatalf("expected one failed convergence outcome, got %+v", result)
		}
		if got := result.GetTargets()[0].GetReason(); got != ipcpb.RestreamReason_RESTREAM_REASON_DESTINATION_REJECTED {
			t.Fatalf("reason=%s, want destination rejected", got)
		}
	})

	t.Run("one DNS failure preserves the complete installed set", func(t *testing.T) {
		url, calls := pushListMistServer(t, nil)
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})
		RecordAdmittedIngestGeneration("live+dns-sibling", "gen-dns", 203)
		previousFactory := restreamDestinationPolicyFromEnvironment
		restreamDestinationPolicyFromEnvironment = func() (restream.DestinationPolicy, error) {
			return restream.DestinationPolicy{LookupIP: func(_ context.Context, host string) ([]net.IP, error) {
				if host == "unavailable.example" {
					return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
				}
				return []net.IP{net.ParseIP("8.8.8.8")}, nil
			}}, nil
		}
		t.Cleanup(func() { restreamDestinationPolicyFromEnvironment = previousFactory })

		var result *ipcpb.ActivatePushTargetsResult
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName: "live+dns-sibling", SourceGeneration: "gen-dns", TargetRevision: 7,
			Targets: []*ipcpb.PushTargetSpec{
				{TargetId: "dns-down", TargetUri: "rtmp://unavailable.example/live/key"},
				{TargetId: "healthy", TargetUri: "rtmp://healthy.example/live/key"},
			},
		}, func(message *ipcpb.ControlMessage) { result = message.GetActivatePushTargetsResult() })

		if calls("push_list") != 2 || calls("push_start") != 1 || calls("push_stop") != 0 {
			t.Fatalf("partial DNS validation did not converge healthy siblings: list=%d start=%d stop=%d", calls("push_list"), calls("push_start"), calls("push_stop"))
		}
		if result == nil || result.GetConverged() || len(result.GetTargets()) != 2 {
			t.Fatalf("expected one retryable DNS outcome plus one healthy outcome, got %+v", result)
		}
		byID := make(map[string]*ipcpb.PushTargetConvergence)
		for _, outcome := range result.GetTargets() {
			byID[outcome.GetTargetId()] = outcome
		}
		if got := byID["dns-down"]; got == nil || got.GetReason() != ipcpb.RestreamReason_RESTREAM_REASON_NETWORK_ERROR {
			t.Fatalf("DNS failure was not retryable: %+v", got)
		}
		if got := byID["healthy"]; got == nil || !got.GetActive() || got.GetReason() != ipcpb.RestreamReason_RESTREAM_REASON_CONNECTED {
			t.Fatalf("healthy sibling did not converge independently: %+v", got)
		}
	})

	t.Run("post-reconcile inventory failure reports every target", func(t *testing.T) {
		const streamName = "live+confirm-failure"
		var mu sync.Mutex
		pushLists := 0
		pushes := make([][]any, 0, 2)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var command map[string]any
			if raw := r.URL.Query().Get("command"); raw != "" {
				_ = json.Unmarshal([]byte(raw), &command)
			} else {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &command)
			}
			w.Header().Set("Content-Type", "application/json")
			if _, ok := command["authorize"]; ok {
				_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
				return
			}
			if start, ok := command["push_start"].(map[string]any); ok {
				mu.Lock()
				pushes = append(pushes, []any{float64(len(pushes) + 1), start["stream"], start["target"], start["target"]})
				mu.Unlock()
				_, _ = w.Write([]byte(`{}`))
				return
			}
			if _, ok := command["push_list"]; ok {
				mu.Lock()
				pushLists++
				call := pushLists
				current := append([][]any(nil), pushes...)
				mu.Unlock()
				if call == 2 {
					http.Error(w, "inventory unavailable", http.StatusServiceUnavailable)
					return
				}
				response, _ := json.Marshal(map[string]any{"push_list": current})
				_, _ = w.Write(response)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		}))
		t.Cleanup(srv.Close)
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: srv.URL})
		RecordAdmittedIngestGeneration(streamName, "gen-confirm", 204)

		var result *ipcpb.ActivatePushTargetsResult
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName: streamName, SourceGeneration: "gen-confirm", TargetRevision: 9,
			Targets: []*ipcpb.PushTargetSpec{
				{TargetId: "target-a", TargetUri: "rtmp://1.1.1.1/live/a"},
				{TargetId: "target-b", TargetUri: "rtmp://8.8.8.8/live/b"},
			},
		}, func(message *ipcpb.ControlMessage) { result = message.GetActivatePushTargetsResult() })

		if result == nil || result.GetConverged() || len(result.GetTargets()) != 2 {
			t.Fatalf("confirm failure did not report the complete target set: %+v", result)
		}
		for _, outcome := range result.GetTargets() {
			if outcome.GetActive() || outcome.GetReason() != ipcpb.RestreamReason_RESTREAM_REASON_PROCESS_ERROR {
				t.Fatalf("confirm failure outcome is not retryable: %+v", outcome)
			}
		}
	})

	t.Run("duplicate destination URI is terminal without wedging its healthy sibling", func(t *testing.T) {
		const streamName = "live+duplicate-uri"
		const targetURI = "rtmp://8.8.8.8/live/key"
		url, calls := pushListMistServer(t, [][]any{{float64(41), streamName, targetURI, targetURI}})
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})
		RecordAdmittedIngestGeneration(streamName, "gen-duplicate", 205)

		var result *ipcpb.ActivatePushTargetsResult
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName: streamName, SourceGeneration: "gen-duplicate", TargetRevision: 8,
			Targets: []*ipcpb.PushTargetSpec{
				{TargetId: "canonical", TargetUri: targetURI},
				{TargetId: "duplicate", TargetUri: targetURI},
			},
		}, func(message *ipcpb.ControlMessage) { result = message.GetActivatePushTargetsResult() })

		if calls("push_start") != 0 || calls("push_stop") != 0 {
			t.Fatalf("duplicate reconcile churned the healthy push: start=%d stop=%d", calls("push_start"), calls("push_stop"))
		}
		if result == nil || result.GetConverged() || len(result.GetTargets()) != 2 {
			t.Fatalf("duplicate target did not produce bounded partial convergence: %+v", result)
		}
		byID := make(map[string]*ipcpb.PushTargetConvergence)
		for _, outcome := range result.GetTargets() {
			byID[outcome.GetTargetId()] = outcome
		}
		if got := byID["canonical"]; got == nil || !got.GetActive() {
			t.Fatalf("canonical target was not retained: %+v", got)
		}
		if got := byID["duplicate"]; got == nil || got.GetReason() != ipcpb.RestreamReason_RESTREAM_REASON_CONFIGURATION_ERROR {
			t.Fatalf("duplicate target was not terminally classified: %+v", got)
		}
	})

	t.Run("duplicate target identity is terminal without retry livelock", func(t *testing.T) {
		const streamName = "live+duplicate-id"
		const canonicalURI = "rtmp://8.8.8.8/live/key"
		url, calls := pushListMistServer(t, [][]any{{float64(42), streamName, canonicalURI, canonicalURI}})
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})
		RecordAdmittedIngestGeneration(streamName, "gen-duplicate-id", 206)

		var result *ipcpb.ActivatePushTargetsResult
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName: streamName, SourceGeneration: "gen-duplicate-id", TargetRevision: 9,
			Targets: []*ipcpb.PushTargetSpec{
				{TargetId: "same-id", TargetUri: canonicalURI},
				{TargetId: "same-id", TargetUri: "rtmp://1.1.1.1/live/other"},
			},
		}, func(message *ipcpb.ControlMessage) { result = message.GetActivatePushTargetsResult() })

		if calls("push_start") != 0 || calls("push_stop") != 0 {
			t.Fatalf("duplicate identity churned the healthy push: start=%d stop=%d", calls("push_start"), calls("push_stop"))
		}
		if result == nil || result.GetConverged() || len(result.GetTargets()) != 2 {
			t.Fatalf("duplicate identity did not produce bounded partial convergence: %+v", result)
		}
		var active, rejected int
		for _, outcome := range result.GetTargets() {
			if outcome.GetActive() {
				active++
			}
			if outcome.GetReason() == ipcpb.RestreamReason_RESTREAM_REASON_CONFIGURATION_ERROR {
				rejected++
			}
		}
		if active != 1 || rejected != 1 {
			t.Fatalf("duplicate identity outcomes = %+v, want one active and one configuration error", result.GetTargets())
		}
	})

	t.Run("DNS resolution does not block a publisher generation replacement", func(t *testing.T) {
		url, calls := pushListMistServer(t, nil)
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})
		const streamName = "live+dns-generation-fence"
		if err := RecordAdmittedIngestGeneration(streamName, "gen-old", 204); err != nil {
			t.Fatal(err)
		}
		resolverEntered := make(chan struct{})
		releaseResolver := make(chan struct{})
		previousFactory := restreamDestinationPolicyFromEnvironment
		restreamDestinationPolicyFromEnvironment = func() (restream.DestinationPolicy, error) {
			return restream.DestinationPolicy{LookupIP: func(context.Context, string) ([]net.IP, error) {
				close(resolverEntered)
				<-releaseResolver
				return []net.IP{net.ParseIP("8.8.8.8")}, nil
			}}, nil
		}
		t.Cleanup(func() { restreamDestinationPolicyFromEnvironment = previousFactory })

		resultCh := make(chan *ipcpb.ActivatePushTargetsResult, 1)
		go activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName: streamName, SourceGeneration: "gen-old", TargetRevision: 1,
			Targets: []*ipcpb.PushTargetSpec{{TargetId: "target-a", TargetUri: "rtmp://slow.example/live/key"}},
		}, func(message *ipcpb.ControlMessage) { resultCh <- message.GetActivatePushTargetsResult() })
		<-resolverEntered

		replaced := make(chan error, 1)
		go func() { replaced <- RecordAdmittedIngestGeneration(streamName, "gen-new", 205) }()
		select {
		case err := <-replaced:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("publisher generation replacement blocked behind destination DNS resolution")
		}
		close(releaseResolver)
		select {
		case result := <-resultCh:
			if result == nil || result.GetConverged() {
				t.Fatalf("superseded activation unexpectedly converged: %+v", result)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("superseded activation did not return")
		}
		if calls("push_list") != 0 || calls("push_start") != 0 {
			t.Fatalf("superseded activation reached Mist: push_list=%d push_start=%d", calls("push_list"), calls("push_start"))
		}
	})
}

func TestDispatchActivatePushTargetsPropagatesRetiredControlEpoch(t *testing.T) {
	const streamName = "live+dispatch-control-epoch"
	previousConnection := getConnection()
	currentConnection := &streamConn{epoch: "control-current"}
	retiredConnection := &streamConn{epoch: "control-retired"}
	activeConn.Store(currentConnection)
	t.Cleanup(func() { activeConn.Store(previousConnection) })
	restreamRegistry.Lock()
	delete(restreamRegistry.streams, streamName)
	restreamRegistry.Unlock()
	t.Cleanup(func() {
		restreamRegistry.Lock()
		delete(restreamRegistry.streams, streamName)
		restreamRegistry.Unlock()
	})

	url, calls := pushListMistServer(t, nil)
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})
	if err := RecordAdmittedIngestGeneration(streamName, "generation-a", 901); err != nil {
		t.Fatal(err)
	}
	request := func(attempt, uri string) *ipcpb.ActivatePushTargets {
		return &ipcpb.ActivatePushTargets{
			StreamName: streamName, SourceGeneration: "generation-a", TargetRevision: 7,
			ActivationAttempt: attempt,
			Targets:           []*ipcpb.PushTargetSpec{{TargetId: "target-a", TargetUri: uri}},
		}
	}
	if _, result := installRestreamDesiredStateForControlEpoch(request("attempt-current", "rtmp://8.8.8.8/current"), currentConnection.epoch, 1); result != restreamInstallApplied {
		t.Fatalf("seed current desired state: %v", result)
	}

	resultCh := make(chan *ipcpb.ActivatePushTargetsResult, 1)
	dispatchActivatePushTargets(logging.NewLogger(), request("attempt-retired", "rtmp://1.1.1.1/retired"), retiredConnection, func(message *ipcpb.ControlMessage) {
		resultCh <- message.GetActivatePushTargetsResult()
	})
	select {
	case result := <-resultCh:
		if result == nil || !result.GetConverged() {
			t.Fatalf("retired dispatch result = %+v, want superseded convergence", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retired dispatch did not complete")
	}
	restreamRegistry.RLock()
	current := restreamRegistry.streams[streamName]
	restreamRegistry.RUnlock()
	if target := current.byID["target-a"]; target.activationAttempt != "attempt-current" || target.targetURI != "rtmp://8.8.8.8/current" {
		t.Fatalf("retired receive-loop dispatch replaced current state: %+v", target)
	}
	if got := calls("push_list") + calls("push_start"); got != 0 {
		t.Fatalf("retired receive-loop dispatch reached Mist %d times", got)
	}
}

// pushListMistServer answers auth + push_list (with the given entries) and
// records every command, so the deactivation path can be driven and asserted.
// Each push entry is [id, stream, target, actual].
func pushListMistServer(t *testing.T, entries [][]any) (url string, calls func(string) int) {
	t.Helper()
	var mu sync.Mutex
	var requests []map[string]any
	pushes := append([][]any(nil), entries...)
	nextPushID := int64(1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := r.URL.Query().Get("command")
		var parsed map[string]any
		if cmd != "" {
			_ = json.Unmarshal([]byte(cmd), &parsed)
		} else {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &parsed)
		}
		mu.Lock()
		requests = append(requests, parsed)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if _, ok := parsed["authorize"]; ok {
			_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
			return
		}
		if _, ok := parsed["push_list"]; ok {
			mu.Lock()
			current := append([][]any(nil), pushes...)
			mu.Unlock()
			resp, _ := json.Marshal(map[string]any{"push_list": current})
			_, _ = w.Write(resp)
			return
		}
		if raw, ok := parsed["push_start"].(map[string]any); ok {
			mu.Lock()
			nextPushID++
			pushes = append(pushes, []any{float64(nextPushID), raw["stream"], raw["target"], raw["target"]})
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func(key string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, req := range requests {
			if _, ok := req[key]; ok {
				n++
			}
		}
		return n
	}
}

func TestHandleDeactivatePushTargets(t *testing.T) {
	t.Run("nil request is a no-op", func(t *testing.T) {
		handleDeactivatePushTargets(logging.NewLogger(), nil)
	})

	t.Run("empty stream name is a no-op", func(t *testing.T) {
		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{})
	})

	t.Run("missing config is a no-op", func(t *testing.T) {
		withConfig(t, nil)
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)
		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{StreamName: "live+a", SourceGeneration: "gen-a"})
	})

	t.Run("push_list error is handled", func(t *testing.T) {
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: errMistServer(t)})
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)
		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{StreamName: "live+a", SourceGeneration: "gen-a"})
	})

	t.Run("no matching pushes is a no-op", func(t *testing.T) {
		mock := newMockMistServer(t) // returns no push_list
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL})
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)
		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{StreamName: "live+a", SourceGeneration: "gen-a"})
	})

	t.Run("stops only pushes matching the stream", func(t *testing.T) {
		url, calls := pushListMistServer(t, [][]any{
			{float64(1), "live+a", "rtmp://1.1.1.1/app", "rtmp://1.1.1.1/app"},
			{float64(2), "live+other", "rtmp://8.8.8.8/app", "rtmp://8.8.8.8/app"},
		})
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)

		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{StreamName: "live+a", SourceGeneration: "gen-a"})

		if n := calls("push_stop"); n != 1 {
			t.Fatalf("expected exactly one push_stop (only the matching stream), got %d", n)
		}
	})
}

func TestPushTargetCommands_UnknownGenerationFailsClosed(t *testing.T) {
	const runtimeName = "live+push-unknown-generation"
	url, calls := pushListMistServer(t, [][]any{{float64(1), runtimeName, "rtmp://1.1.1.1/app", "rtmp://1.1.1.1/app"}})
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})

	var activation *ipcpb.ActivatePushTargetsResult
	activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
		StreamName:       runtimeName,
		SourceGeneration: "unproven-generation",
		Targets:          []*ipcpb.PushTargetSpec{{TargetId: "t1", TargetUri: "rtmp://8.8.8.8/app"}},
	}, func(m *ipcpb.ControlMessage) {
		activation = m.GetActivatePushTargetsResult()
	})
	if activation == nil || activation.GetConverged() || activation.GetError() == "" {
		t.Fatalf("activation without a known local generation was not rejected: %+v", activation)
	}
	handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{
		StreamName:       runtimeName,
		SourceGeneration: "unproven-generation",
	})
	if calls("push_start") != 0 || calls("push_stop") != 0 {
		t.Fatal("unknown-generation push command reached Mist")
	}
}

func TestActivatePushTargets_RejectsEndedGeneration(t *testing.T) {
	const runtimeName = "live+ended-generation"
	if err := RecordAdmittedIngestGeneration(runtimeName, "generation-ended", 211); err != nil {
		t.Fatalf("RecordAdmittedIngestGeneration: %v", err)
	}
	if err := MarkAdmittedIngestGenerationEnded(runtimeName, 211); err != nil {
		t.Fatalf("MarkAdmittedIngestGenerationEnded: %v", err)
	}
	url, calls := pushListMistServer(t, nil)
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})
	var result *ipcpb.ActivatePushTargetsResult
	activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
		StreamName:       runtimeName,
		SourceGeneration: "generation-ended",
		Targets:          []*ipcpb.PushTargetSpec{{TargetId: "target", TargetUri: "rtmp://8.8.8.8/app"}},
	}, func(message *ipcpb.ControlMessage) {
		result = message.GetActivatePushTargetsResult()
	})
	if result == nil || result.GetConverged() || result.GetError() == "" {
		t.Fatalf("activation for ended generation was not rejected: %+v", result)
	}
	if calls("push_start") != 0 {
		t.Fatal("activation for ended generation reached Mist")
	}
}

func TestPushTargetCommands_RejectSupersededGeneration(t *testing.T) {
	const runtimeName = "live+push-generation-fence"
	RecordAdmittedIngestGeneration(runtimeName, "current-generation", 202)
	url, calls := pushListMistServer(t, [][]any{{float64(1), runtimeName, "rtmp://1.1.1.1/app", "rtmp://1.1.1.1/app"}})
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})

	var activation *ipcpb.ActivatePushTargetsResult
	activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
		StreamName:       runtimeName,
		SourceGeneration: "retired-generation",
		Targets:          []*ipcpb.PushTargetSpec{{TargetId: "t1", TargetUri: "rtmp://8.8.8.8/app"}},
	}, func(m *ipcpb.ControlMessage) {
		activation = m.GetActivatePushTargetsResult()
	})
	if activation == nil || activation.GetConverged() || activation.GetError() == "" {
		t.Fatalf("superseded activation was not rejected: %+v", activation)
	}
	if calls("push_start") != 0 {
		t.Fatal("superseded activation reached Mist")
	}

	handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{
		StreamName:       runtimeName,
		SourceGeneration: "retired-generation",
	})
	if calls("push_stop") != 0 {
		t.Fatal("superseded deactivation reached Mist")
	}
}

func TestPushTargetReconciliationSerializesActivateAndDeactivate(t *testing.T) {
	const (
		streamName = "live+serialized-restream"
		generation = "generation-serialized"
		targetURI  = "rtmp://8.8.8.8/live/key"
	)
	var mu sync.Mutex
	var pushes [][]any
	pushStartEntered := make(chan struct{})
	releasePushStart := make(chan struct{})
	overlap := make(chan struct{}, 1)
	blocked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var command map[string]any
		if raw := r.URL.Query().Get("command"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &command)
		} else {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &command)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, ok := command["authorize"]; ok {
			_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
			return
		}
		mu.Lock()
		if blocked {
			select {
			case overlap <- struct{}{}:
			default:
			}
		}
		mu.Unlock()
		if start, ok := command["push_start"].(map[string]any); ok {
			mu.Lock()
			blocked = true
			pushes = [][]any{{float64(1), start["stream"], start["target"], start["target"]}}
			mu.Unlock()
			close(pushStartEntered)
			<-releasePushStart
			mu.Lock()
			blocked = false
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if _, ok := command["push_stop"]; ok {
			mu.Lock()
			pushes = nil
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if _, ok := command["push_list"]; ok {
			mu.Lock()
			response, _ := json.Marshal(map[string]any{"push_list": pushes})
			mu.Unlock()
			_, _ = w.Write(response)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: srv.URL})
	if err := RecordAdmittedIngestGeneration(streamName, generation, 301); err != nil {
		t.Fatal(err)
	}
	clearRestreamDesiredState(streamName, "")
	t.Cleanup(func() { clearRestreamDesiredState(streamName, "") })

	activationDone := make(chan struct{})
	go func() {
		activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName: streamName, SourceGeneration: generation, TargetRevision: 1,
			Targets: []*ipcpb.PushTargetSpec{{TargetId: "target-a", TargetUri: targetURI}},
		}, nil)
		close(activationDone)
	}()
	<-pushStartEntered
	deactivationDone := make(chan struct{})
	go func() {
		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{
			StreamName: streamName, SourceGeneration: generation, TargetRevision: 1,
		})
		close(deactivationDone)
	}()
	select {
	case <-overlap:
		t.Fatal("activation and deactivation reached Mist concurrently")
	case <-time.After(200 * time.Millisecond):
	}
	close(releasePushStart)
	select {
	case <-activationDone:
	case <-time.After(2 * time.Second):
		t.Fatal("activation did not finish")
	}
	select {
	case <-deactivationDone:
	case <-time.After(2 * time.Second):
		t.Fatal("deactivation did not finish")
	}
	mu.Lock()
	remaining := len(pushes)
	mu.Unlock()
	if remaining != 0 {
		t.Fatalf("serialized deactivate left %d external pushes", remaining)
	}
}

func TestDeactivatePushTargetsDoesNotHoldGenerationFenceDuringMistIO(t *testing.T) {
	const streamName = "live+deactivate-generation-fence"
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var command map[string]any
		if raw := r.URL.Query().Get("command"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &command)
		} else {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &command)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, ok := command["authorize"]; ok {
			_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
			return
		}
		if _, ok := command["push_list"]; ok {
			blocked := false
			once.Do(func() { close(entered); blocked = true })
			if blocked {
				<-release
			}
			_, _ = w.Write([]byte(`{"push_list":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: srv.URL})
	if err := RecordAdmittedIngestGeneration(streamName, "generation-old", 302); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{
			StreamName: streamName, SourceGeneration: "generation-old",
		})
		close(done)
	}()
	<-entered
	replaced := make(chan error, 1)
	go func() { replaced <- RecordAdmittedIngestGeneration(streamName, "generation-new", 303) }()
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("generation replacement blocked behind deactivation Mist I/O")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deactivation did not finish")
	}
}

func TestActivatePushTargetsRemovesStalePushAfterGenerationChangesDuringMistIO(t *testing.T) {
	const (
		streamName = "live+activation-final-fence"
		targetURI  = "rtmp://8.8.8.8/live/key"
	)
	var mu sync.Mutex
	var pushes [][]any
	confirmationEntered := make(chan struct{})
	releaseConfirmation := make(chan struct{})
	pushLists := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var command map[string]any
		if raw := r.URL.Query().Get("command"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &command)
		} else {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &command)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, ok := command["authorize"]; ok {
			_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
			return
		}
		if start, ok := command["push_start"].(map[string]any); ok {
			mu.Lock()
			pushes = [][]any{{float64(1), start["stream"], start["target"], start["target"]}}
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if _, ok := command["push_stop"]; ok {
			mu.Lock()
			pushes = nil
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if _, ok := command["push_list"]; ok {
			mu.Lock()
			pushLists++
			listNumber := pushLists
			response, _ := json.Marshal(map[string]any{"push_list": pushes})
			mu.Unlock()
			if listNumber == 2 {
				close(confirmationEntered)
				<-releaseConfirmation
			}
			_, _ = w.Write(response)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: srv.URL})
	if err := RecordAdmittedIngestGeneration(streamName, "generation-old", 304); err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan *ipcpb.ActivatePushTargetsResult, 1)
	go activatePushTargetsForTest(t, logging.NewLogger(), &ipcpb.ActivatePushTargets{
		StreamName: streamName, SourceGeneration: "generation-old", TargetRevision: 1,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: "target-a", TargetUri: targetURI}},
	}, func(message *ipcpb.ControlMessage) { resultCh <- message.GetActivatePushTargetsResult() })
	<-confirmationEntered
	if err := RecordAdmittedIngestGeneration(streamName, "generation-new", 305); err != nil {
		t.Fatal(err)
	}
	close(releaseConfirmation)
	select {
	case result := <-resultCh:
		if result == nil || result.GetConverged() {
			t.Fatalf("superseded activation converged: %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("activation did not finish")
	}
	mu.Lock()
	remaining := len(pushes)
	mu.Unlock()
	if remaining != 0 {
		t.Fatalf("stale activation left %d external pushes", remaining)
	}
	resolved, ok := ResolveRestreamTarget(streamName, 1, targetURI, targetURI)
	if !ok || resolved.GetTargetId() != "target-a" || resolved.GetSourceGeneration() != "generation-old" {
		t.Fatalf("stale cleanup lost final-fact attribution before PushStop: ok=%v report=%+v", ok, resolved)
	}
}
