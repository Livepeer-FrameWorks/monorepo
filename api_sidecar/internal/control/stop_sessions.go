package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"frameworks/api_sidecar/internal/appconfig"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func handleConfirmedStopSessions(logger logging.Logger, req *ipcpb.StopSessionsRequest, connection *streamConn) {
	ctx, cancel := context.WithTimeout(connection.stream.Context(), 10*time.Second)
	defer cancel()
	err := stopSessionsEffectWithAuthorization(ctx, logger, req, func() bool { return stopSessionsNodeMatches(req, connection) }, func(ctx context.Context) (time.Duration, error) {
		return authorizeStopSessions(ctx, req, connection)
	})
	result := &ipcpb.StopSessionsResult{RequestId: req.GetRequestId(), TenantId: req.GetTenantId(), StreamNames: slices.Clone(req.GetStreamNames())}
	if err != nil {
		result.Error = err.Error()
		logger.WithError(err).WithField("tenant_id", req.GetTenantId()).Warn("Session termination remains pending")
	}
	if sendErr := sendStopSessionsMessage(ctx, connection, &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_StopSessionsResult{StopSessionsResult: result}}); sendErr != nil {
		logger.WithError(sendErr).Warn("Failed to acknowledge session termination")
	}
}

// Runtime generation locks cover validation, Mist's stop and verification. A
// replacement publisher cannot acquire the same runtime between those steps.
func stopSessionsEffect(ctx context.Context, logger logging.Logger, req *ipcpb.StopSessionsRequest, current func() bool) error {
	return stopSessionsEffectWithAuthorization(ctx, logger, req, current, nil)
}

func stopSessionsEffectWithAuthorization(ctx context.Context, logger logging.Logger, req *ipcpb.StopSessionsRequest, current func() bool, authorize func(context.Context) (time.Duration, error)) error {
	if len(req.GetStreamNames()) == 0 {
		return errors.New("empty session termination scope")
	}
	confirmed := req.GetRequestId() != ""
	if confirmed {
		if current == nil || !current() || req.GetTenantId() == "" {
			return errors.New("session termination connection or deadline is no longer current")
		}
		var cancel context.CancelFunc
		if req.GetRemainingTimeoutMs() == 0 || authorize == nil {
			return errors.New("session termination lacks a local budget or current-owner authorization")
		}
		ctx, cancel = context.WithTimeout(ctx, min(time.Duration(req.GetRemainingTimeoutMs())*time.Millisecond, 10*time.Second))
		defer cancel()
		remaining, err := authorize(ctx)
		if err != nil {
			return err
		}
		if remaining <= 0 || !current() || ctx.Err() != nil {
			return errors.New("session termination authorization is no longer current")
		}
		ctx, cancel = context.WithTimeout(ctx, remaining)
		defer cancel()
	}
	cfg := currentConfig
	if cfg == nil {
		return errors.New("config not initialized")
	}
	client := mist.NewClient(logger, appconfig.MistClient())
	if cfg.MistServerURL != "" {
		client.BaseURL = cfg.MistServerURL
	}
	live, err := client.ViewerSessions(ctx)
	if err != nil && confirmed {
		return fmt.Errorf("list sessions before termination: %w", err)
	}
	names := mist.SessionStreamNames(req.StreamNames, live)
	var held []*lockedIngestGenerationFence
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i].Unlock()
		}
	}()
	if confirmed {
		expected := make(map[string]string, len(req.Targets))
		for _, target := range req.Targets {
			name := target.GetInternalName()
			if name == "" || !slices.Contains(req.StreamNames, name) {
				return errors.New("session termination target is outside stream scope")
			}
			if _, duplicate := expected[name]; duplicate {
				return errors.New("duplicate session termination target")
			}
			expected[name] = target.GetSourceGeneration()
		}
		if len(expected) != len(req.StreamNames) {
			return errors.New("session termination is missing generation scope")
		}
		sort.Strings(names)
		for _, name := range names {
			fence, _ := lockIngestFence(name, true)
			held = append(held, fence)
			if fence.generation == "" && !fence.active {
				fence.evictPending = true
			}
			bare := mist.ExtractInternalName(name)
			generation, exists := expected[name]
			if !exists {
				generation, exists = expected[bare]
			}
			if !exists || (fence.active && (generation == "" || fence.generation != generation)) {
				return fmt.Errorf("publisher generation changed for %s", name)
			}
		}
		if !current() || ctx.Err() != nil {
			return errors.New("session termination expired before Mist dispatch")
		}
	}
	if sessions := currentPlaybackGrantSessions(); sessions != nil {
		sessions.StopStreams(req.StreamNames, req.GetReason())
	}
	if err := client.StopSessionsMultipleContext(ctx, names); err != nil {
		return fmt.Errorf("stop Mist sessions: %w", err)
	}
	if !confirmed {
		return nil
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !current() {
			return errors.New("session termination connection replaced")
		}
		stopped, verifyErr := client.SessionsStopped(ctx, names)
		if verifyErr != nil {
			return verifyErr
		}
		if stopped {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("mist sessions remain after termination: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func stopSessionsNodeMatches(req *ipcpb.StopSessionsRequest, connection *streamConn) bool {
	current := activeConn.Load()
	return current != nil && current.epoch == connection.epoch && strings.TrimSpace(req.GetNodeId()) != "" && req.GetNodeId() == current.nodeID
}
