package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func assertOK(t *testing.T, rec *httptest.ResponseRecorder, wantBody string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != wantBody {
		t.Fatalf("body = %q, want %q", rec.Body.String(), wantBody)
	}
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, want, rec.Body.String())
	}
}

func assertAction(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := rec.Header().Get("X-Mist-Trigger-Action"); got != want {
		t.Fatalf("X-Mist-Trigger-Action = %q, want %q", got, want)
	}
}

// Blocking triggers hold the Mist request open until Foghorn answers. A known
// result is returned with an explicit action; parse, forward, and timeout
// failures return non-2xx so Mist applies the configured per-trigger onfail
// action. These tests drive the result and failure branches the happy-path
// tests do not cover.
//
// Note on the forward-error stub: these handlers dereference result.ErrorCode
// when logging a forward failure, so the stub must return a non-nil result
// alongside the error (control.SendMistTrigger does exactly that in production).

func registerPendingJob(t *testing.T, streamName string) {
	t.Helper()
	pendingJobsMu.Lock()
	pendingJobs[streamName] = make(chan ProcessingPushEndEvent, 1)
	pendingJobsMu.Unlock()
	t.Cleanup(func() {
		pendingJobsMu.Lock()
		delete(pendingJobs, streamName)
		pendingJobsMu.Unlock()
	})
}

// HandlePushOutStart is entirely uncovered. Empty response aborts the outbound
// push; a non-empty response is the (possibly rewritten) target Mist will use.
func TestHandlePushOutStart(t *testing.T) {
	setupTriggerTest(t, "tenant-blk")
	const body = "live+stream-1\nrtmp://target.example/app"

	t.Run("success returns foghorn response", func(t *testing.T) {
		var got *ipcpb.MistTrigger
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			got = trigger
			return &control.MistTriggerResult{Response: "rtmp://rewritten/app"}, nil
		})
		ctx, rec := newWebhookContext(body)
		HandlePushOutStart(ctx)
		assertOK(t, rec, "rtmp://rewritten/app")
		if got.GetTenantId() != "tenant-blk" {
			t.Fatalf("tenant not applied: %q", got.GetTenantId())
		}
	})

	t.Run("abort yields empty response", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{Abort: true}, nil
		})
		ctx, rec := newWebhookContext(body)
		HandlePushOutStart(ctx)
		assertOK(t, rec, "")
		assertAction(t, rec, "deny")
	})

	t.Run("forward error yields empty response", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{}, errors.New("foghorn down")
		})
		ctx, rec := newWebhookContext(body)
		HandlePushOutStart(ctx)
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})

	t.Run("parse error yields empty response", func(t *testing.T) {
		called := false
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			called = true
			return &control.MistTriggerResult{}, nil
		})
		ctx, rec := newWebhookContext("live+stream-1") // needs 2 params
		HandlePushOutStart(ctx)
		assertStatus(t, rec, http.StatusBadRequest)
		if called {
			t.Fatal("malformed PUSH_OUT_START must not be forwarded")
		}
	})
}

// PushRewrite: empty string denies the push. Cover the abort and forward-error
// branches (happy path + parse error already covered elsewhere).
func TestHandlePushRewriteDenialBranches(t *testing.T) {
	setupTriggerTest(t, "tenant-blk")
	const body = "rtmp://ingest/app\nexample.com\nstream-key"

	t.Run("abort denies", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_INVALID_STREAM_KEY}, nil
		})
		ctx, rec := newWebhookContext(body)
		HandlePushRewrite(ctx)
		assertOK(t, rec, "")
		assertAction(t, rec, "deny")
	})

	t.Run("forward error denies", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL}, errors.New("foghorn down")
		})
		ctx, rec := newWebhookContext(body)
		HandlePushRewrite(ctx)
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})
}

// PLAY_REWRITE distinguishes authoritative denial from handler unavailability.
func TestHandlePlayRewriteBranches(t *testing.T) {
	setupTriggerTest(t, "tenant-blk")
	clearPlayRewriteCache()
	const body = "stream-name\n192.0.2.10\nHLS\nhttp://example.com/view"

	t.Run("abort returns deny action", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{Abort: true}, nil
		})
		ctx, rec := newWebhookContext(body)
		HandlePlayRewrite(ctx)
		assertOK(t, rec, "")
		assertAction(t, rec, "deny")
	})

	t.Run("forward error without cache returns unavailable", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{}, errors.New("foghorn down")
		})
		ctx, rec := newWebhookContext(body)
		HandlePlayRewrite(ctx)
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})

	t.Run("empty non-abort success returns deny action", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{Response: ""}, nil
		})
		ctx, rec := newWebhookContext(body)
		HandlePlayRewrite(ctx)
		assertOK(t, rec, "")
		assertAction(t, rec, "deny")
	})

	t.Run("in-flight processing job resolves locally", func(t *testing.T) {
		registerPendingJob(t, "processing+localjob")
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			t.Fatal("processing+ with a pending job must not reach Foghorn")
			return nil, nil
		})
		ctx, rec := newWebhookContext("processing+localjob\n192.0.2.10\nHLS\nhttp://example.com/view")
		HandlePlayRewrite(ctx)
		assertOK(t, rec, "processing+localjob")
	})
}

// A reachable Foghorn is consulted on EVERY request: PLAY_REWRITE carries
// per-viewer billing/accounting/analytics on the Foghorn side, so the handler
// must not short-circuit a repeat from local cache while Foghorn is up.
func TestHandlePlayRewriteAlwaysConsultsReachableFoghorn(t *testing.T) {
	setupTriggerTest(t, "tenant-blk")
	clearPlayRewriteCache()
	const body = "frameworks-demo\n192.0.2.10\nHLS\nhttp://example.com/view"

	calls := 0
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		calls++
		return &control.MistTriggerResult{Response: "60546679b497415db2338cd5cae54992"}, nil
	})

	ctx, rec := newWebhookContext(body)
	HandlePlayRewrite(ctx)
	assertOK(t, rec, "60546679b497415db2338cd5cae54992")

	ctx, rec = newWebhookContext(body)
	HandlePlayRewrite(ctx)
	assertOK(t, rec, "60546679b497415db2338cd5cae54992")
	if calls != 2 {
		t.Fatalf("Foghorn calls = %d, want 2 (reachable Foghorn must be consulted every time)", calls)
	}
}

// When Foghorn is UNREACHABLE, the handler replays the last Foghorn-approved
// resolution from the recovery cache rather than failing the handler — the
// only case where the local cache answers.
func TestHandlePlayRewriteRecoversFromForwardErrorWithCache(t *testing.T) {
	setupTriggerTest(t, "tenant-blk")
	clearPlayRewriteCache()
	const body = "frameworks-demo\n192.0.2.10\nHLS\nhttp://example.com/view"
	rememberPlayRewrite("frameworks-demo", "60546679b497415db2338cd5cae54992")

	calls := 0
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		calls++
		return &control.MistTriggerResult{ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT}, errors.New("foghorn down")
	})

	ctx, rec := newWebhookContext(body)
	HandlePlayRewrite(ctx)
	assertOK(t, rec, "60546679b497415db2338cd5cae54992")
	if calls != 1 {
		t.Fatalf("Foghorn calls = %d, want 1", calls)
	}
}

// StreamProcess returns a JSON process-override array. Cover the local override,
// forward error, non-empty success, and empty/default branches.
func TestHandleStreamProcessBranches(t *testing.T) {
	setupTriggerTest(t, "tenant-blk")

	t.Run("local override short-circuits", func(t *testing.T) {
		setProcessingProcessOverride("processing+ovr", "[{\"process\":\"MKVExec\"}]")
		t.Cleanup(func() { clearProcessingProcessOverride("processing+ovr") })
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			t.Fatal("local process override must not reach Foghorn")
			return nil, nil
		})
		ctx, rec := newWebhookContext("processing+ovr")
		HandleStreamProcess(ctx)
		assertOK(t, rec, "[{\"process\":\"MKVExec\"}]")
	})

	t.Run("forward error returns unavailable", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{}, errors.New("foghorn down")
		})
		ctx, rec := newWebhookContext("live+stream-1")
		HandleStreamProcess(ctx)
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})

	t.Run("non-empty foghorn response is returned", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{Response: "[{\"process\":\"Livepeer\"}]"}, nil
		})
		ctx, rec := newWebhookContext("live+stream-1")
		HandleStreamProcess(ctx)
		assertOK(t, rec, "[{\"process\":\"Livepeer\"}]")
	})

	t.Run("empty foghorn response keeps wildcard default", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{Response: ""}, nil
		})
		ctx, rec := newWebhookContext("live+stream-1")
		HandleStreamProcess(ctx)
		assertOK(t, rec, "")
		assertAction(t, rec, "use-configured")
	})
}

// StreamSource: empty string makes Mist use its default source. Cover the
// Foghorn forward path (success/abort/error) and the processing+ shortcut
// branch where a job is pending but nothing is staged on disk (falls through to
// the forward path — the staged-file hit needs a real file and is left as a
// follow-up).
func TestHandleStreamSourceForwardBranches(t *testing.T) {
	setupTriggerTest(t, "tenant-blk")

	t.Run("success returns resolved source", func(t *testing.T) {
		var got *ipcpb.MistTrigger
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			got = trigger
			return &control.MistTriggerResult{Response: "dtsc://origin/live+stream-1"}, nil
		})
		ctx, rec := newWebhookContext("live+stream-1")
		HandleStreamSource(ctx)
		assertOK(t, rec, "dtsc://origin/live+stream-1")
		if got.GetTenantId() != "tenant-blk" {
			t.Fatalf("tenant not applied: %q", got.GetTenantId())
		}
	})

	t.Run("abort returns offline marker", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{Abort: true}, nil
		})
		ctx, rec := newWebhookContext("live+stream-1")
		HandleStreamSource(ctx)
		assertOK(t, rec, config.StreamSourceUnavailable)
		assertAction(t, rec, "offline")
	})

	t.Run("forward error returns unavailable", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{}, errors.New("foghorn down")
		})
		ctx, rec := newWebhookContext("live+stream-1")
		HandleStreamSource(ctx)
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})

	t.Run("pending processing job without staged file falls through to foghorn", func(t *testing.T) {
		registerPendingJob(t, "processing+sourcejob")
		forwarded := false
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			forwarded = true
			return &control.MistTriggerResult{Response: "dtsc://origin/processing+sourcejob"}, nil
		})
		ctx, rec := newWebhookContext("processing+sourcejob")
		HandleStreamSource(ctx)
		assertOK(t, rec, "dtsc://origin/processing+sourcejob")
		if !forwarded {
			t.Fatal("with no staged file the request must still reach Foghorn")
		}
	})
}

// UserNew: "true" admits the viewer, "false" denies. Cover the fail-closed
// branches (forward error and Foghorn abort both deny).
func TestHandleUserNewDenialBranches(t *testing.T) {
	setupTriggerTest(t, "tenant-blk")
	const body = "live+stream-1\n192.0.2.20\nconn-1\nHLS\nhttp://example.com/view\nsess-1"

	t.Run("forward error denies", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL}, errors.New("foghorn down")
		})
		ctx, rec := newWebhookContext(body)
		HandleUserNew(ctx)
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})

	t.Run("abort denies", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{Abort: true}, nil
		})
		ctx, rec := newWebhookContext(body)
		HandleUserNew(ctx)
		assertOK(t, rec, "")
		assertAction(t, rec, "deny")
	})
}
