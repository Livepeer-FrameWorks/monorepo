package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestPlaybackGrantRequestEvidenceCountsAttempts(t *testing.T) {
	for _, tc := range []struct {
		name          string
		sendError     bool
		responseError bool
		noConnection  bool
	}{
		{name: "grant returned"},
		{name: "grant refused", responseError: true},
		{name: "send failed", sendError: true},
		{name: "not connected", noConnection: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := logging.NewLogger()
			logger.SetLevel(logging.InfoLevel)
			logger.SetOutput(&output)
			previousLogger := pkgLogger
			pkgLogger = logger
			t.Cleanup(func() { pkgLogger = previousLogger })
			requestID := ""
			stream := &stopAuthorizationStream{send: func(message *ipcpb.ControlMessage) error {
				request := message.GetPlaybackGrantRequest()
				if request == nil {
					t.Fatal("expected a playback grant request")
				}
				requestID = request.GetRequestId()
				if tc.sendError {
					return errors.New("connection lost before send")
				}
				response := &ipcpb.PlaybackGrantResponse{RequestId: requestID, Grant: &ipcpb.PlaybackGrant{InternalName: request.GetInternalName()}}
				if tc.responseError {
					response.Grant, response.Error = nil, "authority unavailable"
				}
				handlePlaybackGrantResponse(response)
				return nil
			}}
			connection := &streamConn{stream: stream}
			if tc.noConnection {
				connection = nil
			}
			previous := activeConn.Swap(connection)
			t.Cleanup(func() { activeConn.Store(previous) })
			_, err := RequestPlaybackGrant(t.Context(), "live+fixture")
			if (err != nil) != (tc.sendError || tc.responseError || tc.noConnection) {
				t.Fatalf("request error: %v", err)
			}
			if tc.noConnection {
				if output.Len() != 0 {
					t.Fatalf("disconnected caller counted as a request: %s", &output)
				}
				return
			}
			var row map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &row); err != nil {
				t.Fatalf("missing one structured request-attempt event: %q (%v)", &output, err)
			}
			if row["msg"] != "Playback grant requested" || row["request_id"] != requestID || row["internal_name"] != "live+fixture" {
				t.Fatalf("request-attempt event has no exact request scope: %v", row)
			}
		})
	}
}
