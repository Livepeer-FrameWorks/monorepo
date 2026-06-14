package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// Blocking triggers hold the Mist request open until Foghorn answers, and the
// handler's *text response* is the decision Mist enforces. The shared safety
// rule is fail-closed: any error (parse, forward, timeout) must resolve to the
// safe default — deny the push / let Mist fall back — never an accidental allow.
// These tests drive the stub's MistTriggerResult to reach the deny/abort/error
// branches the existing happy-path tests don't cover.
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
	})

	t.Run("forward error yields empty response", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{}, errors.New("foghorn down")
		})
		ctx, rec := newWebhookContext(body)
		HandlePushOutStart(ctx)
		assertOK(t, rec, "")
	})

	t.Run("parse error yields empty response", func(t *testing.T) {
		called := false
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			called = true
			return &control.MistTriggerResult{}, nil
		})
		ctx, rec := newWebhookContext("live+stream-1") // needs 2 params
		HandlePushOutStart(ctx)
		assertOK(t, rec, "")
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
	})

	t.Run("forward error denies", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL}, errors.New("foghorn down")
		})
		ctx, rec := newWebhookContext(body)
		HandlePushRewrite(ctx)
		assertOK(t, rec, "")
	})
}

// PlayRewrite: empty string = Mist default behavior. Cover abort, forward error,
// and the local processing+ shortcut (an in-flight job is served locally without
// consulting Foghorn).
func TestHandlePlayRewriteBranches(t *testing.T) {
	setupTriggerTest(t, "tenant-blk")
	const body = "stream-name\n192.0.2.10\nHLS\nhttp://example.com/view"

	t.Run("abort uses default", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{Abort: true}, nil
		})
		ctx, rec := newWebhookContext(body)
		HandlePlayRewrite(ctx)
		assertOK(t, rec, "")
	})

	t.Run("forward error uses default", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{}, errors.New("foghorn down")
		})
		ctx, rec := newWebhookContext(body)
		HandlePlayRewrite(ctx)
		assertOK(t, rec, "")
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

func TestHandlePlayRewriteUsesCachedFoghornSuccess(t *testing.T) {
	setupTriggerTest(t, "tenant-blk")
	const body = "frameworks-demo\n192.0.2.10\nHLS\nhttp://example.com/view"

	calls := 0
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		calls++
		if calls == 1 {
			return &control.MistTriggerResult{Response: "60546679b497415db2338cd5cae54992"}, nil
		}
		t.Fatal("fresh cached PLAY_REWRITE must not reach Foghorn")
		return nil, nil
	})

	ctx, rec := newWebhookContext(body)
	HandlePlayRewrite(ctx)
	assertOK(t, rec, "60546679b497415db2338cd5cae54992")

	ctx, rec = newWebhookContext(body)
	HandlePlayRewrite(ctx)
	assertOK(t, rec, "60546679b497415db2338cd5cae54992")
	if calls != 1 {
		t.Fatalf("Foghorn calls = %d, want 1", calls)
	}
}

func TestHandlePlayRewriteRecoversFromForwardErrorWithCache(t *testing.T) {
	setupTriggerTest(t, "tenant-blk")
	const body = "frameworks-demo\n192.0.2.10\nHLS\nhttp://example.com/view"
	rememberPlayRewrite("frameworks-demo", "60546679b497415db2338cd5cae54992")

	playRewriteCache.Lock()
	entry := playRewriteCache.entries["frameworks-demo"]
	entry.storedAt = entry.storedAt.Add(-playRewriteBurstTTL - time.Millisecond)
	playRewriteCache.entries["frameworks-demo"] = entry
	playRewriteCache.Unlock()

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

	t.Run("forward error returns empty", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{}, errors.New("foghorn down")
		})
		ctx, rec := newWebhookContext("live+stream-1")
		HandleStreamProcess(ctx)
		assertOK(t, rec, "")
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

	t.Run("abort uses default source", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{Abort: true}, nil
		})
		ctx, rec := newWebhookContext("live+stream-1")
		HandleStreamSource(ctx)
		assertOK(t, rec, "")
	})

	t.Run("forward error uses default source", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{}, errors.New("foghorn down")
		})
		ctx, rec := newWebhookContext("live+stream-1")
		HandleStreamSource(ctx)
		assertOK(t, rec, "")
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
		assertOK(t, rec, "false")
	})

	t.Run("abort denies", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{Abort: true}, nil
		})
		ctx, rec := newWebhookContext(body)
		HandleUserNew(ctx)
		assertOK(t, rec, "false")
	})
}
