package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type fakeActiveStreams struct {
	body   string
	err    error
	asked  []string
	called int
}

func (f *fakeActiveStreams) GetActiveStreamsFilteredContext(_ context.Context, streams []string) (map[string]interface{}, error) {
	f.called++
	f.asked = streams
	if f.err != nil {
		return nil, f.err
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(f.body), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// The stop report carries Mist's view of the recording's source: an online buffer is live;
// a stream Mist no longer lists, or a buffer shutting down or offline, has ended; anything
// else, and a failed read, is not known.
func TestDVRSourceStateFromMistActiveStreams(t *testing.T) {
	const stream = "live+abc"
	cases := []struct {
		name string
		body string
		err  error
		want ipcpb.DVRSourceState
	}{
		{"online buffer", `{"active_streams":{"live+abc":{"status":"Online","inputs":1}}}`, nil, ipcpb.DVRSourceState_DVR_SOURCE_STATE_LIVE},
		{"buffer shutting down", `{"active_streams":{"live+abc":{"status":"Shutting down","inputs":1}}}`, nil, ipcpb.DVRSourceState_DVR_SOURCE_STATE_ENDED},
		{"buffer offline", `{"active_streams":{"live+abc":{"status":"Offline"}}}`, nil, ipcpb.DVRSourceState_DVR_SOURCE_STATE_ENDED},
		{"no active stream matched", `{"active_streams":null}`, nil, ipcpb.DVRSourceState_DVR_SOURCE_STATE_ENDED},
		{"another stream is active", `{"active_streams":{"live+other":{"status":"Online"}}}`, nil, ipcpb.DVRSourceState_DVR_SOURCE_STATE_ENDED},
		{"buffer waiting for data", `{"active_streams":{"live+abc":{"status":"Waiting for data"}}}`, nil, ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED},
		{"input booting", `{"active_streams":{"live+abc":{"status":"Input booting"}}}`, nil, ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED},
		{"no status field", `{"active_streams":{"live+abc":{"inputs":1}}}`, nil, ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED},
		{"answer without active_streams", `{"authorize":{"status":"OK"}}`, nil, ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED},
		{"Mist API failed", "", errors.New("connection refused"), ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeActiveStreams{body: tc.body, err: tc.err}
			if got := mistDVRSourceState(reader)(stream); got != tc.want {
				t.Fatalf("source state = %v, want %v", got, tc.want)
			}
			if len(reader.asked) != 1 || reader.asked[0] != stream {
				t.Fatalf("asked Mist for %v, want only %q", reader.asked, stream)
			}
		})
	}
}

// A recording reported without a source stream name never queries Mist.
func TestDVRSourceStateWithoutStreamName(t *testing.T) {
	reader := &fakeActiveStreams{body: `{"active_streams":null}`}
	if got := mistDVRSourceState(reader)(""); got != ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED || reader.called != 0 {
		t.Fatalf("source state = %v after %d Mist reads, want UNSPECIFIED and none", got, reader.called)
	}
}

// The stop report carries the source state read for the recording's own stream.
func TestSendCompletionReportsSourceState(t *testing.T) {
	var asked string
	var sent []*ipcpb.ControlMessage
	dm := &DVRManager{
		logger: logging.NewLogger(),
		jobs:   make(map[string]*DVRJob),
		sourceState: func(streamName string) ipcpb.DVRSourceState {
			asked = streamName
			return ipcpb.DVRSourceState_DVR_SOURCE_STATE_ENDED
		},
	}
	job := &DVRJob{DVRHash: "h1", StreamName: "live+abc", SendFunc: func(m *ipcpb.ControlMessage) { sent = append(sent, m) }}

	dm.sendCompletion(job, "success", "")

	if len(sent) != 1 || sent[0].GetDvrStopped() == nil {
		t.Fatalf("want one DVRStopped, got %v", sent)
	}
	if got := sent[0].GetDvrStopped().GetSourceState(); got != ipcpb.DVRSourceState_DVR_SOURCE_STATE_ENDED || asked != "live+abc" {
		t.Fatalf("reported %v for stream %q, want ENDED for live+abc", got, asked)
	}

	sent = nil
	dm.sourceState = nil
	dm.sendCompletion(job, "success", "")
	if got := sent[0].GetDvrStopped().GetSourceState(); got != ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED {
		t.Fatalf("without a source reader reported %v, want UNSPECIFIED", got)
	}
}
