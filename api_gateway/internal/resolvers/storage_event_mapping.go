package resolvers

import (
	"fmt"
	"strings"
	"time"

	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func mapSignalmanStorageEvent(event *pb.SignalmanEvent) *pb.StorageEvent {
	if event == nil || event.Data == nil {
		return nil
	}

	data := event.Data.GetStorageLifecycle()
	if data == nil {
		return nil
	}

	timestamp := time.Now()
	if event.Timestamp != nil {
		timestamp = event.Timestamp.AsTime()
	}

	streamID := data.GetStreamId()

	action := strings.ToLower(strings.TrimPrefix(data.GetAction().String(), "ACTION_"))
	eventID := fmt.Sprintf("live:%s:%s:%d", data.GetAssetHash(), action, timestamp.UnixNano())

	tenantID := ""
	if event.TenantId != nil && *event.TenantId != "" {
		tenantID = *event.TenantId
	} else if data.TenantId != nil {
		tenantID = data.GetTenantId()
	}

	return &pb.StorageEvent{
		Id:             eventID,
		Timestamp:      timestamppb.New(timestamp),
		TenantId:       tenantID,
		StreamId:       streamID,
		AssetHash:      data.GetAssetHash(),
		Action:         action,
		AssetType:      data.GetAssetType(),
		SizeBytes:      data.GetSizeBytes(),
		S3Url:          stringPtr(data.GetS3Url()),
		LocalPath:      stringPtr(data.GetLocalPath()),
		NodeId:         data.GetNodeId(),
		DurationMs:     int64PtrIfNonZero(data.GetDurationMs()),
		WarmDurationMs: int64PtrIfNonZero(data.GetWarmDurationMs()),
		Error:          stringPtr(data.GetError()),
	}
}

func int64PtrIfNonZero(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}
