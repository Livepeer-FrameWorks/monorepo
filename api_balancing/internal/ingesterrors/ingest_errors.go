package ingesterrors

import ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

// IngestError is a typed error that can be mapped into MistTriggerResponse.error_code
// so clients get structured ingest failure reasons.
//
// NOTE: This lives outside the triggers/control packages to avoid import cycles.
type IngestError struct {
	Code    ipcpb.IngestErrorCode
	Message string
}

func (e *IngestError) Error() string { return e.Message }

func New(code ipcpb.IngestErrorCode, message string) *IngestError {
	return &IngestError{Code: code, Message: message}
}
