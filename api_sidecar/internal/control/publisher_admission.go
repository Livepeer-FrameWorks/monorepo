package control

import (
	"context"
	"sync/atomic"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

type publisherAdmissionConnection struct {
	client  ipcpb.HelmsmanControlClient
	session atomic.Pointer[ipcpb.PublisherAdmissionSession]
}

func sendPublisherAdmission(ctx context.Context, deadline time.Time, connection *streamConn, session *ipcpb.PublisherAdmissionSession, trigger *ipcpb.MistTrigger, admissionKey string) (*MistTriggerResult, error) {
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	response, err := connection.publisher.client.AdmitPublisher(ctx, &ipcpb.AdmitPublisherRequest{NodeId: session.GetNodeId(), SessionToken: session.GetToken(), Trigger: proto.CloneOf(trigger)})
	if err != nil {
		return &MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL}, err
	}
	result := mistTriggerResultFromResponse(response)
	if !result.Abort {
		if err = recordAdmittedIngestGeneration(result.Response, result.IngestGeneration, response.GetIngestConnectorPid(), admissionKey); err != nil {
			return &MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL}, err
		}
	}
	return result, nil
}
