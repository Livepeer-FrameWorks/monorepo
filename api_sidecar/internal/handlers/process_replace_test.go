package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// A Livepeer config as Mist supervised it: one process with a two-rung ladder,
// per-profile upscale inhibits, the source key Mist adds, and the job token.
const failedLivepeerConfig = `{"process":"Livepeer","source":"live+abc","job_token":"v1.x.y","hardcoded_broadcasters":"[{\"address\":\"https://gw\"}]","target_profiles":[{"name":"720p","profile":"H264ConstrainedHigh","height":720,"bitrate":3000000,"fps":30,"track_inhibit":"video=<1280x720"},{"name":"360p","profile":"H264ConstrainedHigh","height":360,"bitrate":800000,"fps":30,"track_inhibit":"video=<640x360"}]}`

func processReplaceBody(stream, processType, config string) string {
	return strings.Join([]string{stream, processType, config, "2", "ER_FORMAT_SPECIFIC", "Livepeer upload fatal HTTP status 403 Forbidden"}, "\n")
}

func callProcessReplace(t *testing.T, body string) (int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/mist/process_replace", strings.NewReader(body))
	HandleProcessReplace(ctx)
	return rec.Code, rec.Body.String()
}

func stubDegradedReports(t *testing.T) (func() []ProcessReplaceEvent, *sync.WaitGroup) {
	t.Helper()
	oldLogger := logger
	logger = logging.NewLogger()
	t.Cleanup(func() { logger = oldLogger })
	var mu sync.Mutex
	var reports []ProcessReplaceEvent
	wg := &sync.WaitGroup{}
	old := reportStreamTranscodeDegraded
	reportStreamTranscodeDegraded = func(evt ProcessReplaceEvent) error {
		mu.Lock()
		reports = append(reports, evt)
		mu.Unlock()
		wg.Done()
		return nil
	}
	t.Cleanup(func() { reportStreamTranscodeDegraded = old })
	return func() []ProcessReplaceEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]ProcessReplaceEvent(nil), reports...)
	}, wg
}

func TestParseProcessReplaceTrigger(t *testing.T) {
	evt, err := ParseProcessReplaceTrigger([]byte(processReplaceBody("live+abc", "Livepeer", failedLivepeerConfig)))
	if err != nil {
		t.Fatal(err)
	}
	if evt.StreamName != "live+abc" || evt.ProcessType != "Livepeer" || evt.Config != failedLivepeerConfig ||
		evt.ExitCode != 2 || evt.ShortReason != "ER_FORMAT_SPECIFIC" || !strings.Contains(evt.Reason, "403") {
		t.Fatalf("parsed %+v", evt)
	}
	if _, err := ParseProcessReplaceTrigger([]byte("live+abc\nLivepeer")); err == nil {
		t.Fatal("expected a short payload to fail")
	}
}

// A hard-failed Livepeer transcode is answered with the equivalent local AV
// ladder: one AV process per profile, no Livepeer entry, and no per-profile
// track_inhibit (split into separate processes, the inhibits would match the
// sibling renditions and stop the ladder). Foghorn is told about it.
func TestProcessReplaceAnswersFailedLivepeerWithLocalLadder(t *testing.T) {
	reports, wg := stubDegradedReports(t)
	wg.Add(1)

	code, body := callProcessReplace(t, processReplaceBody("live+abc", "Livepeer", failedLivepeerConfig))
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	var replacements []map[string]any
	if err := json.Unmarshal([]byte(body), &replacements); err != nil {
		t.Fatalf("response %q is not a JSON array: %v", body, err)
	}
	if len(replacements) != 2 {
		t.Fatalf("replacements = %s, want two AV renditions", body)
	}
	resolutions := map[string]bool{}
	for _, r := range replacements {
		if r["process"] != "AV" || r["codec"] != "H264" {
			t.Fatalf("replacement %v is not a local H264 AV process", r)
		}
		if _, ok := r["track_inhibit"]; ok {
			t.Fatalf("replacement %v carries a per-profile track_inhibit", r)
		}
		if _, ok := r["job_token"]; ok {
			t.Fatalf("replacement %v carries the Livepeer job token", r)
		}
		resolution, _ := r["resolution"].(string)
		resolutions[resolution] = true
	}
	if !resolutions["x720"] || !resolutions["x360"] {
		t.Fatalf("resolutions = %v, want the 720p and 360p rungs", resolutions)
	}

	wg.Wait()
	got := reports()
	if len(got) != 1 || got[0].StreamName != "live+abc" || got[0].ProcessType != "Livepeer" || got[0].ReplacementCount != 2 {
		t.Fatalf("degradation reports = %+v", got)
	}
}

func TestProcessReplaceLeavesOtherProcessesDisabled(t *testing.T) {
	reports, _ := stubDegradedReports(t)
	for _, tc := range []struct{ processType, config string }{
		{"AV", `{"process":"AV","codec":"H264","resolution":"x720"}`},
		{"Thumbs", `{"process":"Thumbs"}`},
		{"Livepeer", `not json`},
		{"Livepeer", `{"process":"Livepeer"}`},
	} {
		code, body := callProcessReplace(t, processReplaceBody("live+abc", tc.processType, tc.config))
		if code != http.StatusOK || body != noProcessReplacement {
			t.Fatalf("%s %s: status %d body %q, want 200 []", tc.processType, tc.config, code, body)
		}
	}
	if got := reports(); len(got) != 0 {
		t.Fatalf("unexpected degradation reports: %+v", got)
	}
}

func TestLivepeerExitFallbackDecision(t *testing.T) {
	for _, tc := range []struct {
		name string
		evt  ProcessExitEvent
		want livepeerFallbackDecision
	}{
		{"gateway 403", ProcessExitEvent{ProcessType: "Livepeer", Status: "unrecoverable", ExitCode: 2, Reason: "Livepeer upload fatal HTTP status 403"}, livepeerFallbackInPlace},
		{"too many upload failures", ProcessExitEvent{ProcessType: "Livepeer", Status: "unrecoverable", ExitCode: 2, Reason: "too many upload failures"}, livepeerFallbackInPlace},
		{"retrying non-zero exit", ProcessExitEvent{ProcessType: "Livepeer", Status: "retrying", ExitCode: 1}, livepeerFallbackRestart},
		{"clean exit", ProcessExitEvent{ProcessType: "Livepeer", Status: "clean"}, livepeerFallbackNone},
		{"supervisor stop", ProcessExitEvent{ProcessType: "Livepeer", Status: "stopped", ExitCode: 2}, livepeerFallbackNone},
		{"not livepeer", ProcessExitEvent{ProcessType: "AV", Status: "unrecoverable", ExitCode: 2}, livepeerFallbackNone},
	} {
		if got := livepeerExitFallbackDecision(tc.evt); got != tc.want {
			t.Errorf("%s: decision = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// Livepeer returns 403 during a VOD job: Mist fires PROCESS_EXIT and then
// PROCESS_REPLACE for the processing+ stream. Helmsman's answer is routed to the
// job, whose in-place wait completes on the first local video output instead of
// tearing the stream down.
func TestLivepeer403IsReplacedInPlaceForProcessingJob(t *testing.T) {
	_, wg := stubDegradedReports(t)
	wg.Add(1)
	const stream = "processing+importhash"
	replaceCh := RegisterProcessReplaceListener(stream)
	defer UnregisterProcessReplaceListener(stream)
	processAVCh := make(chan ProcessAVSegmentCompleteEvent, 1)
	exitCh := make(chan ProcessExitEvent, 1)

	exit := ProcessExitEvent{StreamName: stream, ProcessType: "Livepeer", Status: "unrecoverable", ExitCode: 2, BootCount: 1, Reason: "Livepeer upload fatal HTTP status 403 Forbidden"}
	if livepeerExitFallbackDecision(exit) != livepeerFallbackInPlace {
		t.Fatal("a 403 Livepeer exit must take the in-place path")
	}
	ignored := map[string]int{}
	ignoreProcessExitThrough(ignored, exit.ProcessType, exit.BootCount)

	done := make(chan error, 1)
	go func() {
		done <- awaitInPlaceLivepeerReplacement(context.Background(), logrus.NewEntry(logrus.New()), exitCh, processAVCh, replaceCh, ignored, time.Second, 2*time.Second)
	}()
	if code, body := callProcessReplace(t, processReplaceBody(stream, "Livepeer", failedLivepeerConfig)); code != http.StatusOK || body == noProcessReplacement {
		t.Fatalf("PROCESS_REPLACE status %d body %q", code, body)
	}
	processAVCh <- ProcessAVSegmentCompleteEvent{StreamName: stream, TrackType: "video", OutputCodec: "H264", OutputWidth: 1280, OutputHeight: 720, OutputFrames: 60}
	if err := <-done; err != nil {
		t.Fatalf("in-place replacement failed: %v", err)
	}
	wg.Wait()
}

// A Mist build without PROCESS_REPLACE never asks for a replacement; the job
// must not wait out the output window before restarting on local processes.
func TestInPlaceReplacementGivesUpWithoutProcessReplace(t *testing.T) {
	start := time.Now()
	err := awaitInPlaceLivepeerReplacement(context.Background(), logrus.NewEntry(logrus.New()), nil, nil, nil, map[string]int{}, 150*time.Millisecond, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "PROCESS_REPLACE") {
		t.Fatalf("err = %v, want a missing PROCESS_REPLACE error", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("waited %s, want the confirm window", time.Since(start))
	}
}

func TestInPlaceReplacementFailsOnEmptyAnswerOrCriticalExit(t *testing.T) {
	replaceCh := make(chan ProcessReplaceEvent, 1)
	replaceCh <- ProcessReplaceEvent{ProcessType: "Livepeer"}
	if err := awaitInPlaceLivepeerReplacement(context.Background(), logrus.NewEntry(logrus.New()), nil, nil, replaceCh, map[string]int{}, time.Second, time.Second); err == nil {
		t.Fatal("an empty PROCESS_REPLACE answer must end the in-place wait")
	}

	exitCh := make(chan ProcessExitEvent, 1)
	exitCh <- ProcessExitEvent{ProcessType: "AV", Status: "unrecoverable", Config: `{"process":"AV","codec":"H264"}`, Reason: "encoder died"}
	err := awaitInPlaceLivepeerReplacement(context.Background(), logrus.NewEntry(logrus.New()), exitCh, nil, nil, map[string]int{}, time.Second, time.Second)
	if err == nil || !strings.Contains(err.Error(), "encoder died") {
		t.Fatalf("err = %v, want the replacement's failure", err)
	}
}

func TestInPlaceReplacementExpiry(t *testing.T) {
	start := time.Unix(1000, 0)
	r := &inPlaceLivepeerReplacement{startedAt: start}
	if reason := r.expiredReason(start.Add(5*time.Second), 10*time.Second, 45*time.Second); reason != "" {
		t.Fatalf("expired early: %s", reason)
	}
	if reason := r.expiredReason(start.Add(11*time.Second), 10*time.Second, 45*time.Second); !strings.Contains(reason, "PROCESS_REPLACE") {
		t.Fatalf("unconfirmed replacement reason = %q", reason)
	}
	r.confirmed = true
	if reason := r.expiredReason(start.Add(11*time.Second), 10*time.Second, 45*time.Second); reason != "" {
		t.Fatalf("confirmed replacement expired at 11s: %s", reason)
	}
	if reason := r.expiredReason(start.Add(46*time.Second), 10*time.Second, 45*time.Second); reason == "" {
		t.Fatal("confirmed replacement without output must expire after the output window")
	}
}
