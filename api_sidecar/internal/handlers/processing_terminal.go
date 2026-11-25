package handlers

import (
	"sync"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// A terminal result lets Foghorn redispatch immediately. Publish it only after
// the handler's resource cleanup and local ownership release have returned.
func deferProcessingTerminalResult(forward func(*ipcpb.ControlMessage)) (func(*ipcpb.ControlMessage), func()) {
	var mu sync.Mutex
	var once sync.Once
	var terminal *ipcpb.ControlMessage
	send := func(message *ipcpb.ControlMessage) {
		if message == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if terminal != nil {
			return
		}
		if result := message.GetProcessingJobResult(); result != nil && result.GetStatus() != "cache_update" {
			terminal = proto.CloneOf(message)
			return
		}
		if forward != nil {
			forward(message)
		}
	}
	flush := func() {
		once.Do(func() {
			mu.Lock()
			message := terminal
			mu.Unlock()
			if message != nil && forward != nil {
				forward(message)
			}
		})
	}
	return send, flush
}
