package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/domainevents"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// PublisherAdmissionRepository executes the publisher transitions in the cell
// database. Neither operation holds a SQL transaction across Redis or RPC I/O.
type PublisherAdmissionRepository struct{ database *sql.DB }

func NewPublisherAdmissionRepository(database *sql.DB) *PublisherAdmissionRepository {
	return &PublisherAdmissionRepository{database: database}
}

type PreparedPublisherAdmission struct {
	Generation          string               `json:"generation"`
	Outcome             IngestSessionOutcome `json:"outcome"`
	SourceRevision      int64                `json:"source_revision"`
	Resumed             bool                 `json:"resumed"`
	IncumbentGeneration string               `json:"incumbent_generation"`
	IncumbentNode       string               `json:"incumbent_node"`
	StopClaims          []DVRStopClaim       `json:"stop_claims"`
}

type preparePublisherRequest struct {
	DecisionExpiresAt  *time.Time               `json:"decision_expires_at,omitempty"`
	StrictLeaseOwner   bool                     `json:"strict_lease_owner"`
	TenantID           string                   `json:"tenant_id"`
	NodeID             string                   `json:"node_id"`
	InternalName       string                   `json:"internal_name"`
	StreamID           string                   `json:"stream_id"`
	ConnectorPID       int64                    `json:"connector_pid"`
	TriggerUUID        string                   `json:"trigger_uuid"`
	StartedAtMillis    int64                    `json:"started_at_millis"`
	DVRIntent          json.RawMessage          `json:"dvr_intent"`
	IngestClusterID    string                   `json:"ingest_cluster_id"`
	Authority          *IngestAuthoritySnapshot `json:"authority"`
	Generation         string                   `json:"generation"`
	AdmissionWindowMS  int64                    `json:"admission_window_ms"`
	TakeoverGeneration string                   `json:"takeover_generation"`
	ConnectedEvent     *events.Event            `json:"connected_event"`
	IdleEvent          *events.Event            `json:"idle_event"`
}

func (r *PublisherAdmissionRepository) Prepare(ctx context.Context, request IngestSessionRequest, _ logging.Logger) (prepared PreparedPublisherAdmission, resultErr error) {
	started := time.Now()
	defer func() { ObservePublisherAdmissionPhase("prepare", started, resultErr) }()
	if r == nil || r.database == nil {
		return PreparedPublisherAdmission{}, errors.New("publisher admission database unavailable")
	}
	if request.TenantID == "" || request.NodeID == "" || request.InternalName == "" || request.ConnectorPID <= 0 || request.TriggerUUID == "" || request.StartedAtMillis <= 0 {
		return PreparedPublisherAdmission{}, errors.New("publisher admission requires tenant and connector identity")
	}
	if snapshot := request.Authority; snapshot != nil {
		hasAuthority := snapshot.MediaAuthorityID != "" || snapshot.MediaAuthorityVersion != 0 || snapshot.TenantAuthorityVersion != 0
		if hasAuthority && (snapshot.MediaAuthorityID == "" || snapshot.MediaAuthorityVersion <= 0 || snapshot.TenantAuthorityVersion <= 0) || snapshot.CapacityMaxStreams < 0 {
			return PreparedPublisherAdmission{}, errors.New("publisher authority snapshot is incomplete")
		}
	}
	if _, err := uuid.Parse(request.StreamID); err != nil {
		return PreparedPublisherAdmission{}, errors.New("publisher admission requires a public stream UUID")
	}
	payload := preparePublisherRequest{
		TenantID: request.TenantID, NodeID: request.NodeID, InternalName: request.InternalName, StreamID: request.StreamID,
		ConnectorPID: request.ConnectorPID, TriggerUUID: request.TriggerUUID, StartedAtMillis: request.StartedAtMillis,
		DVRIntent: request.DVRIntent, IngestClusterID: request.IngestClusterID, Authority: request.Authority, StrictLeaseOwner: request.StrictLeaseOwner,
		Generation: uuid.NewString(), AdmissionWindowMS: IngestAdmissionWindow.Milliseconds(),
	}
	if !request.DecisionExpiresAt.IsZero() {
		expiry := request.DecisionExpiresAt.UTC()
		payload.DecisionExpiresAt = &expiry
	}
	connected, err := events.New(domainevents.Source, request.TenantID, request.StreamID,
		&publicv1.StreamConnected{StreamId: request.StreamID, PlaybackId: request.PlaybackID, Protocol: request.Protocol})
	if err != nil {
		return PreparedPublisherAdmission{}, err
	}
	idle, err := events.New(domainevents.Source, request.TenantID, request.StreamID,
		&publicv1.StreamIdle{StreamId: request.StreamID, PlaybackId: request.PlaybackID})
	if err != nil {
		return PreparedPublisherAdmission{}, err
	}
	payload.ConnectedEvent, payload.IdleEvent = &connected, &idle
	var guard ingestTakeoverGuard
	defer guard.releaseGuard()
	// Each takeover result names the incumbent observed by that transaction.
	// Recheck a bounded number of changing incumbents, then let a fresh edge
	// attempt resolve ownership rather than spending its deadline on churn.
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return PreparedPublisherAdmission{}, err
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return PreparedPublisherAdmission{}, err
		}
		var result PreparedPublisherAdmission
		err = database.RetryPostgres(ctx, 0, 0, func() error {
			raw, queryErr := foghorndb.New(r.database).PreparePublisherAdmission(ctx, encoded)
			if queryErr != nil {
				return queryErr
			}
			return json.Unmarshal(raw, &result)
		})
		if err != nil {
			return PreparedPublisherAdmission{}, fmt.Errorf("prepare publisher admission: %w", err)
		}
		if err := validatePreparedPublisher(result); err != nil {
			return PreparedPublisherAdmission{}, err
		}
		if result.Outcome != ingestSessionTakeoverRequired {
			QueueDVRStopClaims(result.StopClaims)
			return result, nil
		}
		if !guard.absent(ctx, result.IncumbentNode) {
			return PreparedPublisherAdmission{Outcome: IngestSessionRejectedDuplicate}, nil
		}
		payload.TakeoverGeneration = result.IncumbentGeneration
	}
	return PreparedPublisherAdmission{Outcome: IngestSessionRejectedDuplicate}, nil
}

// Project confirms only the generation that won the revisioned Redis CAS.
func (r *PublisherAdmissionRepository) Project(ctx context.Context, registry *StreamRegistry, request IngestSessionRequest, prepared PreparedPublisherAdmission, intent AdmissionEffectIntent) (applied bool, resumed bool, resultErr error) {
	if r == nil || r.database == nil || registry == nil || prepared.Generation == "" || prepared.SourceRevision <= 0 {
		return false, prepared.Resumed, errors.New("publisher projection has no prepared generation")
	}
	casStarted := time.Now()
	prior, priorGeneration, applied, err := registry.projectSourceWithPriorGenerationContext(ctx, request.InternalName, request.NodeID, request.ConnectorPID, request.TriggerUUID, prepared.Generation, prepared.SourceRevision)
	ObservePublisherAdmissionPhase("redis_cas", casStarted, err)
	if err != nil || !applied {
		if !prepared.Resumed || err != nil {
			return false, prepared.Resumed, errors.Join(errors.New("publisher source projection refused"), err)
		}
		repaired, repairErr := repairResumedSourceProjection(ctx, registry, request.TenantID, request.NodeID, request.InternalName, request.ConnectorPID, request.TriggerUUID, prepared.Generation)
		if repairErr != nil || !repaired {
			return false, true, errors.Join(errors.New("resumed publisher source repair refused"), repairErr)
		}
		current, readErr := foghorndb.New(r.database).GetPublisherAdmission(ctx, foghorndb.GetPublisherAdmissionParams{TenantID: request.TenantID, NodeID: request.NodeID, TriggerUuid: request.TriggerUUID})
		if readErr != nil {
			return false, true, readErr
		}
		if current.Generation != prepared.Generation || !current.SourceRevision.Valid || current.SourceRevision.Int64 <= prepared.SourceRevision {
			return false, true, errors.New("resumed publisher repair lost its generation revision")
		}
		prepared.SourceRevision = current.SourceRevision.Int64
	}
	if prepared.Resumed {
		// Confirmation still fences close/abandon, but must not replay outputs.
		intent = AdmissionEffectIntent{}
	}
	protectedTargets, err := protectAdmissionPushTargets(intent.PushTargets, request.TenantID, request.InternalName, prepared.Generation)
	if err != nil {
		return false, prepared.Resumed, err
	}
	var activation ipcpb.ActivatePushTargets
	if len(intent.PushTargets) > 0 {
		if err = proto.Unmarshal(intent.PushTargets, &activation); err != nil {
			return false, prepared.Resumed, err
		}
	}
	var peerClusters string
	if len(intent.PeerHints) > 0 {
		peers, marshalErr := json.Marshal(intent.PeerHints)
		if marshalErr != nil {
			return false, prepared.Resumed, marshalErr
		}
		peerClusters = string(peers)
	}
	payload := map[string]any{
		"tenant_id": request.TenantID, "node_id": request.NodeID, "internal_name": request.InternalName,
		"trigger_uuid": request.TriggerUUID, "generation": prepared.Generation, "source_revision": prepared.SourceRevision,
		"prior_node": prior, "prior_generation": priorGeneration, "push_targets": protectedTargets,
		"target_revision": activation.GetTargetRevision(), "activation_attempt": activation.GetActivationAttempt(),
		"broadcast_live": intent.BroadcastLive, "decklog_trigger": intent.DecklogTrigger, "peer_clusters": peerClusters, "resumed": prepared.Resumed,
	}
	if !request.DecisionExpiresAt.IsZero() {
		payload["decision_expires_at"] = request.DecisionExpiresAt.UTC()
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false, prepared.Resumed, err
	}
	var confirmed bool
	confirmStarted := time.Now()
	err = database.RetryPostgres(ctx, 0, 0, func() error {
		var queryErr error
		confirmed, queryErr = foghorndb.New(r.database).ConfirmPublisherAdmission(ctx, encoded)
		return queryErr
	})
	ObservePublisherAdmissionPhase("confirm", confirmStarted, err)
	if err != nil {
		return false, prepared.Resumed, fmt.Errorf("confirm publisher admission: %w", err)
	}
	if !confirmed {
		return false, prepared.Resumed, fmt.Errorf("publisher generation %s ended before confirmation", strings.TrimSpace(prepared.Generation))
	}
	return true, prepared.Resumed, nil
}

// Settle retires only the supplied execution generation and persists all
// resulting obligations before returning. Unknown commit outcomes are retried
// by reading the same durable execution, never by retiring a successor.
func (r *PublisherAdmissionRepository) Settle(ctx context.Context, request IngestSessionRequest, generation, reason string) (settled bool, resultErr error) {
	started := time.Now()
	defer func() { ObservePublisherAdmissionPhase("settle", started, resultErr) }()
	if r == nil || r.database == nil {
		return false, errors.New("publisher settlement database unavailable")
	}
	if reason != IngestEndedAdmissionAbandoned && reason != IngestEndedAdmissionLapsed {
		return false, errors.New("publisher settlement reason invalid")
	}
	var idle *events.Event
	if request.StreamID != "" {
		event, err := events.New(domainevents.Source, request.TenantID, request.StreamID, &publicv1.StreamIdle{StreamId: request.StreamID, PlaybackId: request.PlaybackID})
		if err != nil {
			return false, err
		}
		idle = &event
	}
	payload, err := json.Marshal(map[string]any{"tenant_id": request.TenantID, "node_id": request.NodeID, "internal_name": request.InternalName, "trigger_uuid": request.TriggerUUID, "generation": generation, "reason": reason, "admission_window_ms": IngestAdmissionWindow.Milliseconds(), "idle_event": idle})
	if err != nil {
		return false, err
	}
	err = database.RetryPostgres(ctx, 0, 0, func() error {
		var queryErr error
		settled, queryErr = foghorndb.New(r.database).SettlePublisherAdmission(ctx, payload)
		return queryErr
	})
	if err == nil && settled {
		QueueDVRStopsForGeneration(request.TenantID, generation)
	}
	return settled, err
}

func validatePreparedPublisher(prepared PreparedPublisherAdmission) error {
	switch prepared.Outcome {
	case IngestSessionActive:
		if _, err := uuid.Parse(prepared.Generation); err != nil || prepared.SourceRevision <= 0 {
			return errors.New("publisher prepare returned incomplete generation")
		}
	case ingestSessionTakeoverRequired:
		if _, err := uuid.Parse(prepared.IncumbentGeneration); err != nil || strings.TrimSpace(prepared.IncumbentNode) == "" {
			return errors.New("publisher prepare returned incomplete incumbent")
		}
	case IngestSessionAlreadyEnded, IngestSessionRejectedDuplicate, IngestSessionRejectedCapacity, IngestSessionAuthorityChanged:
	default:
		return fmt.Errorf("publisher prepare returned unknown outcome %d", prepared.Outcome)
	}
	return nil
}
