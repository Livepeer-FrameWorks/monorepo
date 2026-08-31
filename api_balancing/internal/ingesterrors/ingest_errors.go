package ingesterrors

import ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

// IngestError is a typed error that can be mapped into MistTriggerResponse.error_code
// so clients get structured ingest failure reasons.
//
// NOTE: This lives outside the triggers/control packages to avoid import cycles.
type IngestError struct {
	Code    ipcpb.IngestErrorCode
	Message string
	retry   *bool
}

func (e *IngestError) Error() string { return e.Message }

func New(code ipcpb.IngestErrorCode, message string) *IngestError {
	return &IngestError{Code: code, Message: message}
}

// NewTerminal marks a deterministic outcome that will not change when the
// same Mist trigger UUID is delivered again. The public ingest error code is
// unchanged; retry disposition is an internal delivery property.
func NewTerminal(code ipcpb.IngestErrorCode, message string) *IngestError {
	retry := false
	return &IngestError{Code: code, Message: message, retry: &retry}
}

func (e *IngestError) RetryableOverride() (bool, bool) {
	if e == nil || e.retry == nil {
		return false, false
	}
	return *e.retry, true
}
