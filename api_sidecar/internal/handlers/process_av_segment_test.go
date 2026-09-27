package handlers

import (
	"net/http"
	"strings"
	"testing"

	"frameworks/api_sidecar/internal/control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// processAVPayload is a PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE body with the
// fields a window's recording depends on; the rest are zero.
func processAVPayload(trackType, secondsSinceLast, inFramesDelta, outFramesDelta, inputCodec, outputCodec, sampleRate string) string {
	fields := make([]string, 31)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "live+stream-audio"
	fields[1] = trackType
	fields[2] = secondsSinceLast
	fields[5] = inFramesDelta
	fields[6] = outFramesDelta
	fields[12] = inputCodec
	fields[13] = outputCodec
	fields[20] = sampleRate
	return strings.Join(fields, "\n")
}

// The audio process's first report comes before its decoder opens: Mist
// labels it video, names no input codec and carries no media. It records
// nothing; later windows are recorded under the audio label their output
// codec gives them.
func TestProcessAVWindowWithoutDecodedMediaIsNotRecorded(t *testing.T) {
	setupTriggerTest(t, "tenant-procav")
	var forwarded []*ipcpb.MistTrigger
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		forwarded = append(forwarded, trigger)
		return &control.MistTriggerResult{}, nil
	})

	ctx, recorder := newWebhookContext(processAVPayload("video", "5", "0", "0", "none", "libopus", "0"))
	HandleProcessAVSegmentComplete(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("early window answered %d", recorder.Code)
	}
	if len(forwarded) != 0 {
		t.Fatalf("a window with no decoded media was recorded: %+v", forwarded[0].GetProcessBilling())
	}

	ctx, recorder = newWebhookContext(processAVPayload("video", "5", "250", "250", "aac", "libopus", "0"))
	HandleProcessAVSegmentComplete(ctx)
	if recorder.Code != http.StatusOK || len(forwarded) != 1 {
		t.Fatalf("processed window: code %d, recorded %d", recorder.Code, len(forwarded))
	}
	billing := forwarded[0].GetProcessBilling()
	if billing.GetTrackType() != "audio" || billing.GetInputCodec() != "aac" || billing.GetOutputCodec() != "opus" || billing.GetDurationMs() != 5000 {
		t.Fatalf("processed audio window recorded as %s %s->%s %d ms", billing.GetTrackType(), billing.GetInputCodec(), billing.GetOutputCodec(), billing.GetDurationMs())
	}
}

// A process's pre-decoder report never reaches its billing window; the processed samples around it
// join one window under the process id and the audio label their output codec gives them.
func TestProcessAVPreDecoderSampleStaysOutOfBillingWindow(t *testing.T) {
	setupTriggerTest(t, "tenant-procav")
	type recorded struct {
		key     string
		billing *ipcpb.ProcessBillingEvent
	}
	var got []recorded
	original := recordProcessBillingSample
	recordProcessBillingSample = func(key string, trigger *ipcpb.MistTrigger) (string, error) {
		got = append(got, recorded{key: key, billing: trigger.GetProcessBilling()})
		return "window", nil
	}
	t.Cleanup(func() { recordProcessBillingSample = original })

	for _, body := range []string{
		processAVPayload("video", "1", "0", "0", "none", "libopus", "0"),
		processAVPayload("video", "1", "50", "50", "aac", "libopus", "0"),
		processAVPayload("video", "0", "0", "0", "aac", "libopus", "0"),
		processAVPayload("audio", "1", "48", "48", "aac", "libopus", "48000"),
	} {
		ctx, recorder := newWebhookContext(body)
		ctx.Request.Header.Set("X-PID", "4242")
		HandleProcessAVSegmentComplete(ctx)
		if recorder.Code != http.StatusOK {
			t.Fatalf("window answered %d", recorder.Code)
		}
	}
	if len(got) != 2 {
		t.Fatalf("recorded %d samples, want the 2 with decoded media", len(got))
	}
	if got[0].key != got[1].key || !strings.HasPrefix(got[0].key, "4242\x00") || !strings.Contains(got[0].key, "\x00audio\x00") {
		t.Fatalf("samples of one audio process keyed %q and %q", got[0].key, got[1].key)
	}
	if total := got[0].billing.GetDurationMs() + got[1].billing.GetDurationMs(); total != 2000 {
		t.Fatalf("window duration %d ms, want 2000", total)
	}
}
