package triggers

import pb "frameworks/pkg/proto"

type IngestError struct {
	Code    pb.IngestErrorCode
	Message string
}

func (e *IngestError) Error() string {
	return e.Message
}

func NewIngestError(code pb.IngestErrorCode, message string) *IngestError {
	return &IngestError{Code: code, Message: message}
}
