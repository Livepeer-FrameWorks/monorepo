package control

import ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

// SendProcessingMemoryReleased is a best-effort queue wake-up. The durable job
// and reservation state remain authoritative if the connection misses it.
func SendProcessingMemoryReleased(jobID string) error {
	return sendControlMessage(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ProcessingJobResult{
		ProcessingJobResult: &ipcpb.ProcessingJobResult{JobId: jobID, Status: "memory_released"},
	}})
}
