package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornrelaypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_relay"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func ProcessingMemoryReservationSnapshot(ctx context.Context, nodeID string) (*ipcpb.ProcessingMemoryReservation, error) {
	if db == nil {
		return nil, nil
	}
	row, err := foghorndb.New(db).GetProcessingMemoryReservation(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return &ipcpb.ProcessingMemoryReservation{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !row.JobID.Valid && row.RequiredMemoryBytes > 0 {
		return nil, nil
	}
	return &ipcpb.ProcessingMemoryReservation{JobId: row.JobID.String, Bytes: uint64(row.RequiredMemoryBytes), Version: uint64(row.Revision)}, nil
}

func processingReservationForSeed(nodeID string) *ipcpb.ProcessingMemoryReservation {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	protection, err := ProcessingMemoryReservationSnapshot(ctx, nodeID)
	if err != nil {
		logging.NewLogger().WithError(err).WithField("node_id", nodeID).Warn("Processing protection snapshot unavailable; preserving edge state")
		return nil
	}
	return protection
}

func SendLocalProcessingMemoryReservation(nodeID string, reservation *ipcpb.ProcessingMemoryReservation) error {
	if reservation == nil {
		return fmt.Errorf("nil processing memory reservation")
	}
	if registry == nil {
		return ErrNotConnected
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	c.sendGate.Lock()
	defer c.sendGate.Unlock()
	if c.superseded.Load() || !connIsCurrentlyRegistered(nodeID, c) {
		return ErrConnSuperseded
	}
	return c.stream.Send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ProcessingMemoryReservation{ProcessingMemoryReservation: reservation}, SentAt: timestamppb.Now()})
}

func SendProcessingMemoryReservation(ctx context.Context, nodeID string, reservation *ipcpb.ProcessingMemoryReservation) error {
	err := SendLocalProcessingMemoryReservation(nodeID, reservation)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil || reservation == nil {
		return ErrNotConnected
	}
	return relayFailure(err, commandRelay.forward(ctx, &foghornrelaypb.ForwardCommandRequest{TargetNodeId: nodeID, Command: &foghornrelaypb.ForwardCommandRequest_ProcessingMemoryReservation{ProcessingMemoryReservation: reservation}}))
}
