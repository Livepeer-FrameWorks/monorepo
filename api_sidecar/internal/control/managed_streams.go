package control

import (
	"slices"
	"strings"
	"sync"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// Reserved FrameWorks Mist stream tags. MistServer treats `tags` on a stream
// config as first-class persistent metadata: `streamStarted` copies them into
// runtime stats, `Util::streamTags` falls back to the config when SHM is
// empty, `#tag` matchers consult them, and `tags_inhibit` in process configs
// consumes them. Adding these tags affects Mist behavior in addition to
// round-tripping through `config_backup`. Keep the `fw:` namespace reserved
// and do not reuse these tags for non-managed-stream purposes.
//
// managedStreamOwnerTag marks every Mist stream Foghorn provisioned through
// the managed-stream channel. Sidecar adds it on every Apply and uses it as
// a sanity check during Retract so a stale or spoofed Retract for a tenant
// push/pull stream cannot leak through.
const managedStreamOwnerTag = "fw:managed:foghorn"

// managedStreamIDTagPrefix is the tag carrier for the stream_id. It is
// embedded in Mist's stream tags at Apply time and parsed back out on
// post-restart hydration so the sidecar snapshot can reconstruct the
// stream_id even when the in-process map has been wiped. Without it,
// Foghorn's hydration would land under the bare Mist name while the
// reconciler keys by stream_id — see AppliedManagedStream in ipc.proto.
const managedStreamIDTagPrefix = "fw:stream:"

// appliedManagedStreams tracks every concrete Mist stream this sidecar has
// Applied via the managed-stream channel. Keyed by Mist stream name (==
// bare internal name); value is the last Applied snapshot used for
// idempotency on re-Apply. Owner-scope safety: Retract only deletes a stream
// whose name appears in this map AND was tagged on Apply. Tenant push/pull
// streams (no entry here) stay untouched even if a stale Retract arrives.
var appliedManagedStreams = struct {
	sync.Mutex
	m map[string]managedStreamLocalSnapshot
}{m: make(map[string]managedStreamLocalSnapshot)}

type managedStreamLocalSnapshot struct {
	source       string
	alwaysOn     bool
	realtime     bool
	stopSessions bool
	tags         []string
	ingestMode   string
	streamID     string
}

func (s managedStreamLocalSnapshot) equals(o managedStreamLocalSnapshot) bool {
	return s.source == o.source &&
		s.alwaysOn == o.alwaysOn &&
		s.realtime == o.realtime &&
		s.stopSessions == o.stopSessions &&
		s.ingestMode == o.ingestMode &&
		s.streamID == o.streamID &&
		slices.Equal(s.tags, o.tags)
}

// HydrateAppliedManagedStreamsFromMist scans MistServer's current config
// for streams tagged with managedStreamOwnerTag and repopulates the
// in-memory appliedManagedStreams map. Called at sidecar startup so that
// streams already configured on this node (from a previous Foghorn Apply
// before this process restarted) are recognised as ours: subsequent
// Retract commands then correctly delete them instead of being silently
// dropped as "unknown". Without this, a sidecar restart + Foghorn retract
// would leave a stale Mist stream running on the node.
func HydrateAppliedManagedStreamsFromMist(logger logging.Logger) {
	cfg := currentConfig
	if cfg == nil {
		return
	}
	mistClient := mist.NewClient(logger)
	if cfg.MistServerURL != "" {
		mistClient.BaseURL = cfg.MistServerURL
	}
	backup, err := mistClient.ConfigBackup()
	if err != nil {
		logger.WithError(err).Warn("HydrateAppliedManagedStreams: ConfigBackup failed; managed-stream Retract may no-op until next Apply re-records")
		return
	}
	streams, ok := backup["streams"].(map[string]interface{})
	if !ok {
		return
	}
	hydrated := 0
	for name, raw := range streams {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if !mistEntryHasOwnerTag(entry) {
			continue
		}
		snap := mistEntryToSnapshot(entry)
		// Owner tag alone is insufficient: a stream with `fw:managed:foghorn`
		// but no `fw:stream:<id>` (manually added, half-applied, copied from
		// another stream) would hydrate with an empty stream_id and Foghorn
		// would match it by name — a confused-deputy if a later Retract for an
		// unrelated stream arrives. Require both tags; log + skip otherwise.
		if snap.streamID == "" {
			logger.WithField("name", name).Warn("HydrateAppliedManagedStreams: skipping stream with owner tag but missing fw:stream:<id>; not adopting as managed")
			continue
		}
		appliedManagedStreams.Lock()
		appliedManagedStreams.m[name] = snap
		appliedManagedStreams.Unlock()
		hydrated++
	}
	if hydrated > 0 {
		logger.WithField("count", hydrated).Info("HydrateAppliedManagedStreams: recovered managed-stream ownership from Mist config")
	}
}

// mistEntryHasOwnerTag inspects a Mist stream config entry (as returned by
// ConfigBackup) for the managed-stream owner tag. Tags are stored as a
// []string in newly-added streams; older configs may surface them as
// []interface{} after JSON round-trip.
func mistEntryHasOwnerTag(entry map[string]interface{}) bool {
	raw, ok := entry["tags"]
	if !ok {
		return false
	}
	switch tags := raw.(type) {
	case []string:
		return slices.Contains(tags, managedStreamOwnerTag)
	case []interface{}:
		for _, t := range tags {
			if s, ok := t.(string); ok && s == managedStreamOwnerTag {
				return true
			}
		}
	}
	return false
}

func mistEntryToSnapshot(entry map[string]interface{}) managedStreamLocalSnapshot {
	snap := managedStreamLocalSnapshot{}
	if s, ok := entry["source"].(string); ok {
		snap.source = s
	}
	if v, ok := entry["always_on"].(bool); ok {
		snap.alwaysOn = v
	}
	if v, ok := entry["realtime"].(bool); ok {
		snap.realtime = v
	}
	if v, ok := entry["stop_sessions"].(bool); ok {
		snap.stopSessions = v
	}
	// Tags arrive as []interface{} after JSON unmarshal; normalize to []string
	// + ensure stable order so the snapshot equality compare on the next
	// Apply matches the in-process write path.
	switch raw := entry["tags"].(type) {
	case []string:
		snap.tags = normalizeTags(raw, "")
	case []interface{}:
		out := make([]string, 0, len(raw))
		for _, t := range raw {
			if s, ok := t.(string); ok {
				out = append(out, s)
			}
		}
		snap.tags = normalizeTags(out, "")
	}
	for _, tag := range snap.tags {
		if rest, ok := strings.CutPrefix(tag, "ingest:"); ok {
			snap.ingestMode = rest
		}
		if rest, ok := strings.CutPrefix(tag, managedStreamIDTagPrefix); ok {
			snap.streamID = rest
		}
	}
	return snap
}

// snapshotAppliedManagedStreamsForRegister returns the current applied set
// in the wire shape Foghorn consumes on (re)connect. Called from the
// client's Register-build path so Foghorn can hydrate its lastSent map
// after its own restart — without this, a Foghorn restart followed by a
// DB-row removal would leave the Mist config in place forever (the
// reconciler's retract diff would never see the stream).
func snapshotAppliedManagedStreamsForRegister() []*ipcpb.AppliedManagedStream {
	appliedManagedStreams.Lock()
	defer appliedManagedStreams.Unlock()
	if len(appliedManagedStreams.m) == 0 {
		return nil
	}
	out := make([]*ipcpb.AppliedManagedStream, 0, len(appliedManagedStreams.m))
	for name, snap := range appliedManagedStreams.m {
		out = append(out, &ipcpb.AppliedManagedStream{
			Name:       name,
			Source:     snap.source,
			AlwaysOn:   snap.alwaysOn,
			IngestMode: snap.ingestMode,
			StreamId:   snap.streamID,
		})
	}
	return out
}

// handleApplyManagedStream materializes a concrete Mist stream config from
// Foghorn's ApplyManagedStream command. Idempotent: a re-Apply with
// identical fields is a no-op. A change in any field triggers AddStreams
// (which Mist treats as a replace).
func handleApplyManagedStream(logger logging.Logger, req *ipcpb.ApplyManagedStream) {
	if req == nil || req.GetName() == "" || req.GetSource() == "" {
		return
	}
	cfg := currentConfig
	if cfg == nil {
		logger.Warn("config not initialized; cannot apply managed stream")
		return
	}

	// Tags carry both the owner marker and the stream_id. The stream_id
	// tag lets post-restart hydration from Mist config recover the same
	// stream_id Foghorn used at Apply time so Foghorn's lastSent map
	// (keyed by stream_id) stays aligned with sidecar's applied map
	// (keyed by Mist stream name).
	tags := normalizeTags(req.GetTags(), req.GetStreamId())

	snapshot := managedStreamLocalSnapshot{
		source:       req.GetSource(),
		alwaysOn:     req.GetAlwaysOn(),
		realtime:     req.GetRealtime(),
		stopSessions: req.GetStopSessions(),
		tags:         tags,
		ingestMode:   req.GetIngestMode(),
		streamID:     req.GetStreamId(),
	}

	appliedManagedStreams.Lock()
	prev, hadPrev := appliedManagedStreams.m[req.GetName()]
	if hadPrev && prev.equals(snapshot) {
		appliedManagedStreams.Unlock()
		return
	}
	appliedManagedStreams.Unlock()

	mistClient := mist.NewClient(logger)
	if cfg.MistServerURL != "" {
		mistClient.BaseURL = cfg.MistServerURL
	}

	entry := map[string]interface{}{
		"name":          req.GetName(),
		"source":        req.GetSource(),
		"always_on":     req.GetAlwaysOn(),
		"realtime":      req.GetRealtime(),
		"stop_sessions": req.GetStopSessions(),
		"tags":          tags,
	}

	if err := mistClient.AddStreams(map[string]map[string]interface{}{
		req.GetName(): entry,
	}); err != nil {
		logger.WithFields(logging.Fields{
			"stream_name": req.GetName(),
			"stream_id":   req.GetStreamId(),
			"error":       err,
		}).Error("Failed to apply managed stream to Mist")
		return
	}
	if err := mistClient.Save(); err != nil {
		logger.WithFields(logging.Fields{
			"stream_name": req.GetName(),
			"stream_id":   req.GetStreamId(),
			"error":       err,
		}).Error("Failed to persist managed stream in Mist config")
		return
	}

	appliedManagedStreams.Lock()
	appliedManagedStreams.m[req.GetName()] = snapshot
	appliedManagedStreams.Unlock()

	logger.WithFields(logging.Fields{
		"stream_name": req.GetName(),
		"stream_id":   req.GetStreamId(),
		"ingest_mode": req.GetIngestMode(),
		"always_on":   req.GetAlwaysOn(),
	}).Info("Applied managed stream to Mist")
}

// handleRetractManagedStream removes a concrete Mist stream config when
// Foghorn signals it should no longer run on this node. Owner-scope safety:
// only deletes streams the sidecar previously Applied. A Retract for an
// unknown name is silently ignored — protects tenant push/pull streams from
// any stale or spoofed Retract.
func handleRetractManagedStream(logger logging.Logger, req *ipcpb.RetractManagedStream) {
	if req == nil || req.GetName() == "" {
		return
	}
	appliedManagedStreams.Lock()
	_, owned := appliedManagedStreams.m[req.GetName()]
	appliedManagedStreams.Unlock()
	if !owned {
		logger.WithFields(logging.Fields{
			"stream_name": req.GetName(),
			"stream_id":   req.GetStreamId(),
		}).Debug("Retract ignored: stream not in managed set")
		return
	}

	cfg := currentConfig
	if cfg == nil {
		return
	}
	mistClient := mist.NewClient(logger)
	if cfg.MistServerURL != "" {
		mistClient.BaseURL = cfg.MistServerURL
	}
	if err := mistClient.DeleteStream(req.GetName()); err != nil {
		logger.WithFields(logging.Fields{
			"stream_name": req.GetName(),
			"stream_id":   req.GetStreamId(),
			"error":       err,
		}).Error("Failed to retract managed stream from Mist")
		return
	}
	if err := mistClient.Save(); err != nil {
		logger.WithFields(logging.Fields{
			"stream_name": req.GetName(),
			"stream_id":   req.GetStreamId(),
			"error":       err,
		}).Error("Failed to persist managed stream retract in Mist config")
		return
	}

	appliedManagedStreams.Lock()
	delete(appliedManagedStreams.m, req.GetName())
	appliedManagedStreams.Unlock()

	logger.WithFields(logging.Fields{
		"stream_name": req.GetName(),
		"stream_id":   req.GetStreamId(),
	}).Info("Retracted managed stream from Mist")
}

// normalizeTags dedups input tags, guarantees the owner marker is
// present, and adds a stream_id tag when known. Stable order so the
// idempotency compare on re-Apply doesn't trip on tag ordering jitter.
func normalizeTags(in []string, streamID string) []string {
	seen := make(map[string]bool, len(in)+2)
	out := make([]string, 0, len(in)+2)
	add := func(t string) {
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, t := range in {
		add(t)
	}
	add(managedStreamOwnerTag)
	if streamID != "" {
		add(managedStreamIDTagPrefix + streamID)
	}
	slices.Sort(out)
	return out
}
