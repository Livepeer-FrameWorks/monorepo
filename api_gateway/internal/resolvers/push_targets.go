package resolvers

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/globalid"
	pb "frameworks/pkg/proto"
)

// DoGetStreamPushTargets returns push targets for a stream (Stream.pushTargets field resolver).
func (r *Resolver) DoGetStreamPushTargets(ctx context.Context, streamID string) ([]*pb.PushTarget, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GeneratePushTargets(streamID), nil
	}

	resp, err := r.Clients.Commodore.ListPushTargets(ctx, streamID)
	if err != nil {
		r.Logger.WithError(err).WithField("stream_id", streamID).Error("Failed to list push targets")
		return nil, fmt.Errorf("failed to list push targets: %w", err)
	}

	return resp.GetPushTargets(), nil
}

// DoCreatePushTarget creates a new multistream push target.
func (r *Resolver) DoCreatePushTarget(ctx context.Context, streamID string, input model.CreatePushTargetInput) (*pb.PushTarget, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}

	if middleware.IsDemoMode(ctx) {
		return nil, fmt.Errorf("push target creation not available in demo mode")
	}

	req := &pb.CreatePushTargetRequest{
		StreamId:  streamID,
		Name:      input.Name,
		TargetUri: input.TargetURI,
	}
	if input.Platform != nil {
		req.Platform = *input.Platform
	}

	target, err := r.Clients.Commodore.CreatePushTarget(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create push target")
		return nil, fmt.Errorf("failed to create push target: %w", err)
	}

	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventPushTargetCreated,
		ResourceType: "push_target",
		ResourceId:   target.GetId(),
		Payload: &pb.ServiceEvent_StreamChangeEvent{
			StreamChangeEvent: &pb.StreamChangeEvent{
				StreamId:      streamID,
				ChangedFields: []string{"push_targets"},
			},
		},
	})

	return target, nil
}

// DoUpdatePushTarget updates a multistream push target.
func (r *Resolver) DoUpdatePushTarget(ctx context.Context, id string, input model.UpdatePushTargetInput) (*pb.PushTarget, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}

	if middleware.IsDemoMode(ctx) {
		return nil, fmt.Errorf("push target update not available in demo mode")
	}

	rawID, err := globalid.DecodeExpected(id, globalid.TypePushTarget)
	if err != nil {
		return nil, fmt.Errorf("invalid push target ID: %w", err)
	}

	req := &pb.UpdatePushTargetRequest{
		Id: rawID,
	}

	var changedFields []string
	if input.Name != nil {
		req.Name = input.Name
		changedFields = append(changedFields, "name")
	}
	if input.TargetURI != nil {
		req.TargetUri = input.TargetURI
		changedFields = append(changedFields, "target_uri")
	}
	if input.IsEnabled != nil {
		req.IsEnabled = input.IsEnabled
		changedFields = append(changedFields, "is_enabled")
	}

	target, err := r.Clients.Commodore.UpdatePushTarget(ctx, req)
	if err != nil {
		r.Logger.WithError(err).WithField("push_target_id", id).Error("Failed to update push target")
		return nil, fmt.Errorf("failed to update push target: %w", err)
	}

	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventPushTargetUpdated,
		ResourceType: "push_target",
		ResourceId:   id,
		Payload: &pb.ServiceEvent_StreamChangeEvent{
			StreamChangeEvent: &pb.StreamChangeEvent{
				StreamId:      target.GetStreamId(),
				ChangedFields: changedFields,
			},
		},
	})

	return target, nil
}

// DoDeletePushTarget deletes a multistream push target.
func (r *Resolver) DoDeletePushTarget(ctx context.Context, id string) (*model.DeleteSuccess, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}

	if middleware.IsDemoMode(ctx) {
		return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
	}

	rawID, err := globalid.DecodeExpected(id, globalid.TypePushTarget)
	if err != nil {
		return nil, fmt.Errorf("invalid push target ID: %w", err)
	}

	_, err = r.Clients.Commodore.DeletePushTarget(ctx, rawID)
	if err != nil {
		r.Logger.WithError(err).WithField("push_target_id", id).Error("Failed to delete push target")
		if strings.Contains(err.Error(), "not found") {
			return nil, fmt.Errorf("push target not found")
		}
		return nil, fmt.Errorf("failed to delete push target: %w", err)
	}

	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventPushTargetDeleted,
		ResourceType: "push_target",
		ResourceId:   id,
	})

	return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
}
