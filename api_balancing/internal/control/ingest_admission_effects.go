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
	fieldcrypto "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

var admissionEffectEncryptor *fieldcrypto.FieldEncryptor

const (
	admissionStatePendingV2    = "pending_v2"
	admissionStateAppliedV2    = "applied_v2"
	admissionStateSupersededV2 = "superseded_v2"
)

func ConfigureAdmissionEffectEncryption(secret string) error {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return errors.New("FOGHORN_STATE_ENCRYPTION_KEY is required")
	}
	encryptor, err := fieldcrypto.DeriveFieldEncryptor([]byte(secret), "foghorn-ingest-admission-push-targets-v1")
	if err != nil {
		return err
	}
	admissionEffectEncryptor = encryptor
	return nil
}

func admissionEffectAAD(tenantID, internalName, generation string) ([]byte, error) {
	tenantUUID, err := uuid.Parse(strings.TrimSpace(tenantID))
	if err != nil {
		return nil, fmt.Errorf("invalid admission tenant_id: %w", err)
	}
	generationUUID, err := uuid.Parse(strings.TrimSpace(generation))
	if err != nil {
		return nil, fmt.Errorf("invalid admission source_generation: %w", err)
	}
	return []byte("foghorn:ingest-admission:push-targets\x00" + tenantUUID.String() + "\x00" + strings.TrimSpace(internalName) + "\x00" + generationUUID.String()), nil
}

func protectAdmissionPushTargets(raw []byte, tenantID, internalName, generation string) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	if admissionEffectEncryptor == nil {
		return nil, errors.New("admission effect encryption is not configured")
	}
	aad, err := admissionEffectAAD(tenantID, internalName, generation)
	if err != nil {
		return nil, err
	}
	protected, err := admissionEffectEncryptor.EncryptWithAAD(string(raw), aad)
	return []byte(protected), err
}

func openAdmissionPushTargets(stored []byte, tenantID, internalName, generation string, requireV2 ...bool) ([]byte, error) {
	if len(stored) == 0 {
		return stored, nil
	}
	if admissionEffectEncryptor == nil {
		return nil, errors.New("admission effect encryption is not configured")
	}
	aad, err := admissionEffectAAD(tenantID, internalName, generation)
	if err != nil {
		return nil, err
	}
	format := fieldcrypto.CiphertextFormat(string(stored))
	strict := len(requireV2) > 0 && requireV2[0]
	var opened string
	if strict {
		opened, err = admissionEffectEncryptor.DecryptWithAADStrict(string(stored), aad)
	} else {
		opened, err = admissionEffectEncryptor.DecryptWithAAD(string(stored), aad)
	}
	result := "opened"
	if err != nil {
		result = "error"
	}
	incAdmissionPayloadCrypto(string(format), result)
	return []byte(opened), err
}

// AdmissionEffectIntent describes the once-only external admission effects owed to a freshly
// admitted generation: push-target activation (a serialized ipcpb.ActivatePushTargets, nil when the
// stream has no targets), the federation live broadcast, and the admission's Decklog ingest event
// (a serialized, enriched ipcpb.MistTrigger stamped with the deterministic event_id). The
// prior-owner drain is not part of the intent — it is discovered by the registry CAS at
// confirmation time and persisted onto the obligation row there. The intent is persisted in the
// SAME transaction that confirms the source projection, so no trigger goroutine owns completion.
type AdmissionEffectIntent struct {
	PushTargets    []byte
	BroadcastLive  bool
	DecklogTrigger []byte
	// PeerHints is the complete admission-resolved federation peer set. Address and lifecycle are
	// persisted with the cluster id so a delayed or replacement leader can establish tracking from
	// the obligation alone.
	PeerHints []AdmissionPeerHint
}

type AdmissionPeerHint struct {
	ClusterID string `json:"cluster_id"`
	Addr      string `json:"addr"`
	AlwaysOn  bool   `json:"always_on,omitempty"`
	// ControlCellID is the peer's control cell from Quartermaster's routing
	// enrichment, so cross-cell placement can address a cell from the
	// demand-driven hint without waiting for the leader's periodic refresh.
	ControlCellID string `json:"control_cell_id,omitempty"`
}

// AdmissionEffect is one leased durable admission obligation. The per-leg done flags reflect the
// row at claim time; remote legs (drain, activation) are completed by Helmsman's correlated
// acknowledgements, local legs (broadcast, Decklog) by the apply callback.
type AdmissionEffect struct {
	ID                         int64
	TenantID                   string
	InternalName               string
	NodeID                     string
	SourceGeneration           string
	SourceRevision             int64
	PriorOwnerNodeID           string
	PriorOwnerSourceGeneration string
	PushTargets                []byte
	TargetRevision             int64
	CapacityPending            bool
	BroadcastLive              bool
	DecklogTrigger             []byte
	PeerHints                  []AdmissionPeerHint
	// PeerHintsInvalid: the persisted peer set could not be decoded. The broadcast leg treats
	// this as PER-LEG POISON (settled with diagnostics), never as "broadcast without tracking".
	PeerHintsInvalid bool
	// ActivationPayloadInvalid isolates an undecryptable durable target set to
	// this obligation's activation leg. Other rows and legs still converge.
	ActivationPayloadInvalid bool
	// EncryptedState means the row belongs to the v2 state family. Old Foghorn
	// workers ignore this family during rolling upgrades.
	EncryptedState bool
	DrainDone      bool
	ActivationDone bool
	BroadcastDone  bool
	DecklogDone    bool
	// GenerationEnded is resolved under the stream lock during apply: when true, the activation,
	// broadcast AND drain legs are moot (a dead generation must not be announced, start pushes, or
	// nuke a runtime name a successor session may now own); only the Decklog leg remains owed.
	GenerationEnded bool
	LeaseToken      string
}

// AdmissionPushTargetIdentity is the restart-safe attribution retained in the
// encrypted admission obligation for a restream status report.
type AdmissionPushTargetIdentity struct {
	TenantID          string
	InternalName      string
	NodeID            string
	StreamID          string
	SourceGeneration  string
	TargetRevision    int64
	ActivationAttempt string
	LatestRevision    int64
	TargetID          string
	Platform          string
	MistPushID        int64
	MaxViewers        int32
	SourceLive        bool
}

const legacyAdmissionActivationAttempt = "00000000-0000-0000-0000-000000000000"

func normalizeAdmissionActivationAttempt(value string) string {
	value = strings.TrimSpace(value)
	if value == legacyAdmissionActivationAttempt {
		return ""
	}
	return value
}

func ResolveAdmissionPushTargetIdentity(ctx context.Context, tenantID, sourceGeneration string, targetRevision int64, activationAttempt, targetID string) (AdmissionPushTargetIdentity, error) {
	if db == nil {
		return AdmissionPushTargetIdentity{}, errors.New("admission database is unavailable")
	}
	row, err := foghorndb.New(db).GetAdmissionPushTargetsForStatus(ctx, foghorndb.GetAdmissionPushTargetsForStatusParams{
		TenantID: tenantID, SourceGeneration: sourceGeneration, TargetRevision: targetRevision, ActivationAttempt: activationAttempt,
	})
	if err != nil {
		return AdmissionPushTargetIdentity{}, err
	}
	opened, err := openAdmissionPushTargets(row.PushTargets, row.TenantID, row.StreamInternalName, sourceGeneration)
	if err != nil {
		return AdmissionPushTargetIdentity{}, fmt.Errorf("open admission push targets: %w", err)
	}
	var activation ipcpb.ActivatePushTargets
	if err := proto.Unmarshal(opened, &activation); err != nil {
		return AdmissionPushTargetIdentity{}, fmt.Errorf("decode admission push targets: %w", err)
	}
	rowAttempt := normalizeAdmissionActivationAttempt(row.ActivationAttempt)
	if strings.TrimSpace(activation.GetActivationAttempt()) != rowAttempt {
		return AdmissionPushTargetIdentity{}, errors.New("durable restream activation attempt does not match its protected payload")
	}
	mistPushIDs := map[string]int64{}
	if len(row.MistPushIds) > 0 {
		if err := json.Unmarshal(row.MistPushIds, &mistPushIDs); err != nil {
			return AdmissionPushTargetIdentity{}, fmt.Errorf("decode admission Mist push identities: %w", err)
		}
	}
	for _, target := range activation.GetTargets() {
		if target.GetTargetId() != targetID {
			continue
		}
		return AdmissionPushTargetIdentity{
			TenantID: row.TenantID, InternalName: row.StreamInternalName, NodeID: row.NodeID,
			StreamID: activation.GetStreamId(), SourceGeneration: sourceGeneration,
			TargetRevision: row.TargetRevision, LatestRevision: row.LatestTargetRevision,
			ActivationAttempt: rowAttempt,
			TargetID:          target.GetTargetId(), Platform: target.GetPlatform(), MistPushID: mistPushIDs[target.GetTargetId()],
			MaxViewers: activation.GetMaxViewers(), SourceLive: row.SourceLive,
		}, nil
	}
	return AdmissionPushTargetIdentity{}, sql.ErrNoRows
}

// ResolveAdmissionPushTargetsForTeardown restores the exact desired-set
// snapshot behind a deactivation acknowledgement. It is used when the
// non-durable status message was lost or Helmsman restarted before teardown.
func ResolveAdmissionPushTargetsForTeardown(ctx context.Context, nodeID, sourceGeneration string, targetRevision int64, attempt ...string) ([]AdmissionPushTargetIdentity, error) {
	if db == nil {
		return nil, errors.New("admission database is unavailable")
	}
	activationAttempt := ""
	if len(attempt) > 0 {
		activationAttempt = strings.TrimSpace(attempt[0])
	}
	row, err := foghorndb.New(db).GetAdmissionPushTargetsForTeardown(ctx, foghorndb.GetAdmissionPushTargetsForTeardownParams{
		SourceGeneration: sourceGeneration, NodeID: nodeID, TargetRevision: targetRevision, ActivationAttempt: activationAttempt,
	})
	if err != nil {
		return nil, err
	}
	opened, err := openAdmissionPushTargets(row.PushTargets, row.TenantID, row.StreamInternalName, sourceGeneration)
	if err != nil {
		return nil, fmt.Errorf("open teardown admission push targets: %w", err)
	}
	var activation ipcpb.ActivatePushTargets
	if err := proto.Unmarshal(opened, &activation); err != nil {
		return nil, fmt.Errorf("decode teardown admission push targets: %w", err)
	}
	rowAttempt := normalizeAdmissionActivationAttempt(row.ActivationAttempt)
	if strings.TrimSpace(activation.GetActivationAttempt()) != rowAttempt {
		return nil, errors.New("durable teardown activation attempt does not match its protected payload")
	}
	mistPushIDs := map[string]int64{}
	if len(row.MistPushIds) > 0 {
		if err := json.Unmarshal(row.MistPushIds, &mistPushIDs); err != nil {
			return nil, fmt.Errorf("decode teardown Mist push identities: %w", err)
		}
	}
	identities := make([]AdmissionPushTargetIdentity, 0, len(activation.GetTargets()))
	for _, target := range activation.GetTargets() {
		identities = append(identities, AdmissionPushTargetIdentity{
			TenantID: row.TenantID, InternalName: row.StreamInternalName, NodeID: row.NodeID,
			StreamID: activation.GetStreamId(), SourceGeneration: sourceGeneration,
			TargetRevision: row.TargetRevision, ActivationAttempt: rowAttempt,
			TargetID: target.GetTargetId(), Platform: target.GetPlatform(), MistPushID: mistPushIDs[target.GetTargetId()],
			MaxViewers: activation.GetMaxViewers(), SourceLive: false,
		})
	}
	return identities, nil
}

// BindAdmissionPushTargetMistID durably binds the node-local Mist process to
// the immutable desired-set snapshot before an activation can settle.
func BindAdmissionPushTargetMistID(ctx context.Context, nodeID, sourceGeneration string, targetRevision int64, activationAttempt, targetID string, mistPushID int64) error {
	if db == nil || mistPushID <= 0 {
		return nil
	}
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(sourceGeneration) == "" || strings.TrimSpace(activationAttempt) == "" || strings.TrimSpace(targetID) == "" || targetRevision <= 0 {
		return errors.New("bind admission Mist push identity requires node, generation, revision, attempt, target, and positive Mist id")
	}
	n, err := foghorndb.New(db).BindAdmissionPushTargetMistID(ctx, foghorndb.BindAdmissionPushTargetMistIDParams{
		TargetID: targetID, MistPushID: mistPushID, NodeID: nodeID,
		SourceGeneration: sourceGeneration, TargetRevision: targetRevision, ActivationAttempt: activationAttempt,
	})
	if err != nil {
		return fmt.Errorf("bind admission Mist push identity: %w", err)
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// BindAdmissionPushTargetMistIDIfAbsent claims an unbound durable identity
// without overwriting a replacement process that won the race. The bool is
// true only when this call installed the supplied Mist ID.
func BindAdmissionPushTargetMistIDIfAbsent(ctx context.Context, nodeID, sourceGeneration string, targetRevision int64, activationAttempt, targetID string, mistPushID int64) (bool, error) {
	if db == nil {
		return false, errors.New("admission database is unavailable")
	}
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(sourceGeneration) == "" || strings.TrimSpace(activationAttempt) == "" || strings.TrimSpace(targetID) == "" || targetRevision <= 0 || mistPushID <= 0 {
		return false, errors.New("bind absent admission Mist push identity requires node, generation, revision, attempt, target, and positive Mist id")
	}
	n, err := foghorndb.New(db).BindAdmissionPushTargetMistIDIfAbsent(ctx, foghorndb.BindAdmissionPushTargetMistIDIfAbsentParams{
		TargetID: targetID, MistPushID: mistPushID, NodeID: nodeID,
		SourceGeneration: sourceGeneration, TargetRevision: targetRevision, ActivationAttempt: activationAttempt,
	})
	if err != nil {
		return false, fmt.Errorf("bind absent admission Mist push identity: %w", err)
	}
	return n == 1, nil
}

// RecordAdmissionPushTargetDispatch stores the exact capacity-admitted subset
// for this attempt. The effect row retains the complete authority payload for a
// later capacity re-arm; the revision snapshot is the restart-safe record of
// what the node was actually asked to converge.
func RecordAdmissionPushTargetDispatch(ctx context.Context, tenantID, internalName, sourceGeneration string, activation *ipcpb.ActivatePushTargets) error {
	if db == nil {
		return errors.New("admission database is unavailable")
	}
	if activation == nil || strings.TrimSpace(sourceGeneration) == "" || activation.GetTargetRevision() <= 0 || strings.TrimSpace(activation.GetActivationAttempt()) == "" {
		return errors.New("record push-target dispatch requires generation, revision, and activation attempt")
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(activation)
	if err != nil {
		return fmt.Errorf("encode dispatched push-target subset: %w", err)
	}
	protected, err := protectAdmissionPushTargets(raw, tenantID, internalName, sourceGeneration)
	if err != nil {
		return fmt.Errorf("protect dispatched push-target subset: %w", err)
	}
	n, err := foghorndb.New(db).RecordAdmissionPushTargetDispatch(ctx, foghorndb.RecordAdmissionPushTargetDispatchParams{
		PushTargets: protected, SourceGeneration: sourceGeneration, TargetRevision: activation.GetTargetRevision(), ActivationAttempt: activation.GetActivationAttempt(),
	})
	if err != nil {
		return fmt.Errorf("record dispatched push-target subset: %w", err)
	}
	if n != 1 {
		return errors.New("record dispatched push-target subset: activation attempt is no longer current")
	}
	return nil
}

// ResolveAdmissionTargetRevision maps a legacy revision-less acknowledgement
// to the latest durable desired-set revision for its exact generation/node.
func ResolveAdmissionTargetRevision(ctx context.Context, nodeID, sourceGeneration string) (int64, error) {
	if db == nil {
		return 0, errors.New("admission database is unavailable")
	}
	revision, err := foghorndb.New(db).GetAdmissionTargetRevision(ctx, foghorndb.GetAdmissionTargetRevisionParams{
		SourceGeneration: sourceGeneration, NodeID: nodeID,
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}

// IsAdmissionGenerationLive is the durable liveness check used by capacity
// renewal. Process-local target tracking may outlive a missed teardown status,
// but an ended ingest_sessions row is authoritative and must stop renewal.
func IsAdmissionGenerationLive(ctx context.Context, tenantID, internalName, sourceGeneration string) (bool, error) {
	if db == nil {
		return false, errors.New("admission database is unavailable")
	}
	row, err := foghorndb.New(db).ProbeCurrentSourceProjection(ctx, foghorndb.ProbeCurrentSourceProjectionParams{
		TenantID: tenantID, StreamInternalName: internalName, Generation: sourceGeneration,
	})
	if err != nil {
		return false, err
	}
	return row.IsCurrent, nil
}

// AdmissionEffectFence identifies one ended federation membership watermark considered for
// cleanup. A fence is purgeable only after every admission callback at or below its revision has
// left the pending state.
type AdmissionEffectFence struct {
	TenantID       string `json:"tenant_id"`
	InternalName   string `json:"internal_name"`
	SourceRevision int64  `json:"source_revision"`
}

// AdmissionEffectLegResults reports what the apply callback settled this pass. Remote legs are
// never completed by the callback — dispatch is not completion. Deferred means at least one owed
// leg was skipped because this replica lacks its authority (node-connection owner for activation,
// PeerManager leader for the broadcast); the worker releases the lease neutrally so the
// authoritative replica picks the obligation up on its own tick. Poisoned legs (undecodable
// durable payload — no retry can succeed) are settled with diagnostics accumulated in PoisonNote,
// leaving unrelated valid legs to converge.
type AdmissionEffectLegResults struct {
	BroadcastDone bool
	DecklogDone   bool
	// ActivationDone settles a diagnosed, non-retryable dispatch block while
	// retaining the encrypted target payload. A later capable node reconnect
	// re-arms that exact set. ActivationPoisoned is reserved for payloads that
	// can never be replayed and therefore must be cleared.
	ActivationDone     bool
	ActivationPoisoned bool
	BroadcastPoisoned  bool
	Deferred           bool
	// AuthorityInstance is the instance that owns the deferred leg's authority (resolved by the
	// callback at the defer point: the node's connection owner or the federation leader); the
	// worker records it as the row's claim affinity. Empty when unresolvable — plain hand-back.
	AuthorityInstance string
	PoisonNote        string
	// CapacityPending is set only when this pass evaluated target capacity.
	// A nil value preserves the prior durable state after unrelated failures.
	CapacityPending *bool
}

func enqueueAdmissionEffectTx(ctx context.Context, tx *sql.Tx, tenantID, internalName, nodeID, generation string, revision int64, priorOwnerNodeID, priorOwnerSourceGeneration string, intent AdmissionEffectIntent) error {
	if revision <= 0 {
		return fmt.Errorf("enqueue admission effect requires positive source revision")
	}
	protectedPushTargets, err := protectAdmissionPushTargets(intent.PushTargets, tenantID, internalName, generation)
	if err != nil {
		return fmt.Errorf("protect admission push targets: %w", err)
	}
	var targetRevision int64
	var activationAttempt string
	if len(intent.PushTargets) > 0 {
		var activation ipcpb.ActivatePushTargets
		if unmarshalErr := proto.Unmarshal(intent.PushTargets, &activation); unmarshalErr == nil {
			targetRevision = activation.GetTargetRevision()
			activationAttempt = activation.GetActivationAttempt()
		}
	}
	// Legs that do not apply to this obligation are born complete.
	var peerClusters sql.NullString
	if len(intent.PeerHints) > 0 {
		raw, marshalErr := json.Marshal(intent.PeerHints)
		if marshalErr != nil {
			return fmt.Errorf("serialize admission peer clusters: %w", marshalErr)
		}
		peerClusters = sql.NullString{String: string(raw), Valid: true}
	}
	queries := foghorndb.New(tx)
	err = queries.EnqueueAdmissionEffect(ctx, foghorndb.EnqueueAdmissionEffectParams{
		TenantID: tenantID, StreamInternalName: internalName, NodeID: nodeID, SourceGeneration: generation,
		SourceRevision: revision, PriorOwnerNodeID: priorOwnerNodeID, PriorOwnerSourceGeneration: priorOwnerSourceGeneration,
		PushTargets: protectedPushTargets, TargetRevision: targetRevision,
		BroadcastLive: intent.BroadcastLive, DecklogTrigger: intent.DecklogTrigger,
		PeerClusters:   peerClusters,
		DrainDone:      strings.TrimSpace(priorOwnerNodeID) == "" || strings.TrimSpace(priorOwnerNodeID) == nodeID || strings.TrimSpace(priorOwnerSourceGeneration) == "",
		ActivationDone: len(intent.PushTargets) == 0, BroadcastDone: !intent.BroadcastLive, DecklogDone: len(intent.DecklogTrigger) == 0,
	})
	if err != nil {
		return fmt.Errorf("enqueue admission effect: %w", err)
	}
	if len(protectedPushTargets) > 0 && targetRevision > 0 {
		if err := queries.StoreAdmissionPushTargetRevision(ctx, foghorndb.StoreAdmissionPushTargetRevisionParams{
			TenantID: tenantID, StreamInternalName: internalName, NodeID: nodeID,
			SourceGeneration: generation, TargetRevision: targetRevision, ActivationAttempt: activationAttempt, PushTargets: protectedPushTargets,
		}); err != nil {
			return fmt.Errorf("store admission push-target revision: %w", err)
		}
	}
	return nil
}

// ClaimAdmissionEffects leases due admission obligations across HA replicas. A worker that dies
// loses only its lease; the row remains pending and another worker re-drives the owed legs.
// Cell-scoped administrative scan over Foghorn's own schema (the tenant-filter rule's documented
// exception): each claimed row carries its tenant_id, which scopes the advisory lock and every
// effect applied under it.
func ClaimAdmissionEffects(ctx context.Context, limit int, lease time.Duration, instanceID string) ([]AdmissionEffect, error) {
	if db == nil || limit <= 0 {
		return nil, nil
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	rows, err := foghorndb.New(db).ClaimAdmissionEffects(ctx, foghorndb.ClaimAdmissionEffectsParams{
		InstanceID: sql.NullString{String: instanceID, Valid: instanceID != ""}, LeaseMs: lease.Milliseconds(), RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("claim admission effects: %w", err)
	}
	out := make([]AdmissionEffect, 0, len(rows))
	for _, row := range rows {
		e := AdmissionEffect{
			ID: row.ID, TenantID: row.TenantID, InternalName: row.StreamInternalName, NodeID: row.NodeID,
			SourceGeneration: row.SourceGeneration, SourceRevision: row.SourceRevision,
			PriorOwnerNodeID: row.PriorOwnerNodeID, PriorOwnerSourceGeneration: row.PriorOwnerSourceGeneration,
			TargetRevision: row.TargetRevision, CapacityPending: row.CapacityPending, BroadcastLive: row.BroadcastLive, DecklogTrigger: row.DecklogTrigger,
			DrainDone: row.DrainDone, ActivationDone: row.ActivationDone, BroadcastDone: row.BroadcastDone,
			DecklogDone: row.DecklogDone, LeaseToken: row.LeaseToken, EncryptedState: row.State == admissionStatePendingV2,
		}
		pushTargets, openErr := openAdmissionPushTargets(row.PushTargets, e.TenantID, e.InternalName, e.SourceGeneration, e.EncryptedState)
		if openErr != nil {
			e.ActivationPayloadInvalid = true
			logging.NewLogger().WithError(openErr).WithFields(logging.Fields{
				"effect_id": e.ID, "tenant_id": e.TenantID, "generation": e.SourceGeneration,
			}).Error("Admission effect push targets are undecryptable; activation leg will be poisoned")
		} else {
			e.PushTargets = pushTargets
		}
		peerClusters := row.PeerClusters.String
		if peerClusters != "" {
			if err := json.Unmarshal([]byte(peerClusters), &e.PeerHints); err != nil {
				// Per-leg poison, decided at apply: broadcasting without the durable filter input
				// would fail open (reach an unknown peer set while looking deliberate).
				e.PeerHintsInvalid = true
				logging.NewLogger().WithError(err).WithField("generation", e.SourceGeneration).Error("Admission effect peer_clusters undecodable; broadcast leg will be poisoned")
			} else {
				seenPeers := make(map[string]AdmissionPeerHint, len(e.PeerHints))
				for index := range e.PeerHints {
					e.PeerHints[index].ClusterID = strings.TrimSpace(e.PeerHints[index].ClusterID)
					e.PeerHints[index].Addr = strings.TrimSpace(e.PeerHints[index].Addr)
					if e.PeerHints[index].ClusterID == "" || e.PeerHints[index].Addr == "" {
						e.PeerHintsInvalid = true
						logging.NewLogger().WithField("generation", e.SourceGeneration).Error("Admission effect peer_clusters contains an incomplete peer; broadcast leg will be poisoned")
						break
					}
					if existing, ok := seenPeers[e.PeerHints[index].ClusterID]; ok &&
						(existing.Addr != e.PeerHints[index].Addr || existing.AlwaysOn != e.PeerHints[index].AlwaysOn) {
						e.PeerHintsInvalid = true
						logging.NewLogger().WithFields(map[string]interface{}{
							"generation":   e.SourceGeneration,
							"peer_cluster": e.PeerHints[index].ClusterID,
						}).Error("Admission effect peer_clusters contains conflicting peer hints; broadcast leg will be poisoned")
						break
					}
					seenPeers[e.PeerHints[index].ClusterID] = e.PeerHints[index]
				}
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// admissionLegFlags is one obligation's per-leg completion snapshot.
type admissionLegFlags struct {
	drain, activation, broadcast, decklog bool
	capacityPending                       bool
}

func (f admissionLegFlags) allDone() bool {
	return f.drain && f.activation && f.broadcast && f.decklog
}

// readAdmissionLegsLocked reads the row's flags under the caller's transaction, guarded by the
// worker's lease. sql.ErrNoRows means the lease was lost or the row settled elsewhere.
func readAdmissionLegsLocked(ctx context.Context, tx *sql.Tx, id int64, leaseToken string) (admissionLegFlags, error) {
	row, err := foghorndb.New(tx).ReadAdmissionLegsLocked(ctx, foghorndb.ReadAdmissionLegsLockedParams{EffectID: id, LeaseToken: leaseToken})
	return admissionLegFlags{drain: row.DrainDone, activation: row.ActivationDone, broadcast: row.BroadcastDone, decklog: row.DecklogDone, capacityPending: row.CapacityPending}, err
}

// settleAdmissionLegsLocked persists merged leg flags and, when every leg is done, the terminal
// transition. A valid push-target payload remains attached to an applied, still-active publisher:
// it is the exact admitted output authority replayed after a Helmsman/Mist restart. It is cleared
// when the generation ended or the activation payload was undecodable.
func settleAdmissionLegsLocked(ctx context.Context, tx *sql.Tx, effect AdmissionEffect, f admissionLegFlags, generationEnded bool, poisonNote string, clearPushTargets bool) (bool, error) {
	newState := "pending"
	if effect.EncryptedState {
		newState = admissionStatePendingV2
	}
	if f.allDone() {
		if generationEnded {
			newState = "superseded"
			if effect.EncryptedState {
				newState = admissionStateSupersededV2
			}
		} else {
			newState = "applied"
			if effect.EncryptedState {
				newState = admissionStateAppliedV2
			}
		}
	}
	n, err := foghorndb.New(tx).SettleAdmissionLegs(ctx, foghorndb.SettleAdmissionLegsParams{
		DrainDone: f.drain, ActivationDone: f.activation, BroadcastDone: f.broadcast, DecklogDone: f.decklog,
		CapacityPending: f.capacityPending,
		NewState:        newState, PoisonNote: poisonNote, ClearPushTargets: clearPushTargets,
		EffectID: effect.ID, LeaseToken: effect.LeaseToken,
	})
	if err != nil {
		return false, fmt.Errorf("settle admission effect legs: %w", err)
	}
	return f.allDone() && n == 1, nil
}

// RunAdmissionEffectEncryptionMigration continuously converts legacy
// plaintext/v1 rows to row-bound v2 ciphertext. Each batch is locked and state
// fenced atomically, so a pre-v0.3 worker either owns the legacy row first or
// can no longer see it after commit.
func RunAdmissionEffectEncryptionMigration(ctx context.Context, logger logging.Logger) {
	migrate := func() {
		for range 10 {
			count, err := migrateAdmissionEffectEncryptionBatch(ctx, 100)
			if err != nil {
				logger.WithError(err).Warn("Admission push-target encryption migration failed")
				return
			}
			if count < 100 {
				return
			}
		}
	}
	migrate()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			migrate()
		}
	}
}

func migrateAdmissionEffectEncryptionBatch(ctx context.Context, limit int32) (int, error) {
	if db == nil || admissionEffectEncryptor == nil || limit <= 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rollbackQuiet(tx)
	queries := foghorndb.New(tx)
	rows, err := queries.ListLegacyAdmissionPushTargetsForEncryption(ctx, limit)
	if err != nil {
		return 0, err
	}
	migrated := 0
	for _, row := range rows {
		format := fieldcrypto.CiphertextFormat(string(row.PushTargets))
		opened, openErr := openAdmissionPushTargets(row.PushTargets, row.TenantID, row.StreamInternalName, row.SourceGeneration)
		if openErr != nil {
			logging.NewLogger().WithError(openErr).WithField("effect_id", row.ID).Error("Skipping corrupt legacy admission payload during encryption migration")
			continue
		}
		protected, protectErr := protectAdmissionPushTargets(opened, row.TenantID, row.StreamInternalName, row.SourceGeneration)
		if protectErr != nil {
			logging.NewLogger().WithError(protectErr).WithField("effect_id", row.ID).Error("Skipping invalid legacy admission identity during encryption migration")
			continue
		}
		updated, updateErr := queries.UpgradeAdmissionPushTargetsEncryption(ctx, foghorndb.UpgradeAdmissionPushTargetsEncryptionParams{
			PushTargets: protected,
			EffectID:    row.ID,
		})
		if updateErr != nil {
			return 0, updateErr
		}
		if updated == 1 {
			incAdmissionPayloadCrypto(string(format), "migrated")
			migrated++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return migrated, nil
}

func probeAdmissionGeneration(ctx context.Context, tx *sql.Tx, effect AdmissionEffect) (ended bool, err error) {
	active, probeErr := foghorndb.New(tx).AdmissionGenerationActive(ctx, foghorndb.AdmissionGenerationActiveParams{
		TenantID: effect.TenantID, StreamInternalName: effect.InternalName, SourceGeneration: effect.SourceGeneration,
	})
	if probeErr != nil {
		return false, fmt.Errorf("recheck admission effect generation: %w", probeErr)
	}
	return !active, nil
}

// ApplyClaimedAdmissionEffect drives one obligation one step forward in three phases, so that NO
// row lock or stream advisory lock is ever held across external network I/O — an acknowledgement
// arriving while a slow leg dispatches must complete immediately, not time out behind the worker's
// transaction:
//
//	PHASE 1 (tx + stream advisory lock, no I/O): verify the lease, refresh the leg flags (earlier
//	acknowledgements may have landed), resolve generation liveness and MOOT the generation-bound
//	legs (activation, broadcast, drain — a late drain could nuke a successor session's buffer),
//	persist, commit. If nothing is owed the row settles here.
//
//	PHASE 2 (no locks): the apply callback dispatches remote legs and completes local ones.
//	Acknowledgements update the unlocked row freely.
//
//	PHASE 3 (tx + stream advisory lock, no I/O): re-read the flags (merging any acknowledgements
//	that landed during phase 2), merge the callback's results, re-resolve generation liveness for
//	the terminal label (and late moots), settle.
//
// Leg semantics are unchanged: the Decklog leg is owed regardless of generation liveness (the
// admission is a historical fact); drain/activation/broadcast moot when the generation ends; remote
// legs complete only via Helmsman's generation-correlated acknowledgements — dispatch is not
// completion. Committing the moots BEFORE the dispatches widens the documented in-flight drain
// residual from network-flight to the phase-2 window (bounded by the per-effect budget the worker
// applies, a few seconds worst-case, with the receiver-side SentAt acceptance window fencing a
// late-delivered drain); that is the accepted cost of never pinning the stream's admission path
// behind a stalled send.
func ApplyClaimedAdmissionEffect(ctx context.Context, effect AdmissionEffect, apply func(context.Context, AdmissionEffect) (AdmissionEffectLegResults, error)) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("apply admission effect: no database configured")
	}
	if effect.ID <= 0 || effect.TenantID == "" || effect.InternalName == "" || effect.LeaseToken == "" {
		return false, fmt.Errorf("apply admission effect missing identity")
	}

	// PHASE 1
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin admission effect tx: %w", err)
	}
	defer rollbackQuiet(tx)
	if lockErr := foghorndb.New(tx).AcquireDVRStartLock(ctx, ingestStreamAdvisoryLockKey(effect.TenantID, effect.InternalName)); lockErr != nil {
		return false, fmt.Errorf("lock admission effect stream: %w", lockErr)
	}
	flags, err := readAdmissionLegsLocked(ctx, tx, effect.ID, effect.LeaseToken)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, fmt.Errorf("lock admission effect lease: %w", err)
	}
	generationEnded, err := probeAdmissionGeneration(ctx, tx, effect)
	if err != nil {
		return false, err
	}
	if generationEnded {
		flags.activation, flags.broadcast, flags.drain = true, true, true
	}
	effect.DrainDone, effect.ActivationDone, effect.BroadcastDone, effect.DecklogDone = flags.drain, flags.activation, flags.broadcast, flags.decklog
	effect.CapacityPending = flags.capacityPending
	effect.GenerationEnded = generationEnded
	terminal, err := settleAdmissionLegsLocked(ctx, tx, effect, flags, generationEnded, "", false)
	if err != nil {
		return false, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return false, fmt.Errorf("commit admission effect phase 1: %w", commitErr)
	}
	if terminal {
		return true, nil
	}

	// PHASE 2 — no locks held.
	if apply == nil {
		return false, errors.New("apply admission effect callback is nil")
	}
	legs, applyErr := apply(ctx, effect)

	// PHASE 3
	tx3, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.Join(applyErr, fmt.Errorf("begin admission effect settle tx: %w", err))
	}
	defer rollbackQuiet(tx3)
	if lockErr := foghorndb.New(tx3).AcquireDVRStartLock(ctx, ingestStreamAdvisoryLockKey(effect.TenantID, effect.InternalName)); lockErr != nil {
		return false, errors.Join(applyErr, fmt.Errorf("lock admission effect stream for settle: %w", lockErr))
	}
	current, err := readAdmissionLegsLocked(ctx, tx3, effect.ID, effect.LeaseToken)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errors.Join(applyErr, tx3.Commit())
	}
	if err != nil {
		return false, errors.Join(applyErr, fmt.Errorf("re-read admission effect legs: %w", err))
	}
	current.broadcast = current.broadcast || legs.BroadcastDone || legs.BroadcastPoisoned
	current.decklog = current.decklog || legs.DecklogDone
	current.activation = current.activation || legs.ActivationDone || legs.ActivationPoisoned
	if legs.CapacityPending != nil {
		current.capacityPending = *legs.CapacityPending
	}
	generationEnded, err = probeAdmissionGeneration(ctx, tx3, effect)
	if err != nil {
		return false, errors.Join(applyErr, err)
	}
	if generationEnded {
		current.activation, current.broadcast, current.drain = true, true, true
	}
	terminal, err = settleAdmissionLegsLocked(ctx, tx3, effect, current, generationEnded, legs.PoisonNote, legs.ActivationPoisoned)
	if err != nil {
		return false, errors.Join(applyErr, err)
	}
	if commitErr := tx3.Commit(); commitErr != nil {
		return false, errors.Join(applyErr, fmt.Errorf("commit admission effect settle: %w", commitErr))
	}
	if applyErr != nil {
		return false, applyErr
	}
	return terminal, nil
}

// markAdmissionLeg completes one remote leg from Helmsman's acknowledgement, correlated by the
// EXACT obligation identity: the ingest generation echoed through the command/response pair
// (UNIQUE per obligation), additionally checked against the dispatch-target node column. A delayed
// or duplicated acknowledgement from an earlier generation therefore cannot complete a later
// generation's obligation — it matches at most its own single row. The marker only sets the leg
// flag; the WORKER terminalizes the row on its next pass, under the stream advisory lock, where it
// can decide applied-vs-superseded against the generation's current liveness.
func markAdmissionLeg(ctx context.Context, legColumn, nodeColumn, nodeID, sourceGeneration string, connectionFence int64) error {
	if db == nil {
		return nil
	}
	if strings.TrimSpace(sourceGeneration) == "" || strings.TrimSpace(nodeID) == "" {
		// An acknowledgement without its obligation identity cannot be correlated. Surface it —
		// this is a protocol bug (the responder failed to echo the generation), and dropping it
		// silently would look like a Helmsman that never answers while the obligation re-dispatches
		// forever. The lease/backoff cycle re-dispatches until a compliant response arrives.
		logging.NewLogger().WithFields(logging.Fields{
			"leg":     legColumn,
			"node_id": nodeID,
		}).Warn("Dropping uncorrelatable admission-effect acknowledgement (missing source generation)")
		return nil
	}
	q := foghorndb.New(db)
	var err error
	switch legColumn {
	case "drain_done":
		err = q.MarkAdmissionDrainDone(ctx, foghorndb.MarkAdmissionDrainDoneParams{SourceGeneration: sourceGeneration, NodeID: nodeID})
	default:
		return fmt.Errorf("mark admission: unsupported leg %q/%q", legColumn, nodeColumn)
	}
	if err != nil {
		return fmt.Errorf("mark admission %s: %w", legColumn, err)
	}
	return nil
}

// MarkAdmissionDrainDone records a successful (or nothing-to-drain) DrainStreamResponse from the
// prior owner node, for the exact obligation whose generation the response echoes.
func MarkAdmissionDrainDone(ctx context.Context, priorOwnerNodeID, sourceGeneration string) error {
	return markAdmissionLeg(ctx, "drain_done", "prior_owner_node_id", priorOwnerNodeID, sourceGeneration, 0)
}

// MarkAdmissionActivationDone records a converged ActivatePushTargetsResult from the publishing
// node, for the exact obligation whose generation the result echoes. connectionFence is the
// authenticated control-session fence that delivered the acknowledgement; a retired connection
// cannot complete output recovery armed by a newer registration.
func MarkAdmissionActivationDone(ctx context.Context, nodeID, sourceGeneration, activationAttempt string, connectionFence, targetRevision int64) error {
	if db == nil {
		return nil
	}
	if strings.TrimSpace(sourceGeneration) == "" || strings.TrimSpace(nodeID) == "" || connectionFence <= 0 || targetRevision < 0 {
		return errors.New("mark admission activation requires generation, node, connection fence, and target revision")
	}
	n, err := foghorndb.New(db).MarkAdmissionActivationDone(ctx, foghorndb.MarkAdmissionActivationDoneParams{
		SourceGeneration: sourceGeneration, NodeID: nodeID, ActivationAttempt: activationAttempt,
		ConnectionFence: connectionFence, TargetRevision: targetRevision,
	})
	if err != nil {
		return fmt.Errorf("mark admission activation: %w", err)
	}
	if n != 1 {
		return errors.New("mark admission activation: acknowledgement is outside the current durable activation attempt")
	}
	return nil
}

// AdmissionPushTargetAttemptCurrent checks the durable attempt before any
// result-side runtime mutation. MarkAdmissionActivationDone repeats this fence
// in its CAS so a rotation between validation and settlement cannot complete
// the replacement obligation.
func AdmissionPushTargetAttemptCurrent(ctx context.Context, nodeID, sourceGeneration string, targetRevision int64, activationAttempt string) (bool, error) {
	if db == nil {
		return false, errors.New("admission database is unavailable")
	}
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(sourceGeneration) == "" || targetRevision <= 0 || strings.TrimSpace(activationAttempt) == "" {
		return false, errors.New("activation-attempt check requires node, generation, revision, and attempt")
	}
	current, err := foghorndb.New(db).AdmissionPushTargetAttemptCurrent(ctx, foghorndb.AdmissionPushTargetAttemptCurrentParams{
		SourceGeneration: sourceGeneration, NodeID: nodeID, TargetRevision: targetRevision, ActivationAttempt: activationAttempt,
	})
	if err != nil {
		return false, fmt.Errorf("check durable activation attempt: %w", err)
	}
	return current, nil
}

// RequeueActivePushTargetActivationsForNode re-arms every exact target set belonging to a still-open
// publisher on nodeID when Helmsman registers a newer authenticated control connection. It resets
// only the activation leg; already-completed analytics, federation, and prior-owner work remain
// complete. The exact admission payload is retained rather than resolving current central policy.
func RequeueActivePushTargetActivationsForNode(ctx context.Context, nodeID string, connectionFence int64, instanceID string) (int64, error) {
	var total int64
	for {
		rearmed, examined, err := requeueActivePushTargetActivationsForNodeBatch(ctx, nodeID, connectionFence, instanceID)
		total += rearmed
		if err != nil || examined < reconnectPushTargetRearmBatchSize {
			return total, err
		}
	}
}

const reconnectPushTargetRearmBatchSize = 100

func requeueActivePushTargetActivationsForNodeBatch(ctx context.Context, nodeID string, connectionFence int64, instanceID string) (int64, int, error) {
	if db == nil {
		return 0, 0, errors.New("requeue active push targets requires the durable session store")
	}
	if strings.TrimSpace(nodeID) == "" || connectionFence <= 0 {
		return 0, 0, errors.New("requeue active push targets requires node identity and positive connection fence")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin reconnect push-target requeue: %w", err)
	}
	defer rollbackQuiet(tx)
	q := foghorndb.New(tx)
	rows, err := q.ListActivePushTargetActivationsForNodeRequeue(ctx, foghorndb.ListActivePushTargetActivationsForNodeRequeueParams{
		NodeID: nodeID, ConnectionFence: connectionFence,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("list reconnect push-target activations: %w", err)
	}
	examined := len(rows)
	var requeued int64
	for _, row := range rows {
		protected, _, rotateErr := rotateAdmissionPushTargetAttempt(ctx, q, row.TenantID, row.StreamInternalName, row.SourceGeneration, row.TargetRevision, row.PushTargets)
		if rotateErr != nil {
			logging.NewLogger().WithError(rotateErr).WithFields(logging.Fields{
				"effect_id": row.ID, "tenant_id": row.TenantID, "generation": row.SourceGeneration,
			}).Error("Skipping malformed reconnect restream obligation")
			incRestreamReconcile("reconnect", "poison_skipped")
			skipped, skipErr := q.SkipReconnectPushTargetActivationByID(ctx, foghorndb.SkipReconnectPushTargetActivationByIDParams{
				ConnectionFence: connectionFence, ErrorMessage: "reconnect re-arm skipped: " + rotateErr.Error(), EffectID: row.ID,
			})
			if skipErr != nil {
				return 0, examined, fmt.Errorf("record skipped reconnect push-target activation: %w", skipErr)
			}
			if skipped != 1 {
				return 0, examined, fmt.Errorf("record skipped reconnect push-target activation: expected one effect row, updated %d", skipped)
			}
			continue
		}
		updated, updateErr := q.RequeueActivePushTargetActivationByID(ctx, foghorndb.RequeueActivePushTargetActivationByIDParams{
			PushTargets: protected, ConnectionFence: connectionFence, InstanceID: instanceID, EffectID: row.ID,
		})
		if updateErr != nil {
			return 0, examined, fmt.Errorf("requeue reconnect push-target activation: %w", updateErr)
		}
		if updated != 1 {
			return 0, examined, fmt.Errorf("requeue reconnect push-target activation: expected one effect row, updated %d", updated)
		}
		requeued += updated
	}
	if err := tx.Commit(); err != nil {
		return 0, examined, fmt.Errorf("commit reconnect push-target requeue: %w", err)
	}
	return requeued, examined, nil
}

// ReconcileActivePushTargetAuthority replaces the durable desired target set
// for every live ingest generation of one stream. The media-authority version
// is the monotonic fence: duplicate/stale envelopes cannot re-arm older state.
func ReconcileActivePushTargetAuthority(ctx context.Context, tenantID, internalName string, desired *ipcpb.ActivatePushTargets) (int64, error) {
	if db == nil || desired == nil {
		return 0, nil
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(internalName) == "" || desired.GetTargetRevision() <= 0 {
		return 0, errors.New("push-target authority reconciliation requires tenant, stream, and positive revision")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin push-target authority reconciliation: %w", err)
	}
	defer rollbackQuiet(tx)
	queries := foghorndb.New(tx)
	if lockErr := queries.AcquireDVRStartLock(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); lockErr != nil {
		return 0, fmt.Errorf("lock push-target authority reconciliation: %w", lockErr)
	}
	rows, err := queries.ListActiveAdmissionPushTargetEffectsForUpdate(ctx, foghorndb.ListActiveAdmissionPushTargetEffectsForUpdateParams{
		TenantID: tenantID, StreamInternalName: internalName,
	})
	if err != nil {
		return 0, fmt.Errorf("list active push-target obligations: %w", err)
	}
	var updated int64
	for _, row := range rows {
		currentRevision := row.TargetRevision
		var current ipcpb.ActivatePushTargets
		currentReadable := true
		if len(row.PushTargets) > 0 {
			opened, openErr := openAdmissionPushTargets(row.PushTargets, tenantID, internalName, row.SourceGeneration)
			if openErr != nil {
				currentReadable = false
				logging.NewLogger().WithError(openErr).WithFields(logging.Fields{
					"tenant_id": tenantID, "stream": internalName, "generation": row.SourceGeneration,
				}).Error("Replacing unreadable durable push-target obligation from signed authority")
			} else if decodeErr := proto.Unmarshal(opened, &current); decodeErr != nil {
				currentReadable = false
				logging.NewLogger().WithError(decodeErr).WithFields(logging.Fields{
					"tenant_id": tenantID, "stream": internalName, "generation": row.SourceGeneration,
				}).Error("Replacing undecodable durable push-target obligation from signed authority")
			} else {
				currentRevision = current.GetTargetRevision()
			}
		}
		if currentRevision > desired.GetTargetRevision() {
			continue
		}
		next := proto.CloneOf(desired)
		next.SourceGeneration = row.SourceGeneration
		if strings.TrimSpace(next.GetActivationAttempt()) == "" {
			next.ActivationAttempt = uuid.NewString()
		}
		if currentReadable && samePushTargetSet(&current, next) {
			continue
		}
		raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(next)
		if err != nil {
			return 0, fmt.Errorf("encode desired push-target obligation: %w", err)
		}
		protected, err := protectAdmissionPushTargets(raw, tenantID, internalName, row.SourceGeneration)
		if err != nil {
			return 0, fmt.Errorf("protect desired push-target obligation: %w", err)
		}
		if row.TargetRevision == desired.GetTargetRevision() {
			n, repairErr := queries.RepairAdmissionPushTargetRevision(ctx, foghorndb.RepairAdmissionPushTargetRevisionParams{
				PushTargets: protected, SourceGeneration: row.SourceGeneration, TargetRevision: desired.GetTargetRevision(), ActivationAttempt: next.GetActivationAttempt(),
			})
			if repairErr != nil {
				return 0, fmt.Errorf("repair desired push-target revision: %w", repairErr)
			}
			if n != 1 {
				return 0, fmt.Errorf("repair desired push-target revision: expected one history row, updated %d", n)
			}
		} else if storeErr := queries.StoreAdmissionPushTargetRevision(ctx, foghorndb.StoreAdmissionPushTargetRevisionParams{
			TenantID: tenantID, StreamInternalName: internalName, NodeID: row.NodeID,
			SourceGeneration: row.SourceGeneration, TargetRevision: desired.GetTargetRevision(), PushTargets: protected,
			ActivationAttempt: next.GetActivationAttempt(),
		}); storeErr != nil {
			return 0, fmt.Errorf("store desired push-target revision: %w", storeErr)
		}
		n, rearmErr := queries.RearmAdmissionPushTargetEffect(ctx, foghorndb.RearmAdmissionPushTargetEffectParams{
			PushTargets: protected, TargetRevision: desired.GetTargetRevision(), EffectID: row.ID,
		})
		if rearmErr != nil {
			return 0, fmt.Errorf("re-arm push-target obligation: %w", rearmErr)
		}
		updated += n
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit push-target authority reconciliation: %w", err)
	}
	return updated, nil
}

func samePushTargetSet(left, right *ipcpb.ActivatePushTargets) bool {
	if left == nil || right == nil || left.GetMaxViewers() != right.GetMaxViewers() || len(left.GetTargets()) != len(right.GetTargets()) {
		return false
	}
	byID := make(map[string]*ipcpb.PushTargetSpec, len(left.GetTargets()))
	for _, target := range left.GetTargets() {
		byID[target.GetTargetId()] = target
	}
	for _, target := range right.GetTargets() {
		other, ok := byID[target.GetTargetId()]
		if !ok || other.GetTargetUri() != target.GetTargetUri() || other.GetName() != target.GetName() || other.GetPlatform() != target.GetPlatform() {
			return false
		}
	}
	return true
}

// RearmAdmissionPushTargetsAfterRuntimeEnd schedules exact-state convergence
// after a managed output exits while its publisher generation is still live.
// Configuration failures are intentionally not passed here; they remain
// failed until the next authority revision.
func RearmAdmissionPushTargetsAfterRuntimeEnd(ctx context.Context, tenantID, internalName, sourceGeneration string, targetRevision int64) (bool, error) {
	if db == nil {
		return false, nil
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(internalName) == "" || strings.TrimSpace(sourceGeneration) == "" || targetRevision <= 0 {
		return false, errors.New("runtime restream rearm requires tenant, stream, generation, and target revision")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin runtime restream re-arm: %w", err)
	}
	defer rollbackQuiet(tx)
	q := foghorndb.New(tx)
	row, err := q.GetAdmissionPushTargetRuntimeRearmForUpdate(ctx, foghorndb.GetAdmissionPushTargetRuntimeRearmForUpdateParams{
		TenantID: tenantID, StreamInternalName: internalName, SourceGeneration: sourceGeneration, TargetRevision: targetRevision,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock runtime restream re-arm: %w", err)
	}
	protected, _, err := rotateAdmissionPushTargetAttempt(ctx, q, tenantID, internalName, sourceGeneration, targetRevision, row.PushTargets)
	if err != nil {
		return false, err
	}
	n, err := q.RearmAdmissionPushTargetsAfterRuntimeEnd(ctx, foghorndb.RearmAdmissionPushTargetsAfterRuntimeEndParams{
		PushTargets: protected, TenantID: tenantID, StreamInternalName: internalName,
		SourceGeneration: sourceGeneration, TargetRevision: targetRevision,
	})
	if err != nil {
		return false, fmt.Errorf("re-arm restream after runtime end: %w", err)
	}
	if n != 1 {
		return false, fmt.Errorf("re-arm restream after runtime end: expected one effect row, updated %d", n)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit runtime restream re-arm: %w", err)
	}
	return true, nil
}

func rotateAdmissionPushTargetAttempt(ctx context.Context, q *foghorndb.Queries, tenantID, internalName, sourceGeneration string, targetRevision int64, protectedPayload []byte) ([]byte, string, error) {
	opened, err := openAdmissionPushTargets(protectedPayload, tenantID, internalName, sourceGeneration)
	if err != nil {
		return nil, "", fmt.Errorf("open runtime restream re-arm payload: %w", err)
	}
	var activation ipcpb.ActivatePushTargets
	if decodeErr := proto.Unmarshal(opened, &activation); decodeErr != nil {
		return nil, "", fmt.Errorf("decode runtime restream re-arm payload: %w", decodeErr)
	}
	if activation.GetTargetRevision() != targetRevision {
		return nil, "", fmt.Errorf("runtime restream re-arm payload revision %d does not match durable revision %d", activation.GetTargetRevision(), targetRevision)
	}
	activation.ActivationAttempt = uuid.NewString()
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(&activation)
	if err != nil {
		return nil, "", fmt.Errorf("encode runtime restream re-arm payload: %w", err)
	}
	protected, err := protectAdmissionPushTargets(raw, tenantID, internalName, sourceGeneration)
	if err != nil {
		return nil, "", fmt.Errorf("protect runtime restream re-arm payload: %w", err)
	}
	historyRows, err := q.RotateAdmissionPushTargetRuntimeAttempt(ctx, foghorndb.RotateAdmissionPushTargetRuntimeAttemptParams{
		PushTargets: protected, ActivationAttempt: activation.GetActivationAttempt(),
		SourceGeneration: sourceGeneration, TargetRevision: targetRevision,
	})
	if err != nil {
		return nil, "", fmt.Errorf("rotate durable restream runtime attempt: %w", err)
	}
	if historyRows != 1 {
		return nil, "", fmt.Errorf("rotate durable restream runtime attempt: expected one history row, updated %d", historyRows)
	}
	return protected, activation.GetActivationAttempt(), nil
}

// RearmCapacityPendingPushTargetEffects retries durable target sets whose last
// convergence admitted only a capacity-available subset. Existing revision-
// scoped reservations are idempotently retained while newly available slots
// are added on the next activation pass.
func RearmCapacityPendingPushTargetEffects(ctx context.Context) (int64, error) {
	if db == nil {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin capacity-pending restream re-arm: %w", err)
	}
	defer rollbackQuiet(tx)
	q := foghorndb.New(tx)
	acquired, err := q.TryAcquireRestreamCapacityRearmLock(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire capacity-pending restream re-arm lock: %w", err)
	}
	if !acquired {
		return 0, nil
	}
	rows, err := q.ListCapacityPendingPushTargetEffectsForRearm(ctx)
	if err != nil {
		return 0, fmt.Errorf("list capacity-pending restream targets: %w", err)
	}
	var n int64
	for _, row := range rows {
		protected, _, rotateErr := rotateAdmissionPushTargetAttempt(ctx, q, row.TenantID, row.StreamInternalName, row.SourceGeneration, row.TargetRevision, row.PushTargets)
		if rotateErr != nil {
			logging.NewLogger().WithError(rotateErr).WithFields(logging.Fields{
				"effect_id": row.ID, "tenant_id": row.TenantID, "generation": row.SourceGeneration,
			}).Error("Skipping malformed capacity-pending restream obligation")
			continue
		}
		updated, updateErr := q.RearmCapacityPendingPushTargetEffectByID(ctx, foghorndb.RearmCapacityPendingPushTargetEffectByIDParams{PushTargets: protected, EffectID: row.ID})
		if updateErr != nil {
			return 0, fmt.Errorf("re-arm capacity-pending restream target: %w", updateErr)
		}
		if updated != 1 {
			return 0, fmt.Errorf("re-arm capacity-pending restream target: expected one effect row, updated %d", updated)
		}
		n += updated
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit capacity-pending restream re-arm: %w", err)
	}
	return n, nil
}

// RearmCooledDownRuntimePushTargetEffects starts a fresh bounded retry cycle
// after an unstable destination exhausted its per-generation budget and then
// remained quiet for the cooldown window.
func RearmCooledDownRuntimePushTargetEffects(ctx context.Context) (int64, error) {
	if db == nil {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin cooled-down restream re-arm: %w", err)
	}
	defer rollbackQuiet(tx)
	q := foghorndb.New(tx)
	acquired, err := q.TryAcquireRestreamCapacityRearmLock(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire cooled-down restream re-arm lock: %w", err)
	}
	if !acquired {
		return 0, nil
	}
	rows, err := q.ListCooledDownRuntimePushTargetEffectsForRearm(ctx)
	if err != nil {
		return 0, fmt.Errorf("list cooled-down restream targets: %w", err)
	}
	var n int64
	for _, row := range rows {
		protected, _, rotateErr := rotateAdmissionPushTargetAttempt(ctx, q, row.TenantID, row.StreamInternalName, row.SourceGeneration, row.TargetRevision, row.PushTargets)
		if rotateErr != nil {
			logging.NewLogger().WithError(rotateErr).WithFields(logging.Fields{
				"effect_id": row.ID, "tenant_id": row.TenantID, "generation": row.SourceGeneration,
			}).Error("Skipping malformed cooled-down restream obligation")
			continue
		}
		updated, updateErr := q.RearmCooledDownRuntimePushTargetEffectByID(ctx, foghorndb.RearmCooledDownRuntimePushTargetEffectByIDParams{PushTargets: protected, EffectID: row.ID})
		if updateErr != nil {
			return 0, fmt.Errorf("re-arm cooled-down restream target: %w", updateErr)
		}
		if updated != 1 {
			return 0, fmt.Errorf("re-arm cooled-down restream target: expected one effect row, updated %d", updated)
		}
		n += updated
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit cooled-down restream re-arm: %w", err)
	}
	return n, nil
}

// NodeConnOwnerInstance resolves which Foghorn instance owns the node's control connection ("" when
// unknown/disconnected). Used to route the node-affine activation leg's durable claim affinity.
func NodeConnOwnerInstance(ctx context.Context, nodeID string) string {
	rs := GetRedisStore()
	if rs == nil {
		return ""
	}
	owner, err := rs.GetConnOwner(ctx, nodeID)
	if err != nil {
		return ""
	}
	return owner.InstanceID
}

// NodeConnOwnedLocally reports whether THIS replica owns the node's control connection. The
// node-affine ACTIVATION leg (push-target tracking for PUSH_OUT_START/PUSH_END attribution plus the
// local-only dispatch) must run on that replica; the federation broadcast leg has its own authority
// (the PeerManager leader) and is gated separately.
func NodeConnOwnedLocally(nodeID string) bool {
	_, ok := currentNodeSession(nodeID)
	return ok
}

// ReleaseAdmissionEffectNotOwner releases a claimed obligation WITHOUT a failure penalty. When
// authorityInstance is non-empty, it is recorded as the row's durable CLAIM AFFINITY: the claim
// query then admits only that instance — the one that actually owns the outstanding
// authority-bound work (the node's connection owner, the federation leader) — until the affinity
// goes stale (10s), so N-1 wrong replicas cannot alternate claims while the authority never wins
// the SKIP LOCKED race. An empty authorityInstance is a plain hand-back (unknown authority or an
// unprocessed batch tail) — immediately claimable by anyone, including this instance.
func ReleaseAdmissionEffectNotOwner(ctx context.Context, effect AdmissionEffect, authorityInstance string) error {
	if db == nil || effect.ID <= 0 || effect.LeaseToken == "" {
		return nil
	}
	err := foghorndb.New(db).ReleaseAdmissionEffectNotOwner(ctx, foghorndb.ReleaseAdmissionEffectNotOwnerParams{
		EffectID: effect.ID, LeaseToken: effect.LeaseToken, AuthorityInstance: authorityInstance,
	})
	if err != nil {
		return fmt.Errorf("release admission effect to owner: %w", err)
	}
	return nil
}

// FailAdmissionEffect releases a claimed obligation after a transient apply failure with
// exponential backoff, so another pass (on any replica) re-drives the owed legs.
func FailAdmissionEffect(ctx context.Context, effect AdmissionEffect, cause error) error {
	if db == nil || effect.ID <= 0 || effect.LeaseToken == "" {
		return nil
	}
	message := "admission effect failed"
	if cause != nil {
		message = cause.Error()
	}
	// Preserve any per-leg poison diagnostic already recorded on the row: a transient failure of a
	// DIFFERENT leg in the same pass must not overwrite the permanent poison note.
	err := foghorndb.New(db).FailAdmissionEffect(ctx, foghorndb.FailAdmissionEffectParams{
		EffectID: effect.ID, LeaseToken: effect.LeaseToken, ErrorMessage: message,
	})
	if err != nil {
		return fmt.Errorf("release failed admission effect: %w", err)
	}
	return nil
}

// PurgeTerminalAdmissionEffects deletes applied/superseded obligations older than the retention
// window. An applied row holding an active publisher's exact push-target set is restart authority,
// not merely diagnostics, and remains until that generation ends.
func PurgeTerminalAdmissionEffects(ctx context.Context, olderThan time.Duration) (int64, error) {
	if db == nil {
		return 0, nil
	}
	if olderThan <= 0 {
		olderThan = 24 * time.Hour
	}
	queries := foghorndb.New(db)
	n, err := queries.PurgeTerminalAdmissionEffects(ctx, olderThan.Milliseconds())
	if err != nil {
		return 0, fmt.Errorf("purge terminal admission effects: %w", err)
	}
	history, err := queries.PurgeAdmissionPushTargetRevisions(ctx, olderThan.Milliseconds())
	if err != nil {
		return n, fmt.Errorf("purge admission push-target revisions: %w", err)
	}
	return n + history, nil
}

// PurgeableAdmissionEffectFences checks a bounded cross-tenant maintenance batch against the
// durable admission ledger. Pending includes leased/in-flight callbacks, so a positive result
// proves that deleting the matching Redis tombstone cannot expose a delayed TrackStream write.
func PurgeableAdmissionEffectFences(ctx context.Context, fences []AdmissionEffectFence) (map[string]bool, error) {
	result := make(map[string]bool, len(fences))
	if db == nil || len(fences) == 0 {
		return result, nil
	}
	for _, fence := range fences {
		if strings.TrimSpace(fence.TenantID) == "" || strings.TrimSpace(fence.InternalName) == "" || fence.SourceRevision <= 0 {
			return nil, errors.New("admission-effect purge fence requires tenant, stream, and positive revision")
		}
	}
	raw, err := json.Marshal(fences)
	if err != nil {
		return nil, fmt.Errorf("encode admission-effect purge fences: %w", err)
	}
	// Cell-scoped maintenance exception to ordinary tenant-filtered queries: each candidate carries
	// its tenant id, and the anti-join compares only that tenant's stream and revision range.
	rows, err := foghorndb.New(db).ListPurgeableAdmissionEffectFences(ctx, json.RawMessage(raw))
	if err != nil {
		return nil, fmt.Errorf("check admission-effect purge fences: %w", err)
	}
	for _, row := range rows {
		result[row.InternalName] = row.Purgeable
	}
	return result, nil
}
