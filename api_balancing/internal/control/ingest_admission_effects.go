package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

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
	BroadcastLive              bool
	DecklogTrigger             []byte
	PeerHints                  []AdmissionPeerHint
	// PeerHintsInvalid: the persisted peer set could not be decoded. The broadcast leg treats
	// this as PER-LEG POISON (settled with diagnostics), never as "broadcast without tracking".
	PeerHintsInvalid bool
	DrainDone        bool
	ActivationDone   bool
	BroadcastDone    bool
	DecklogDone      bool
	// GenerationEnded is resolved under the stream lock during apply: when true, the activation,
	// broadcast AND drain legs are moot (a dead generation must not be announced, start pushes, or
	// nuke a runtime name a successor session may now own); only the Decklog leg remains owed.
	GenerationEnded bool
	LeaseToken      string
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
	BroadcastDone      bool
	DecklogDone        bool
	ActivationPoisoned bool
	BroadcastPoisoned  bool
	Deferred           bool
	// AuthorityInstance is the instance that owns the deferred leg's authority (resolved by the
	// callback at the defer point: the node's connection owner or the federation leader); the
	// worker records it as the row's claim affinity. Empty when unresolvable — plain hand-back.
	AuthorityInstance string
	PoisonNote        string
}

func enqueueAdmissionEffectTx(ctx context.Context, tx *sql.Tx, tenantID, internalName, nodeID, generation string, revision int64, priorOwnerNodeID, priorOwnerSourceGeneration string, intent AdmissionEffectIntent) error {
	if revision <= 0 {
		return fmt.Errorf("enqueue admission effect requires positive source revision")
	}
	// Legs that do not apply to this obligation are born complete.
	var peerClusters any
	if len(intent.PeerHints) > 0 {
		raw, marshalErr := json.Marshal(intent.PeerHints)
		if marshalErr != nil {
			return fmt.Errorf("serialize admission peer clusters: %w", marshalErr)
		}
		peerClusters = string(raw)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO foghorn.ingest_admission_effects
			(tenant_id, stream_internal_name, node_id, source_generation, source_revision,
			 prior_owner_node_id, prior_owner_source_generation, push_targets, broadcast_live, decklog_trigger, peer_clusters,
			 drain_done, activation_done, broadcast_done, decklog_done)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6, NULLIF($7, '')::uuid, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (source_generation) DO NOTHING
	`, tenantID, internalName, nodeID, generation, revision, priorOwnerNodeID, priorOwnerSourceGeneration,
		intent.PushTargets, intent.BroadcastLive, intent.DecklogTrigger, peerClusters,
		strings.TrimSpace(priorOwnerNodeID) == "" || strings.TrimSpace(priorOwnerNodeID) == nodeID || strings.TrimSpace(priorOwnerSourceGeneration) == "",
		len(intent.PushTargets) == 0,
		!intent.BroadcastLive,
		len(intent.DecklogTrigger) == 0)
	if err != nil {
		return fmt.Errorf("enqueue admission effect: %w", err)
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
	rows, err := db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id
			  FROM foghorn.ingest_admission_effects
			 WHERE state = 'pending'
			   AND next_attempt_at <= NOW()
			   AND (leased_until IS NULL OR leased_until < NOW())
			   AND (claim_affinity IS NULL OR claim_affinity = $3
			        OR updated_at <= NOW() - INTERVAL '10 seconds')
			 ORDER BY next_attempt_at, id
			 FOR UPDATE SKIP LOCKED
			 LIMIT $1
		), leased AS (
			UPDATE foghorn.ingest_admission_effects a
			   SET lease_token = gen_random_uuid(),
			       leased_until = NOW() + ($2 * INTERVAL '1 millisecond'),
			       attempts = attempts + 1,
			       claim_affinity = NULL,
			       updated_at = NOW()
			  FROM candidates c
			 WHERE a.id = c.id
			 RETURNING a.id, a.tenant_id::text, a.stream_internal_name, a.node_id,
			           a.source_generation::text, a.source_revision, a.prior_owner_node_id,
			           COALESCE(a.prior_owner_source_generation::text, ''),
			           a.push_targets, a.broadcast_live, a.decklog_trigger, COALESCE(a.peer_clusters, ''),
			           a.drain_done, a.activation_done, a.broadcast_done, a.decklog_done,
			           a.lease_token::text
		)
		SELECT * FROM leased ORDER BY id
	`, limit, lease.Milliseconds(), instanceID)
	if err != nil {
		return nil, fmt.Errorf("claim admission effects: %w", err)
	}
	defer rows.Close()
	var out []AdmissionEffect
	for rows.Next() {
		var e AdmissionEffect
		var peerClusters string
		if err := rows.Scan(&e.ID, &e.TenantID, &e.InternalName, &e.NodeID, &e.SourceGeneration,
			&e.SourceRevision, &e.PriorOwnerNodeID, &e.PriorOwnerSourceGeneration, &e.PushTargets, &e.BroadcastLive,
			&e.DecklogTrigger, &peerClusters, &e.DrainDone, &e.ActivationDone, &e.BroadcastDone, &e.DecklogDone,
			&e.LeaseToken); err != nil {
			return nil, fmt.Errorf("scan admission effect: %w", err)
		}
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
	return out, rows.Err()
}

// admissionLegFlags is one obligation's per-leg completion snapshot.
type admissionLegFlags struct {
	drain, activation, broadcast, decklog bool
}

func (f admissionLegFlags) allDone() bool {
	return f.drain && f.activation && f.broadcast && f.decklog
}

// readAdmissionLegsLocked reads the row's flags under the caller's transaction, guarded by the
// worker's lease. sql.ErrNoRows means the lease was lost or the row settled elsewhere.
func readAdmissionLegsLocked(ctx context.Context, tx *sql.Tx, id int64, leaseToken string) (admissionLegFlags, error) {
	var f admissionLegFlags
	err := tx.QueryRowContext(ctx, `
		SELECT drain_done, activation_done, broadcast_done, decklog_done
		  FROM foghorn.ingest_admission_effects
		 WHERE id=$1 AND state='pending' AND lease_token=$2::uuid
		 FOR UPDATE
	`, id, leaseToken).Scan(&f.drain, &f.activation, &f.broadcast, &f.decklog)
	return f, err
}

// settleAdmissionLegsLocked persists merged leg flags and, when every leg is done, the terminal
// transition (label decided by generation liveness; payload columns cleared — push_targets embeds
// destination credentials; per-leg poison notes retained in last_error).
func settleAdmissionLegsLocked(ctx context.Context, tx *sql.Tx, effect AdmissionEffect, f admissionLegFlags, generationEnded bool, poisonNote string) (bool, error) {
	newState := "pending"
	if f.allDone() {
		if generationEnded {
			newState = "superseded"
		} else {
			newState = "applied"
		}
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE foghorn.ingest_admission_effects
		   SET drain_done=$3, activation_done=$4, broadcast_done=$5, decklog_done=$6,
		       state=$7::varchar, updated_at=NOW(),
		       last_error = COALESCE(NULLIF($8, ''), last_error),
		       applied_at = CASE WHEN $7::varchar <> 'pending' THEN NOW() ELSE applied_at END,
		       leased_until = CASE WHEN $7::varchar <> 'pending' THEN NULL ELSE leased_until END,
		       lease_token  = CASE WHEN $7::varchar <> 'pending' THEN NULL ELSE lease_token END,
		       push_targets   = CASE WHEN $7::varchar <> 'pending' THEN NULL ELSE push_targets END,
		       decklog_trigger = CASE WHEN $7::varchar <> 'pending' THEN NULL ELSE decklog_trigger END
		 WHERE id=$1 AND state='pending' AND lease_token=$2::uuid
	`, effect.ID, effect.LeaseToken, f.drain, f.activation, f.broadcast, f.decklog, newState, poisonNote)
	if err != nil {
		return false, fmt.Errorf("settle admission effect legs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("settle admission effect rows: %w", err)
	}
	return newState != "pending" && n == 1, nil
}

func probeAdmissionGeneration(ctx context.Context, tx *sql.Tx, effect AdmissionEffect) (ended bool, err error) {
	var active bool
	if probeErr := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM foghorn.ingest_sessions
			 WHERE tenant_id = $1::uuid AND stream_internal_name = $2 AND id = $3::uuid AND ended_at IS NULL
		)`, effect.TenantID, effect.InternalName, effect.SourceGeneration).Scan(&active); probeErr != nil {
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
	if _, lockErr := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`, ingestStreamAdvisoryLockKey(effect.TenantID, effect.InternalName)); lockErr != nil {
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
	effect.GenerationEnded = generationEnded
	terminal, err := settleAdmissionLegsLocked(ctx, tx, effect, flags, generationEnded, "")
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
	if _, lockErr := tx3.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`, ingestStreamAdvisoryLockKey(effect.TenantID, effect.InternalName)); lockErr != nil {
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
	current.activation = current.activation || legs.ActivationPoisoned
	generationEnded, err = probeAdmissionGeneration(ctx, tx3, effect)
	if err != nil {
		return false, errors.Join(applyErr, err)
	}
	if generationEnded {
		current.activation, current.broadcast, current.drain = true, true, true
	}
	terminal, err = settleAdmissionLegsLocked(ctx, tx3, effect, current, generationEnded, legs.PoisonNote)
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
func markAdmissionLeg(ctx context.Context, legColumn, nodeColumn, nodeID, sourceGeneration string) error {
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
	//nolint:gosec // legColumn/nodeColumn are compile-time constants from the two callers below.
	query := fmt.Sprintf(`
		UPDATE foghorn.ingest_admission_effects
		   SET %s = TRUE, updated_at = NOW()
		 WHERE state='pending' AND source_generation = $1::uuid AND %s = $2
	`, legColumn, nodeColumn)
	if _, err := db.ExecContext(ctx, query, sourceGeneration, nodeID); err != nil {
		return fmt.Errorf("mark admission %s: %w", legColumn, err)
	}
	return nil
}

// MarkAdmissionDrainDone records a successful (or nothing-to-drain) DrainStreamResponse from the
// prior owner node, for the exact obligation whose generation the response echoes.
func MarkAdmissionDrainDone(ctx context.Context, priorOwnerNodeID, sourceGeneration string) error {
	return markAdmissionLeg(ctx, "drain_done", "prior_owner_node_id", priorOwnerNodeID, sourceGeneration)
}

// MarkAdmissionActivationDone records a converged ActivatePushTargetsResult from the publishing
// node, for the exact obligation whose generation the result echoes.
func MarkAdmissionActivationDone(ctx context.Context, nodeID, sourceGeneration string) error {
	return markAdmissionLeg(ctx, "activation_done", "node_id", nodeID, sourceGeneration)
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
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.ingest_admission_effects
		   SET leased_until=NULL, lease_token=NULL, attempts=GREATEST(attempts-1, 0),
		       claim_affinity=NULLIF($3, ''), next_attempt_at=NOW(), updated_at=NOW()
		 WHERE id=$1 AND state='pending' AND lease_token=$2::uuid
	`, effect.ID, effect.LeaseToken, authorityInstance)
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
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.ingest_admission_effects
		   SET leased_until=NULL, lease_token=NULL, updated_at=NOW(),
		       last_error = CASE WHEN last_error LIKE 'poison:%' THEN last_error || ' | ' || $3 ELSE $3 END,
		       next_attempt_at=NOW() + LEAST(INTERVAL '5 minutes', INTERVAL '1 second' * power(2, LEAST(attempts, 8)))
		 WHERE id=$1 AND state='pending' AND lease_token=$2::uuid
	`, effect.ID, effect.LeaseToken, message)
	if err != nil {
		return fmt.Errorf("release failed admission effect: %w", err)
	}
	return nil
}

// PurgeTerminalAdmissionEffects deletes applied/superseded obligations older than the retention
// window; the pending set is the working state, terminal rows are only diagnostics.
func PurgeTerminalAdmissionEffects(ctx context.Context, olderThan time.Duration) (int64, error) {
	if db == nil {
		return 0, nil
	}
	if olderThan <= 0 {
		olderThan = 24 * time.Hour
	}
	res, err := db.ExecContext(ctx, `
		DELETE FROM foghorn.ingest_admission_effects
		 WHERE id IN (
			SELECT id FROM foghorn.ingest_admission_effects
			 WHERE state IN ('applied', 'superseded')
			   AND updated_at < NOW() - ($1 * INTERVAL '1 millisecond')
			 ORDER BY updated_at
			 LIMIT 1000
		 )
	`, olderThan.Milliseconds())
	if err != nil {
		return 0, fmt.Errorf("purge terminal admission effects: %w", err)
	}
	return res.RowsAffected()
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
	rows, err := db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT tenant_id, internal_name, source_revision
			  FROM jsonb_to_recordset($1::jsonb)
			       AS c(tenant_id text, internal_name text, source_revision bigint)
		)
		SELECT c.internal_name,
		       NOT EXISTS (
				SELECT 1
				  FROM foghorn.ingest_admission_effects e
				 WHERE e.tenant_id = c.tenant_id::uuid
				   AND e.stream_internal_name = c.internal_name
				   AND e.source_revision <= c.source_revision
				   AND e.state = 'pending'
		       ) AS purgeable
		  FROM candidates c
	`, string(raw))
	if err != nil {
		return nil, fmt.Errorf("check admission-effect purge fences: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var internalName string
		var purgeable bool
		if err := rows.Scan(&internalName, &purgeable); err != nil {
			return nil, fmt.Errorf("scan admission-effect purge fence: %w", err)
		}
		result[internalName] = purgeable
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read admission-effect purge fences: %w", err)
	}
	return result, nil
}
