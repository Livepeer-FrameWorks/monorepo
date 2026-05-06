package orchestrator

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

const desiredStateApplyTimeout = 15 * time.Minute

type MistUpdateRequest struct {
	NodeID        string
	ClusterID     string
	TargetRelease string
	Components    []*pb.DesiredComponent
	DrainDeadline time.Duration
	Force         bool
}

type DirectUpdateRequest struct {
	NodeID        string
	ClusterID     string
	TargetRelease string
	Components    []*pb.DesiredComponent
}

func ApplyDirectUpdate(ctx context.Context, req DirectUpdateRequest) error {
	if req.NodeID == "" {
		return fmt.Errorf("node_id required")
	}
	if len(req.Components) == 0 {
		return fmt.Errorf("components required")
	}
	progress, err := loadProgress(ctx, req.NodeID)
	if err != nil {
		return err
	}
	if progress.TargetRelease == req.TargetRelease && (progress.Phase == "updating" || progress.Phase == "warming") {
		if progress.Deadline.IsZero() {
			return persistPhase(ctx, req.NodeID, req.TargetRelease, progress.Phase, "", time.Now().Add(desiredStateApplyTimeout))
		}
		if deadlineExpired(progress.Deadline) {
			return persistPhase(ctx, req.NodeID, req.TargetRelease, "failed", "update apply result deadline reached", progress.Deadline)
		}
		return nil
	}
	deadline := time.Now().Add(desiredStateApplyTimeout)
	if err := persistPhase(ctx, req.NodeID, req.TargetRelease, "updating", "", deadline); err != nil {
		return err
	}
	if err := control.SendDesiredStateUpdate(req.NodeID, &pb.DesiredStateUpdate{
		ClusterId:     req.ClusterID,
		NodeId:        req.NodeID,
		TargetRelease: req.TargetRelease,
		Components:    req.Components,
	}); err != nil {
		return persistFailure(ctx, req.NodeID, req.TargetRelease, err, deadline)
	}
	return nil
}

func ApplyMistUpdate(ctx context.Context, req MistUpdateRequest) error {
	if req.NodeID == "" {
		return fmt.Errorf("node_id required")
	}
	if len(req.Components) == 0 {
		return fmt.Errorf("components required")
	}
	if req.DrainDeadline <= 0 {
		req.DrainDeadline = 4 * time.Hour
	}
	progress, err := loadProgress(ctx, req.NodeID)
	if err != nil {
		return err
	}
	if progress.TargetRelease != "" && progress.TargetRelease != req.TargetRelease && progress.Phase != "idle" && progress.Phase != "failed" {
		return nil
	}

	switch progress.Phase {
	case "", "idle", "failed":
		deadline := time.Now().Add(req.DrainDeadline)
		if err := persistPhase(ctx, req.NodeID, req.TargetRelease, "cordoning", "", deadline); err != nil {
			return err
		}
		if err := cordonNodeForUpdate(ctx, req.NodeID); err != nil {
			return persistFailure(ctx, req.NodeID, req.TargetRelease, err, time.Time{})
		}
		return persistPhase(ctx, req.NodeID, req.TargetRelease, "draining", "", deadline)
	case "cordoning":
		deadline := progress.Deadline
		if deadline.IsZero() {
			deadline = time.Now().Add(req.DrainDeadline)
		}
		if err := cordonNodeForUpdate(ctx, req.NodeID); err != nil {
			return persistFailure(ctx, req.NodeID, req.TargetRelease, err, deadline)
		}
		return persistPhase(ctx, req.NodeID, req.TargetRelease, "draining", "", deadline)
	case "draining":
		deadline := progress.Deadline
		if deadline.IsZero() {
			deadline = time.Now().Add(req.DrainDeadline)
		}
		if state.DefaultManager().GetNodeActiveStreams(req.NodeID) > 0 && time.Now().Before(deadline) {
			return nil
		}
		if state.DefaultManager().GetNodeActiveStreams(req.NodeID) > 0 && !req.Force {
			return persistPhase(ctx, req.NodeID, req.TargetRelease, "failed", "drain deadline reached", deadline)
		}
		if err := persistPhase(ctx, req.NodeID, req.TargetRelease, "drained", "", deadline); err != nil {
			return err
		}
		fallthrough
	case "drained":
		token, err := newCordonToken()
		if err != nil {
			return err
		}
		applyDeadline := time.Now().Add(desiredStateApplyTimeout)
		if err := persistPhaseWithExpected(ctx, req.NodeID, req.TargetRelease, "updating_restore", "", applyDeadline, expectedComponentVersions(req.Components)); err != nil {
			return err
		}
		if err := control.SendDesiredStateUpdate(req.NodeID, &pb.DesiredStateUpdate{
			ClusterId:            req.ClusterID,
			NodeId:               req.NodeID,
			TargetRelease:        req.TargetRelease,
			Components:           req.Components,
			CordonToken:          token,
			CordonTokenExpiresAt: timestamppb.New(applyDeadline),
		}); err != nil {
			return persistFailure(ctx, req.NodeID, req.TargetRelease, err, applyDeadline)
		}
		return nil
	case "updating", "updating_restore":
		if progress.Deadline.IsZero() {
			return persistPhase(ctx, req.NodeID, req.TargetRelease, progress.Phase, "", time.Now().Add(desiredStateApplyTimeout))
		}
		if deadlineExpired(progress.Deadline) {
			return persistPhase(ctx, req.NodeID, req.TargetRelease, "failed", "update apply result deadline reached", progress.Deadline)
		}
		return nil
	case "warming", "warming_restore":
		if progress.Deadline.IsZero() {
			return persistPhase(ctx, req.NodeID, req.TargetRelease, progress.Phase, "", time.Now().Add(90*time.Second))
		}
		expected := progress.ExpectedComponents
		if len(expected) == 0 {
			return persistPhase(ctx, req.NodeID, req.TargetRelease, "failed", "warmup expected components missing", progress.Deadline)
		}
		ready, _, err := control.CompleteUpdateWarmupIfReady(ctx, req.NodeID, req.TargetRelease, expected, progress.UpdatedAt, nil)
		if err != nil {
			_ = fenceNodeAfterUpdateFailure(ctx, req.NodeID)
			return persistFailure(ctx, req.NodeID, req.TargetRelease, err, progress.Deadline)
		}
		if ready {
			return nil
		}
		if deadlineExpired(progress.Deadline) {
			if err := fenceNodeAfterUpdateFailure(ctx, req.NodeID); err != nil {
				return persistFailure(ctx, req.NodeID, req.TargetRelease, err, progress.Deadline)
			}
			return persistPhase(ctx, req.NodeID, req.TargetRelease, "failed", "warmup deadline reached", progress.Deadline)
		}
		return nil
	default:
		return persistPhase(ctx, req.NodeID, req.TargetRelease, "failed", fmt.Sprintf("unknown update phase %q", progress.Phase), progress.Deadline)
	}
}

func cordonNodeForUpdate(ctx context.Context, nodeID string) error {
	if err := state.DefaultManager().SetNodeOperationalMode(ctx, nodeID, state.NodeModeDraining, "update-orchestrator"); err != nil {
		return err
	}
	return control.PushOperationalMode(nodeID, pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING)
}

func fenceNodeAfterUpdateFailure(ctx context.Context, nodeID string) error {
	if err := state.DefaultManager().SetNodeOperationalMode(ctx, nodeID, state.NodeModeMaintenance, "update-orchestrator"); err != nil {
		return err
	}
	return control.PushOperationalMode(nodeID, pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE)
}

func persistFailure(ctx context.Context, nodeID, targetRelease string, cause error, deadline time.Time) error {
	if err := persistPhase(ctx, nodeID, targetRelease, "failed", cause.Error(), deadline); err != nil {
		return fmt.Errorf("%w; failed to persist update failure: %w", cause, err)
	}
	return cause
}

func deadlineExpired(deadline time.Time) bool {
	return !deadline.IsZero() && !time.Now().Before(deadline)
}

type updateProgress struct {
	TargetRelease      string
	Phase              string
	Deadline           time.Time
	UpdatedAt          time.Time
	ExpectedComponents map[string]string
}

func loadProgress(ctx context.Context, nodeID string) (updateProgress, error) {
	db := control.GetDB()
	if db == nil {
		return updateProgress{}, nil
	}
	var progress updateProgress
	var deadline sql.NullTime
	var updatedAt sql.NullTime
	var expectedRaw string
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(target_release, ''), phase, deadline, updated_at, COALESCE(expected_components::text, '{}')
		FROM foghorn.node_update_state
		WHERE node_id = $1
	`, nodeID).Scan(&progress.TargetRelease, &progress.Phase, &deadline, &updatedAt, &expectedRaw)
	if err == sql.ErrNoRows {
		return updateProgress{}, nil
	}
	if err != nil {
		return updateProgress{}, err
	}
	if deadline.Valid {
		progress.Deadline = deadline.Time
	}
	if updatedAt.Valid {
		progress.UpdatedAt = updatedAt.Time
	}
	if strings.TrimSpace(expectedRaw) != "" {
		if err := json.Unmarshal([]byte(expectedRaw), &progress.ExpectedComponents); err != nil {
			return updateProgress{}, fmt.Errorf("parse expected components for %s: %w", nodeID, err)
		}
	}
	return progress, nil
}

func persistPhase(ctx context.Context, nodeID, targetRelease, phase, lastError string, deadline time.Time) error {
	return persistPhaseWithExpected(ctx, nodeID, targetRelease, phase, lastError, deadline, nil)
}

func persistPhaseWithExpected(ctx context.Context, nodeID, targetRelease, phase, lastError string, deadline time.Time, expected map[string]string) error {
	db := control.GetDB()
	if db == nil {
		return nil
	}
	deadlineArg := any(nil)
	if !deadline.IsZero() {
		deadlineArg = deadline
	}
	expectedArg := any(nil)
	if len(expected) > 0 {
		encoded, err := json.Marshal(expected)
		if err != nil {
			return err
		}
		expectedArg = string(encoded)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.node_update_state (node_id, target_release, phase, started_at, deadline, expected_components, last_error, updated_at)
		VALUES ($1, NULLIF($2, ''), $3, NOW(), $5, COALESCE($6::jsonb, '{}'::jsonb), NULLIF($4, ''), NOW())
		ON CONFLICT (node_id) DO UPDATE SET
			target_release = EXCLUDED.target_release,
			phase = EXCLUDED.phase,
			started_at = COALESCE(foghorn.node_update_state.started_at, EXCLUDED.started_at),
			deadline = COALESCE(EXCLUDED.deadline, foghorn.node_update_state.deadline),
			expected_components = CASE
				WHEN $6::jsonb IS NULL THEN foghorn.node_update_state.expected_components
				ELSE EXCLUDED.expected_components
			END,
			last_error = EXCLUDED.last_error,
			updated_at = NOW()
	`, nodeID, targetRelease, phase, lastError, deadlineArg, expectedArg)
	return err
}

func newCordonToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func expectedComponentVersions(components []*pb.DesiredComponent) map[string]string {
	expected := make(map[string]string)
	for _, component := range components {
		if component == nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(component.GetComponent()))
		version := strings.TrimSpace(component.GetVersion())
		if name != "" && version != "" {
			expected[name] = version
		}
	}
	return expected
}
