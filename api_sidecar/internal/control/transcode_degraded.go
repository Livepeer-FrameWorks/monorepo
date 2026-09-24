package control

import (
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SendStreamTranscodeDegraded tells Foghorn that Mist replaced a hard-failed
// transcode process on this node. Delivery is bounded: the report is
// diagnostic and accounting input, and the stream keeps running either way.
func SendStreamTranscodeDegraded(stream, failedProcessType, reason string, replacementCount int) error {
	msg := &ipcpb.ControlMessage{
		SentAt: timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_StreamTranscodeDegraded{StreamTranscodeDegraded: &ipcpb.StreamTranscodeDegraded{
			Stream:            stream,
			Reason:            reason,
			FailedProcessType: failedProcessType,
			ReplacementCount:  int32(replacementCount),
		}},
	}
	return sendControlMessage(msg)
}
