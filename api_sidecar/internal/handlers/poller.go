package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/control"
	"frameworks/api_sidecar/internal/dtsh"
	"frameworks/api_sidecar/internal/leases"
	"frameworks/api_sidecar/internal/updater"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// clientLifecycleTickStride spaces client-lifecycle emissions across monitorNodes ticks.
// monitorNodes ticks every 10s; stride 6 yields a 60s client-QoE cadence while keeping
// node and stream lifecycle on the 10s cadence. The clients API is QoE/diagnostic only;
// USER_NEW/USER_END remain authoritative for connect/disconnect and billing.
const clientLifecycleTickStride = 6

var nodeMetricsForwardTimeout = 5 * time.Second

const (
	admittedRuntimeMinimumAge       = 30 * time.Second
	admittedRuntimeMissingDwell     = 20 * time.Second
	maxMissingRuntimeReportsPerPoll = 64
	maxStreamObservationEntries     = 8192
)

type admittedRuntimeIdentity struct {
	runtimeName  string
	generation   string
	connectorPID int64
}

// mistNodeRuntime is an immutable node/client identity with a cancellable task
// lifetime. Poll work captures one runtime, so a node replacement can never
// pair the new node ID with the old node's Mist client.
type mistNodeRuntime struct {
	generation        uint64
	nodeID            string
	baseURL           string
	client            *mist.Client
	clientMu          sync.Mutex
	acceleratorClient *mist.Client
	acceleratorMu     sync.Mutex
	ctx               context.Context
	cancel            context.CancelFunc

	taskMu   sync.Mutex
	stopping bool
	tasks    sync.WaitGroup
}

func newMistNodeRuntime(generation uint64, nodeID, baseURL, username, password string) *mistNodeRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	client := mist.NewClient(monitorLogger)
	client.BaseURL = baseURL
	client.Username = username
	client.Password = password
	acceleratorClient := mist.NewClient(monitorLogger)
	acceleratorClient.BaseURL = baseURL
	acceleratorClient.Username = username
	acceleratorClient.Password = password
	return &mistNodeRuntime{
		generation:        generation,
		nodeID:            nodeID,
		baseURL:           baseURL,
		client:            client,
		acceleratorClient: acceleratorClient,
		ctx:               ctx,
		cancel:            cancel,
	}
}

func (runtime *mistNodeRuntime) launch(task func(context.Context)) bool {
	runtime.taskMu.Lock()
	defer runtime.taskMu.Unlock()
	if runtime.stopping {
		return false
	}
	runtime.tasks.Add(1)
	go func() {
		defer runtime.tasks.Done()
		task(runtime.ctx)
	}()
	return true
}

func (runtime *mistNodeRuntime) stop() {
	runtime.beginStop()
	runtime.waitStopped()
}

func (runtime *mistNodeRuntime) beginStop() {
	runtime.taskMu.Lock()
	if !runtime.stopping {
		runtime.stopping = true
		runtime.cancel()
	}
	runtime.taskMu.Unlock()
}

func (runtime *mistNodeRuntime) waitStopped() {
	runtime.tasks.Wait()
}

func (runtime *mistNodeRuntime) requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(runtime.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func isInternalMistRuntimeStream(streamName string) bool {
	return streamident.Parse(streamName).Kind == streamident.KindArtifactProcessing
}

// ClipInfo represents local clip metadata for VOD serving
type ClipInfo struct {
	FilePath     string
	StreamName   string
	Format       string
	SizeBytes    uint64
	CreatedAt    time.Time
	S3URL        string
	SegmentCount int  // Number of segments (for DVR recordings)
	HasDtsh      bool // True if .dtsh index file exists locally
	AccessCount  int
	LastAccessed time.Time
	ArtifactType ipcpb.ArtifactEvent_ArtifactType
}

// PrometheusMonitor handles monitoring of MistServer Prometheus endpoints
type PrometheusMonitor struct {
	mutex         sync.RWMutex
	updateChannel chan models.NodeUpdate
	stopChannel   chan bool
	// MistServer API authentication
	mistUsername    string // Username for MistServer API
	mistAPIPassword string // Password for MistServer API
	// Single node info (since each sidecar monitors one node)
	nodeID        string
	baseURL       string // Internal MistServer URL (for API calls)
	edgePublicURL string // Public edge URL (for client-facing BaseUrl)
	latitude      *float64
	longitude     *float64
	location      string
	lastSeen      time.Time
	isHealthy     bool
	lastJSONData  map[string]any // Store last fetched JSON data
	// Artifact index for fast VOD lookups
	artifactIndex    map[string]*ClipInfo // clipHash -> ClipInfo
	lastArtifactScan time.Time
	// artifactIndexTrusted is set once a COMPLETE filesystem scan has populated artifactIndex. A scan
	// that hit a directory-read error is not allowed to replace the index (last-good is retained), and
	// until the first complete scan the reported snapshot is marked incomplete so Foghorn does not
	// treat an empty/partial index as an authoritative "node holds nothing" and orphan real copies.
	artifactIndexTrusted bool
	// artifactScanHealthy tracks the CURRENT scan's health, SEPARATELY from the sticky artifactIndexTrusted.
	// A complete scan sets it true; an incomplete scan (lost mount / read error) sets it FALSE while the
	// last-good index is retained for observability. captureArtifactSnapshot flags the report incomplete
	// whenever this is false, so Foghorn cordons the node from artifact routing instead of routing to a
	// stale inventory the disk can no longer back. Without this, a failed scan after a good one would
	// still report "complete" (trusted stays true) and leave vanished copies routable.
	artifactScanHealthy bool
	// lastScanGen is the generation of the newest scan whose result has been published. A slower OLDER
	// scan (lower generation) that finishes after a newer one must NOT overwrite it.
	lastScanGen uint64

	// nodeRuntime is the sole authority for node identity and Mist API clients.
	// The visible fields above are a status snapshot only.
	nodeRuntimeMu             sync.RWMutex
	nodeLifecycleMu           sync.Mutex
	nodeRuntime               *mistNodeRuntime
	nextNodeGeneration        uint64
	stopOnce                  sync.Once
	stopped                   atomic.Bool
	monitorWorkers            sync.WaitGroup
	nodeMetricsWake           chan struct{}
	nodeMetricsVersion        atomic.Uint64
	nodeMetricsSendInFlight   atomic.Bool
	streamObservationNext     atomic.Uint64
	streamObservationFloor    uint64
	streamObservationOverflow bool
	streamObservationApply    sync.Mutex
	streamObservationMu       sync.Mutex
	streamObservationLast     map[string]uint64
	streamObservationIssued   map[string]uint64

	// Bandwidth rate calculation state
	lastBwUp     uint64
	lastBwDown   uint64
	lastPollTime time.Time

	// Vanish-diff state: internal names seen on the previous successful
	// active_streams poll. A stream present last poll but absent now has
	// lost its Mist session; Mist emits no per-stream trigger for that
	// edge, so the poller reports it as an explicit offline lifecycle
	// update. seeded distinguishes "first successful poll after start"
	// (only record, never emit) from "everything genuinely vanished".
	// pendingOfflineNames holds vanished names whose offline send has not
	// succeeded yet; they retry each poll until delivered or the stream
	// reappears.
	streamDiffMu            sync.Mutex
	lastActiveInternalNames map[string]struct{}
	pendingOfflineNames     map[string]struct{}
	targetedActiveNames     map[string]uint64
	admittedRuntimeMissing  map[admittedRuntimeIdentity]time.Time
	activeStreamsSeeded     bool
	reconciliationNodeID    string
	loadAdmittedGenerations func() ([]control.AdmittedIngestGeneration, error)
	sendControlTrigger      func(*ipcpb.MistTrigger, logging.Logger) (*control.MistTriggerResult, error)
	sendControlTriggerCtx   func(context.Context, *ipcpb.MistTrigger, logging.Logger) (*control.MistTriggerResult, error)
	markGenerationEnded     func(string, string, int64) error
	// emitStreamLifecycle is spawned with `go` every tick; a slow Mist
	// API response could overlap runs and a late finisher would swap in
	// a stale set. Ticks that find a run in flight are dropped.
	streamLifecycleInFlight atomic.Bool

	// The controller WebSocket is a lossy change detector. Its
	// generation fence prevents a replaced node connection from refreshing the
	// new node with frames or API results from the old socket.
	streamWSMu         sync.Mutex
	streamWSGeneration uint64
	streamWSCancel     func()
	streamWSDone       <-chan struct{}
	streamWSQueue      *streamWSRefreshQueue
	streamWSFullSweep  func(string, string) bool
}

var prometheusMonitor *PrometheusMonitor
var monitorLogger logging.Logger
var monitorInitMu sync.Mutex
var fileStabilityThreshold = 10 * time.Second

// Artifact-report ordering. Node metrics are coalesced, but artifact mutations
// can still race a report. Capture the whole-node snapshot and its sequence
// together so a newer snapshot cannot be paired with an older sequence.
var (
	artifactReportMu  sync.Mutex
	artifactReportSeq int64
)

// artifactScanGen issues a strictly-monotonic generation to each scanLocalArtifacts call at its START,
// so a slow older scan's result can be discarded at commit time if a newer scan already published
// (generation-checked publication — prevents overlapping scans from committing out of order).
var artifactScanGen atomic.Uint64

// artifactMutationGen is bumped by every POINT mutation of artifactIndex (a deletion or a DTSH update).
// A scan records it at start and rejects its own publish if it changed, because a scan enumerates disk
// WITHOUT the lock: a delete/DTSH that committed after enumeration but before publication would
// otherwise be silently reverted by the scan replacing the whole index. Generation-ordering alone only
// orders scan-vs-scan; this orders scan-vs-point-mutation.
var artifactMutationGen atomic.Uint64

// forgetArtifact removes one materialized copy from the authoritative local
// inventory and invalidates any disk scan that may have enumerated it before
// the bytes disappeared. Every path that reports ArtifactDeleted must call this
// before stamping/sending the deletion event.
func forgetArtifact(artifactHash string) {
	if prometheusMonitor == nil {
		return
	}
	prometheusMonitor.mutex.Lock()
	delete(prometheusMonitor.artifactIndex, artifactHash)
	// Bump even when the entry was already absent: an in-flight filesystem scan
	// may have observed the file before its successful deletion.
	artifactMutationGen.Add(1)
	prometheusMonitor.mutex.Unlock()
}

// captureArtifactSnapshot returns the current whole-node artifact snapshot paired atomically with a
// fresh monotonic sequence and node-clock capture time, so sequence/time order matches the snapshot. incomplete is true
// when the inventory is not authoritative — either no COMPLETE scan has ever populated the index
// (artifactIndexTrusted false) OR the most recent scan hit a traversal/mount failure
// (artifactScanHealthy false). Foghorn must then NOT apply the snapshot as an authoritative whole-node
// inventory, and cordons the node from artifact routing, because a partial/empty/stale list would
// orphan real copies. A healthy complete scan clears both flags.
func captureArtifactSnapshot() (artifacts []*ipcpb.StoredArtifact, seq, reportedAtMs int64, incomplete bool) {
	artifactReportMu.Lock()
	defer artifactReportMu.Unlock()
	artifactReportSeq++
	trusted, healthy := true, true
	var arts []*ipcpb.StoredArtifact
	if prometheusMonitor != nil {
		// Read the readiness flags and the inventory under ONE lock acquisition, so an incomplete scan
		// committing concurrently can never yield incomplete=false paired with a stale inventory.
		prometheusMonitor.mutex.RLock()
		trusted = prometheusMonitor.artifactIndexTrusted
		healthy = prometheusMonitor.artifactScanHealthy
		arts = storedArtifactsLocked()
		reportedAtMs = time.Now().UnixMilli()
		prometheusMonitor.mutex.RUnlock()
	} else {
		reportedAtMs = time.Now().UnixMilli()
	}
	// Incomplete when there has never been a complete scan (!trusted) OR the most recent scan failed
	// (!healthy). The latter re-arms Foghorn's artifact cordon after a disk disappears.
	return arts, artifactReportSeq, reportedAtMs, !trusted || !healthy
}

var (
	streamViewers = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_viewers",
			Help: "Number of viewers per stream",
		},
		[]string{"stream"},
	)

	streamBandwidthDown = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_bandwidth_down_bps",
			Help: "Download bandwidth in bytes per second",
		},
		[]string{"stream", "protocol", "host"},
	)

	streamBandwidthUp = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_bandwidth_up_bps",
			Help: "Upload bandwidth in bytes per second",
		},
		[]string{"stream", "protocol", "host"},
	)

	streamConnectionTime = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_connection_time_seconds",
			Help: "Connection duration in seconds",
		},
		[]string{"stream", "protocol", "host"},
	)

	streamPacketsTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_packets_total",
			Help: "Total packets processed",
		},
		[]string{"stream", "protocol", "host"},
	)

	streamPacketsLost = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_packets_lost",
			Help: "Total packets lost",
		},
		[]string{"stream", "protocol", "host"},
	)

	streamPacketsRetransmitted = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_packets_retransmitted",
			Help: "Total packets retransmitted",
		},
		[]string{"stream", "protocol", "host"},
	)

	mistSourcePIDObservations = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mist_source_pid_observations_total",
			Help: "Mist active-stream source PID field observations by payload shape",
		},
		[]string{"shape"},
	)
)

// streamClientKey identifies a per-client labelset on the bandwidth /
// connection / packet gauges so we can clean up entries for clients that
// have disappeared between polls.
type streamClientKey struct {
	stream, protocol, host string
}

// prev-poll observed labelsets for stale cleanup. Mist's GetClients
// response is the source of truth; anything present last poll but missing
// this poll must be removed from the GaugeVecs to avoid stale rows
// surviving past disconnect.
var (
	prevStreamsMu     sync.Mutex
	prevStreams       = map[string]struct{}{}
	prevStreamClients = map[streamClientKey]struct{}{}
)

func currentComponentVersions() []*ipcpb.EdgeComponentVersion {
	recorded := updater.ReadComponentVersions()
	versions := []*ipcpb.EdgeComponentVersion{{
		Component: "helmsman",
		Version: firstNonEmptyString(
			recorded["HELMSMAN_VERSION"],
			os.Getenv("HELMSMAN_VERSION"),
			version.ComponentVersion,
			version.Version,
		),
	}}
	for _, component := range []struct {
		name string
		keys []string
	}{
		{name: "mist", keys: []string{"MIST_VERSION", "MISTSERVER_VERSION"}},
		{name: "caddy", keys: []string{"CADDY_VERSION"}},
		{name: "config_schema", keys: []string{"CONFIG_SCHEMA_VERSION", "FRAMEWORKS_CONFIG_SCHEMA_VERSION"}},
	} {
		values := make([]string, 0, len(component.keys)*2)
		for _, key := range component.keys {
			values = append(values, recorded[key], os.Getenv(key))
		}
		if value := firstNonEmptyString(values...); value != "" {
			versions = append(versions, &ipcpb.EdgeComponentVersion{Component: component.name, Version: value})
		}
	}
	return versions
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// InitPrometheusMonitor initializes the Prometheus monitoring system with logger
func InitPrometheusMonitor(logger logging.Logger) {
	monitorInitMu.Lock()
	defer monitorInitMu.Unlock()

	monitorLogger = logger
	if prometheusMonitor != nil {
		return
	}

	mistAPIPassword := os.Getenv("MIST_API_PASSWORD")
	mistUsername := os.Getenv("MIST_API_USERNAME")

	if mistAPIPassword == "" {
		mistAPIPassword = "test"
	}
	if mistUsername == "" {
		mistUsername = "test"
	}

	prometheusMonitor = &PrometheusMonitor{
		mistUsername:            mistUsername,
		mistAPIPassword:         mistAPIPassword,
		updateChannel:           make(chan models.NodeUpdate, 10),
		stopChannel:             make(chan bool, 1),
		nodeMetricsWake:         make(chan struct{}, 1),
		isHealthy:               true,
		lastJSONData:            make(map[string]any),
		artifactIndex:           make(map[string]*ClipInfo),
		admittedRuntimeMissing:  make(map[admittedRuntimeIdentity]time.Time),
		loadAdmittedGenerations: control.ActiveAdmittedIngestGenerations,
		sendControlTrigger:      control.SendMistTrigger,
		sendControlTriggerCtx:   control.SendMistTriggerContext,
		markGenerationEnded:     control.MarkAdmittedIngestGenerationEndedExact,
	}

	monitorLogger.WithFields(logging.Fields{
		"mist_api_user": mistUsername,
	}).Info("Prometheus monitor initialized")

	// Start monitoring goroutines.
	prometheusMonitor.monitorWorkers.Add(3)
	go func() {
		defer prometheusMonitor.monitorWorkers.Done()
		prometheusMonitor.monitorNodes()
	}()
	go func() {
		defer prometheusMonitor.monitorWorkers.Done()
		prometheusMonitor.processUpdates()
	}()
	go func() {
		defer prometheusMonitor.monitorWorkers.Done()
		prometheusMonitor.forwardNodeMetricsLoop()
	}()
}

func (pm *PrometheusMonitor) currentNodeRuntime() *mistNodeRuntime {
	pm.nodeRuntimeMu.RLock()
	defer pm.nodeRuntimeMu.RUnlock()
	return pm.nodeRuntime
}

func (pm *PrometheusMonitor) nodeRuntimeCurrent(runtime *mistNodeRuntime) bool {
	if runtime == nil || pm.stopped.Load() || runtime.ctx.Err() != nil {
		return false
	}
	pm.nodeRuntimeMu.RLock()
	defer pm.nodeRuntimeMu.RUnlock()
	return pm.nodeRuntime == runtime
}

func (pm *PrometheusMonitor) detachNodeRuntime() *mistNodeRuntime {
	pm.nodeRuntimeMu.Lock()
	runtime := pm.nodeRuntime
	pm.nodeRuntime = nil
	pm.nodeRuntimeMu.Unlock()
	if runtime != nil {
		runtime.beginStop()
	}
	return runtime
}

func (pm *PrometheusMonitor) joinRetiredNodeRuntime(runtime *mistNodeRuntime, streamWSDone <-chan struct{}) {
	if runtime == nil && streamWSDone == nil {
		return
	}
	pm.monitorWorkers.Add(1)
	go func() {
		defer pm.monitorWorkers.Done()
		if streamWSDone != nil {
			<-streamWSDone
		}
		if runtime != nil {
			runtime.waitStopped()
		}
	}()
}

func (pm *PrometheusMonitor) resetNodeScopedStreamState(preserveReconciliation bool) {
	if !preserveReconciliation {
		pm.streamDiffMu.Lock()
		pm.lastActiveInternalNames = nil
		pm.pendingOfflineNames = make(map[string]struct{})
		pm.targetedActiveNames = make(map[string]uint64)
		pm.admittedRuntimeMissing = make(map[admittedRuntimeIdentity]time.Time)
		pm.activeStreamsSeeded = false
		pm.reconciliationNodeID = ""
		pm.streamDiffMu.Unlock()
	}

	pm.streamObservationApply.Lock()
	pm.streamObservationMu.Lock()
	pm.streamObservationLast = make(map[string]uint64)
	pm.streamObservationIssued = make(map[string]uint64)
	pm.streamObservationFloor = 0
	pm.streamObservationOverflow = false
	pm.streamObservationMu.Unlock()
	pm.streamObservationApply.Unlock()
}

func (pm *PrometheusMonitor) beginStreamObservation() uint64 {
	return pm.streamObservationNext.Add(1)
}

func (pm *PrometheusMonitor) beginTargetedStreamObservation(streamNames []string) uint64 {
	observation := pm.beginStreamObservation()
	if !pm.issueTargetedStreamObservation(observation, streamNames) {
		return 0
	}
	return observation
}

func (pm *PrometheusMonitor) issueTargetedStreamObservation(observation uint64, streamNames []string) bool {
	pm.streamObservationApply.Lock()
	defer pm.streamObservationApply.Unlock()
	pm.streamObservationMu.Lock()
	defer pm.streamObservationMu.Unlock()
	if pm.streamObservationIssued == nil {
		pm.streamObservationIssued = make(map[string]uint64)
	}
	if observation == 0 || observation < pm.streamObservationFloor || pm.streamObservationOverflow {
		return false
	}
	newEntries := 0
	for _, streamName := range streamNames {
		if pm.streamObservationIssued[streamName] > observation {
			return false
		}
		if _, exists := pm.streamObservationIssued[streamName]; !exists {
			newEntries++
		}
	}
	if len(pm.streamObservationIssued)+newEntries > maxStreamObservationEntries {
		pm.streamObservationLast = make(map[string]uint64)
		pm.streamObservationIssued = make(map[string]uint64)
		if observation > pm.streamObservationFloor {
			pm.streamObservationFloor = observation
		}
		pm.streamObservationOverflow = true
		return false
	}
	for _, streamName := range streamNames {
		pm.streamObservationIssued[streamName] = observation
	}
	return true
}

func (pm *PrometheusMonitor) claimStreamObservation(streamName string, observation uint64) bool {
	pm.streamObservationMu.Lock()
	defer pm.streamObservationMu.Unlock()
	if observation == 0 || observation < pm.streamObservationFloor {
		return false
	}
	if pm.streamObservationOverflow {
		return true
	}
	if pm.streamObservationLast == nil {
		pm.streamObservationLast = make(map[string]uint64)
	}
	if _, exists := pm.streamObservationLast[streamName]; !exists && len(pm.streamObservationLast) >= maxStreamObservationEntries {
		pm.streamObservationLast = make(map[string]uint64)
		pm.streamObservationIssued = make(map[string]uint64)
		if observation > pm.streamObservationFloor {
			pm.streamObservationFloor = observation
		}
		pm.streamObservationOverflow = true
		return true
	}
	if pm.streamObservationIssued[streamName] > observation || pm.streamObservationLast[streamName] > observation {
		return false
	}
	pm.streamObservationLast[streamName] = observation
	return true
}

// applyStreamObservation spans freshness claim through trigger send. Targeted
// issuance uses the same apply mutex, so once a newer observation is issued an
// older row cannot claim and then publish after it.
func (pm *PrometheusMonitor) applyStreamObservation(streamName string, observation uint64, apply func()) bool {
	pm.streamObservationApply.Lock()
	defer pm.streamObservationApply.Unlock()
	if !pm.claimStreamObservation(streamName, observation) {
		return false
	}
	apply()
	return true
}

func (pm *PrometheusMonitor) recordTargetedStreamPresence(streamName string, observation uint64) {
	internalName, ok := offlineReportableInternalName(streamName)
	if !ok || observation == 0 {
		return
	}
	pm.streamDiffMu.Lock()
	defer pm.streamDiffMu.Unlock()
	if pm.lastActiveInternalNames == nil {
		pm.lastActiveInternalNames = make(map[string]struct{})
	}
	if pm.targetedActiveNames == nil {
		pm.targetedActiveNames = make(map[string]uint64)
	}
	pm.lastActiveInternalNames[internalName] = struct{}{}
	if observation > pm.targetedActiveNames[streamName] {
		pm.targetedActiveNames[streamName] = observation
	}
	// A successful targeted read is authoritative for this stream even before
	// the first complete inventory. Its later absence must produce an offline
	// edge instead of being absorbed as bootstrap state.
	pm.activeStreamsSeeded = true
}

// reconcileStreamPresenceSnapshot merges only targeted reads that began after
// the authoritative poll. The returned runtime-name set is the single presence
// view used by lease, vanish, admission, and observation reconciliation for
// this poll, so a stream cannot be present for one subsystem and absent for
// another solely because their reconciliation ran at different times.
func (pm *PrometheusMonitor) reconcileStreamPresenceSnapshot(
	present map[string]struct{},
	observation uint64,
) (map[string]struct{}, []string) {
	reconciled := make(map[string]struct{}, len(present))
	for streamName := range present {
		reconciled[streamName] = struct{}{}
	}

	pm.streamDiffMu.Lock()
	defer pm.streamDiffMu.Unlock()
	for streamName, targetedObservation := range pm.targetedActiveNames {
		if targetedObservation > observation {
			reconciled[streamName] = struct{}{}
		} else {
			delete(pm.targetedActiveNames, streamName)
		}
	}

	presentInternal := make(map[string]struct{}, len(reconciled))
	for streamName := range reconciled {
		if internalName, ok := offlineReportableInternalName(streamName); ok {
			presentInternal[internalName] = struct{}{}
		}
	}
	if pm.pendingOfflineNames == nil {
		pm.pendingOfflineNames = make(map[string]struct{})
	}
	toReport := updatePendingOffline(
		pm.pendingOfflineNames,
		pm.lastActiveInternalNames,
		presentInternal,
		pm.activeStreamsSeeded,
	)
	pm.lastActiveInternalNames = presentInternal
	pm.activeStreamsSeeded = true
	return reconciled, toReport
}

func (pm *PrometheusMonitor) pruneStreamObservations(present map[string]struct{}, observation uint64) {
	pm.streamObservationApply.Lock()
	defer pm.streamObservationApply.Unlock()
	pm.streamObservationMu.Lock()
	defer pm.streamObservationMu.Unlock()
	if observation < pm.streamObservationFloor {
		return
	}
	// This authoritative inventory supersedes observations that began before
	// it, but not targeted refreshes issued while the poll was in flight.
	// Advancing to streamObservationNext would invalidate those in-flight rows
	// even though this inventory never observed their later change.
	if observation > pm.streamObservationFloor {
		pm.streamObservationFloor = observation
	}
	pm.streamObservationOverflow = len(present) > maxStreamObservationEntries
	if pm.streamObservationOverflow {
		clear(pm.streamObservationLast)
		clear(pm.streamObservationIssued)
		return
	}
	for name, lastObservation := range pm.streamObservationLast {
		if _, ok := present[name]; !ok && lastObservation <= observation {
			delete(pm.streamObservationLast, name)
		}
	}
	for name, issuedObservation := range pm.streamObservationIssued {
		if _, ok := present[name]; !ok && issuedObservation <= observation {
			delete(pm.streamObservationIssued, name)
		}
	}
}

// AddNode adds a MistServer node to monitor
// baseURL is internal MistServer URL, edgePublicURL is client-facing URL
func (pm *PrometheusMonitor) AddNode(nodeID, baseURL, edgePublicURL string) {
	pm.nodeLifecycleMu.Lock()
	if pm.stopped.Load() {
		pm.nodeLifecycleMu.Unlock()
		return
	}
	previousRuntime := pm.currentNodeRuntime()
	pm.streamDiffMu.Lock()
	preserveReconciliation := pm.reconciliationNodeID == nodeID
	pm.streamDiffMu.Unlock()
	preserveReconciliation = preserveReconciliation || previousRuntime != nil && previousRuntime.nodeID == nodeID
	streamWSDone := pm.detachStreamWS()
	retiredRuntime := pm.detachNodeRuntime()
	pm.nodeRuntimeMu.Lock()
	pm.nextNodeGeneration++
	generation := pm.nextNodeGeneration
	pm.nodeRuntimeMu.Unlock()
	runtime := newMistNodeRuntime(generation, nodeID, baseURL, pm.mistUsername, pm.mistAPIPassword)
	pm.resetNodeScopedStreamState(preserveReconciliation)
	pm.streamDiffMu.Lock()
	pm.reconciliationNodeID = nodeID
	pm.streamDiffMu.Unlock()

	pm.mutex.Lock()
	pm.nodeID = nodeID
	pm.baseURL = baseURL
	pm.edgePublicURL = edgePublicURL
	pm.lastSeen = time.Now()
	pm.isHealthy = false
	pm.latitude = nil
	pm.longitude = nil
	pm.location = ""
	pm.lastJSONData = make(map[string]any) // Clear previous data

	pm.mutex.Unlock()

	// Publish node identity and both client lanes as one immutable runtime.
	pm.nodeRuntimeMu.Lock()
	pm.nodeRuntime = runtime
	pm.nodeRuntimeMu.Unlock()

	pm.startStreamWS(runtime)
	pm.joinRetiredNodeRuntime(retiredRuntime, streamWSDone)
	pm.nodeLifecycleMu.Unlock()

	monitorLogger.WithFields(logging.Fields{
		"node_id":         nodeID,
		"base_url":        baseURL,
		"edge_public_url": edgePublicURL,
	}).Info("Added MistServer node for monitoring")
}

// RemoveNode removes a MistServer node from monitoring
func (pm *PrometheusMonitor) RemoveNode(nodeID string) {
	pm.nodeLifecycleMu.Lock()
	if pm.stopped.Load() {
		pm.nodeLifecycleMu.Unlock()
		return
	}
	streamWSDone := pm.detachStreamWS()
	retiredRuntime := pm.detachNodeRuntime()
	// Keep reconciliation evidence until the next AddNode reveals whether this
	// was a same-node replacement or a genuinely different node. The latter
	// clears it before publishing its runtime.
	pm.resetNodeScopedStreamState(true)

	pm.mutex.Lock()

	pm.nodeID = ""
	pm.baseURL = ""
	pm.edgePublicURL = ""
	pm.lastSeen = time.Time{}
	pm.isHealthy = false
	pm.latitude = nil
	pm.longitude = nil
	pm.location = ""
	pm.lastJSONData = make(map[string]any) // Clear previous data
	pm.mutex.Unlock()
	pm.joinRetiredNodeRuntime(retiredRuntime, streamWSDone)
	pm.nodeLifecycleMu.Unlock()

	monitorLogger.WithFields(logging.Fields{
		"node_id": nodeID,
	}).Info("Removed MistServer node from monitoring")
}

// TriggerImmediatePoll triggers immediate JSON and stream polling using the stored node
func (pm *PrometheusMonitor) TriggerImmediatePoll() {
	runtime := pm.currentNodeRuntime()
	if runtime == nil {
		return
	}
	runtime.launch(func(context.Context) { pm.emitNodeLifecycleRuntime(runtime) })
	runtime.launch(func(context.Context) { pm.emitStreamLifecycleRuntime(runtime) })
}

func (pm *PrometheusMonitor) TriggerArtifactReport() {
	runtime := pm.currentNodeRuntime()
	if runtime == nil {
		return
	}
	runtime.launch(func(context.Context) { pm.emitNodeLifecycleRuntime(runtime) })
}

// TriggerImmediatePoll triggers immediate polling if the monitor is initialized
func TriggerImmediatePoll() {
	if prometheusMonitor != nil {
		prometheusMonitor.TriggerImmediatePoll()
	}
}

func TriggerArtifactReport() {
	if prometheusMonitor != nil {
		prometheusMonitor.TriggerArtifactReport()
	}
}

// GetNodes returns the single monitored node (for compatibility)
func (pm *PrometheusMonitor) GetNodes() map[string]*models.NodeInfo {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	nodes := make(map[string]*models.NodeInfo)
	if pm.nodeID != "" {
		nodes[pm.nodeID] = &models.NodeInfo{
			NodeID:    pm.nodeID,
			BaseURL:   pm.baseURL,
			LastSeen:  pm.lastSeen,
			IsHealthy: pm.isHealthy,
			GeoData: geoip.GeoData{
				Latitude:  getFloat64PointerValue(pm.latitude),
				Longitude: getFloat64PointerValue(pm.longitude),
			},
			Location: pm.location,
		}
	}
	return nodes
}

// monitorNodes continuously monitors all registered nodes
func (pm *PrometheusMonitor) monitorNodes() {
	Ticker := time.NewTicker(10 * time.Second) // Monitor every 10 seconds
	defer Ticker.Stop()

	// Separate ticker for artifact scanning (less frequent to reduce disk I/O)
	artifactTicker := time.NewTicker(60 * time.Second)
	defer artifactTicker.Stop()

	var clientTickCount uint64

	// The initial startup scan is performed synchronously in Init (handlers.go); do NOT launch a second
	// one here — two concurrent startup scans could publish out of order. Periodic rescans run below.

	for {
		select {
		case <-Ticker.C:
			if runtime := pm.currentNodeRuntime(); runtime != nil {
				runtime.launch(func(context.Context) { pm.emitNodeLifecycleRuntime(runtime) })
				runtime.launch(func(context.Context) { pm.emitStreamLifecycleRuntime(runtime) })
				if clientTickCount%clientLifecycleTickStride == 0 {
					runtime.launch(func(context.Context) {
						if err := pm.emitClientLifecycleRuntime(runtime); err != nil {
							return
						}
					})
				}
				clientTickCount++
			}

		case <-artifactTicker.C:
			// Periodic artifact rescan to detect late-appearing .dtsh files
			if storagePath := os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH"); storagePath != "" {
				go scanLocalArtifacts(storagePath)
			}

		case <-pm.stopChannel:
			monitorLogger.Info("Stopping Prometheus monitor")
			return
		}
	}
}

// emitNodeLifecycle fetches metrics from a single node (JSON only)
func (pm *PrometheusMonitor) emitNodeLifecycle(nodeID, baseURL string) {
	runtime := pm.currentNodeRuntime()
	if runtime == nil || runtime.nodeID != nodeID || runtime.baseURL != baseURL {
		return
	}
	pm.emitNodeLifecycleRuntime(runtime)
}

func (pm *PrometheusMonitor) emitNodeLifecycleRuntime(runtime *mistNodeRuntime) {
	if !pm.nodeRuntimeCurrent(runtime) {
		return
	}
	// Fetch JSON data using Mist client (/{secret}.json)
	runtime.clientMu.Lock()
	jsonData, jsonErr := runtime.client.FetchJSONContext(runtime.ctx, "")
	runtime.clientMu.Unlock()
	if !pm.nodeRuntimeCurrent(runtime) {
		return
	}

	// Send update through channel
	update := models.NodeUpdate{
		NodeID:   runtime.nodeID,
		BaseURL:  runtime.baseURL,
		JSONData: jsonData,
		Error:    jsonErr,
	}

	select {
	case pm.updateChannel <- update:
	default:
		control.TriggersDropped.WithLabelValues("node_lifecycle", "channel_full").Inc()
		monitorLogger.WithFields(logging.Fields{
			"node_id": runtime.nodeID,
		}).Warn("Update channel full, dropping update for node")
	}
}

// emitStreamLifecycle fetches data from MistServer's TCP API directly
func (pm *PrometheusMonitor) emitStreamLifecycle(nodeID, baseURL string) {
	runtime := pm.currentNodeRuntime()
	if runtime == nil || runtime.nodeID != nodeID || runtime.baseURL != baseURL {
		return
	}
	pm.emitStreamLifecycleRuntime(runtime)
}

func (pm *PrometheusMonitor) emitStreamLifecycleRuntime(runtime *mistNodeRuntime) {
	pm.emitStreamLifecycleWithClient(
		runtime.ctx,
		runtime.nodeID,
		runtime.baseURL,
		runtime.client,
		&runtime.clientMu,
		func() bool { return pm.nodeRuntimeCurrent(runtime) },
	)
}

type streamLifecyclePollResult uint8

const (
	streamLifecyclePollFailed streamLifecyclePollResult = iota
	streamLifecyclePollContended
	streamLifecyclePollSucceeded
)

func (pm *PrometheusMonitor) emitStreamLifecycleWithClient(
	ctx context.Context,
	nodeID string,
	baseURL string,
	client *mist.Client,
	clientMu *sync.Mutex,
	current func() bool,
) streamLifecyclePollResult {
	if client == nil || !current() {
		return streamLifecyclePollFailed
	}
	if !pm.streamLifecycleInFlight.CompareAndSwap(false, true) {
		monitorLogger.WithField("node_id", nodeID).Debug("Stream lifecycle poll still in flight, dropping tick")
		return streamLifecyclePollContended
	}
	defer pm.streamLifecycleInFlight.Store(false)
	observation := pm.beginStreamObservation()

	monitorLogger.WithFields(logging.Fields{
		"api_url": baseURL + "/api2",
		"node_id": nodeID,
	}).Info("Fetching active streams from Mist API")

	pollStartedAt := time.Now()
	clientMu.Lock()
	apiResponse, err := client.GetActiveStreamsContext(ctx)
	clientMu.Unlock()
	if err != nil {
		if ctx.Err() != nil || !current() {
			return streamLifecyclePollFailed
		}
		monitorLogger.WithFields(logging.Fields{
			"api_url": baseURL + "/api2",
			"node_id": nodeID,
			"error":   err,
		}).Error("Failed to fetch active streams")
		return streamLifecyclePollFailed
	}
	if !current() {
		return streamLifecyclePollFailed
	}

	// Extract active streams data
	activeStreams, activeStreamsAuthoritative := activeStreamsFromResponse(apiResponse)
	present := make(map[string]struct{}, len(activeStreams))
	sourcePIDs := make(map[string]map[int64]struct{}, len(activeStreams))
	if activeStreamsAuthoritative {
		monitorLogger.WithFields(logging.Fields{
			"api_url": baseURL + "/api2",
			"node_id": nodeID,
			"count":   len(activeStreams),
		}).Info("Found active streams via Mist API")
		for streamName, streamData := range activeStreams {
			if !current() {
				return streamLifecyclePollFailed
			}
			present[streamName] = struct{}{}
			if streamInfo, ok := streamData.(map[string]any); ok {
				if pids, authoritative := sourcePIDsFromStreamData(streamInfo); authoritative {
					sourcePIDs[streamName] = pids
					if len(pids) == 0 {
						mistSourcePIDObservations.WithLabelValues("authoritative_empty").Inc()
					} else {
						mistSourcePIDObservations.WithLabelValues("authoritative_nonempty").Inc()
					}
				} else if _, present := streamInfo["sourcepids"]; present {
					mistSourcePIDObservations.WithLabelValues("malformed").Inc()
				} else {
					mistSourcePIDObservations.WithLabelValues("missing").Inc()
				}
				pm.applyStreamObservation(streamName, observation, func() {
					pm.processActiveStreamDataContext(ctx, nodeID, streamName, streamInfo)
				})
			}
		}
	} else {
		monitorLogger.WithFields(logging.Fields{
			"api_url": baseURL + "/api2",
			"node_id": nodeID,
		}).Warn("active_streams missing or malformed in Mist response; skipping stream-presence reconciliation this poll")
		return streamLifecyclePollFailed
	}
	presenceObservedAt := time.Now()
	if !current() {
		return streamLifecyclePollFailed
	}
	// A targeted refresh issued after this poll began is newer evidence for its
	// one stream. Merge it once, then give every reconciliation path the same
	// stable view; the next authoritative poll confirms or removes it.
	reconciledPresent, toReport := pm.reconcileStreamPresenceSnapshot(present, observation)
	// Reconcile source leases against Mist's authoritative view. Only fires
	// after a successful GetActiveStreams response with a well-formed
	// active_streams member — poll errors and malformed payloads returned
	// above. Without the shape gate, a malformed response would count as an
	// absent-stream observation, and two of those in a row release source
	// leases (ReconcileSources' missing-poll threshold) for streams Mist is
	// still serving.
	if tracker := leases.GlobalTracker(); tracker != nil {
		// Boot-recovery: streams open in Mist before Helmsman restarted have
		// no source lease yet because STREAM_SOURCE only fires once per
		// session. Install leases for everything Mist currently serves before
		// reconciliation releases anything.
		rebuildSourceLeasesFromMist(tracker, reconciledPresent, control.LookupActiveDVRByInternalName)

		// ReconcileSources forgets each released stream's source-registry entry
		// under the tracker lock (atomic with the lease removal), so no separate
		// registry forget is needed here — doing it separately could race a
		// concurrent STREAM_SOURCE reinstalling the lease+entry.
		if released := tracker.ReconcileSourcesAt(reconciledPresent, presenceObservedAt); len(released) > 0 {
			monitorLogger.WithField("released", released).Info("Source leases released by Mist reconciliation")
		}
	}
	// Vanish diff: report streams that disappeared since the previous
	// successful poll as explicit offline lifecycle updates. Without this,
	// nothing corrects stream_state_current when STREAM_END is delayed by
	// buffer drain or dropped on an expired stream-context cache — the
	// user-visible status stays "live". Only authoritative payloads reach
	// this point: synthesizing mass offlines from a malformed payload is
	// worse than leaving the residue to the ingest backstop.
	// Names stay pending until a send succeeds, so a transient Foghorn
	// failure retries on the next poll instead of silently dropping the
	// offline edge until the ingest backstop catches it minutes later.
	for _, internalName := range toReport {
		if !current() {
			return streamLifecyclePollFailed
		}
		result, err := pm.sendMistTriggerContext(ctx, buildOfflineLifecycleTrigger(nodeID, internalName))
		if err != nil || result == nil || result.Abort {
			monitorLogger.WithFields(logging.Fields{
				"error":         err,
				"internal_name": internalName,
				"aborted":       result != nil && result.Abort,
			}).Error("Failed to send offline stream lifecycle update to Foghorn")
			continue
		}
		pm.streamDiffMu.Lock()
		delete(pm.pendingOfflineNames, internalName)
		pm.streamDiffMu.Unlock()
	}
	pm.reconcileAdmittedRuntimePresenceContext(ctx, nodeID, reconciledPresent, sourcePIDs, presenceObservedAt)
	if !current() {
		return streamLifecyclePollFailed
	}

	pm.pruneStreamObservations(reconciledPresent, observation)
	pm.recordAuthoritativeStreamInventory(nodeID, baseURL, present, pollStartedAt)
	markMistActiveStreamsPolled()
	return streamLifecyclePollSucceeded
}

// activeStreamsFromResponse extracts the active_streams member of a Mist
// API response and reports whether it is authoritative. Mist's fillActive
// leaves the member JSON null when zero streams are active, so null is an
// authoritative empty set — while a missing key or a non-map value means
// the payload cannot be trusted as a stream-presence observation (treating
// it as empty would synthesize offline events and, via ReconcileSources'
// missing-poll counting, release source leases for live streams).
func activeStreamsFromResponse(apiResponse map[string]any) (map[string]any, bool) {
	raw, hasKey := apiResponse["active_streams"]
	if !hasKey {
		return nil, false
	}
	if raw == nil {
		return nil, true
	}
	activeStreams, ok := raw.(map[string]any)
	return activeStreams, ok
}

func sourcePIDsFromStreamData(streamData map[string]any) (map[int64]struct{}, bool) {
	raw, ok := streamData["sourcepids"]
	if !ok {
		return nil, false
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	pids := make(map[int64]struct{}, len(values))
	for _, value := range values {
		number, ok := value.(float64)
		if !ok || number <= 0 || number != float64(int64(number)) {
			return nil, false
		}
		pids[int64(number)] = struct{}{}
	}
	return pids, true
}

// offlineReportableInternalName maps a Mist runtime stream name to the
// internal name eligible for vanish-offline reporting. Only source streams
// (live+/pull+) participate — the population whose ending means "the
// stream went offline". Everything else is excluded as not
// self-describing: artifact surfaces (vod+/dvr+/processing+) vanish
// whenever their viewers leave, and bare names may be a mist-native
// stream but equally an unprefixed artifact key or a leaked playback_id.
// Foghorn's authority gate is positive-ownership-only, so a colliding
// bare token could never flip a stream owned elsewhere — but it could
// still spam the identity resolver with junk lookups, and bare streams
// have no ownership stamp to consume anyway. Bare streams keep their
// live-path lifecycle reporting; a stale live row for one is corrected by
// the ingest backstop instead of this fast path.
func offlineReportableInternalName(streamName string) (string, bool) {
	parsed := streamident.Parse(streamName)
	if parsed.IsSource() {
		return parsed.Concrete, true
	}
	return "", false
}

// updatePendingOffline merges newly vanished names into pending, drops
// pending names that reappeared (they are live again; reporting the stale
// vanish would fight their own refreshes), and returns the sorted names to
// report this poll. seeded=false (first successful poll) only records —
// nothing new is merged, but leftover pending names still report.
func updatePendingOffline(pending, prev, current map[string]struct{}, seeded bool) []string {
	if seeded {
		for _, name := range diffVanishedStreams(prev, current) {
			pending[name] = struct{}{}
		}
	}
	for name := range pending {
		if _, back := current[name]; back {
			delete(pending, name)
		}
	}
	toReport := make([]string, 0, len(pending))
	for name := range pending {
		toReport = append(toReport, name)
	}
	sort.Strings(toReport)
	return toReport
}

// diffVanishedStreams returns the names present in prev but absent from
// current, sorted for deterministic emission order.
func diffVanishedStreams(prev, current map[string]struct{}) []string {
	var vanished []string
	for name := range prev {
		if _, ok := current[name]; !ok {
			vanished = append(vanished, name)
		}
	}
	sort.Strings(vanished)
	return vanished
}

func (pm *PrometheusMonitor) reconcileAdmittedRuntimePresence(nodeID string, present map[string]struct{}, sourcePIDs map[string]map[int64]struct{}, now time.Time) {
	pm.reconcileAdmittedRuntimePresenceContext(context.Background(), nodeID, present, sourcePIDs, now)
}

func (pm *PrometheusMonitor) reconcileAdmittedRuntimePresenceContext(ctx context.Context, nodeID string, present map[string]struct{}, sourcePIDs map[string]map[int64]struct{}, now time.Time) {
	load := pm.loadAdmittedGenerations
	if load == nil {
		load = control.ActiveAdmittedIngestGenerations
	}
	records, err := load()
	if err != nil {
		monitorLogger.WithError(err).Warn("Failed to load admitted ingest generations for Mist reconciliation")
		return
	}

	live := make(map[admittedRuntimeIdentity]struct{}, len(records))
	eligible := make([]control.AdmittedIngestGeneration, 0)
	pm.streamDiffMu.Lock()
	if pm.admittedRuntimeMissing == nil {
		pm.admittedRuntimeMissing = make(map[admittedRuntimeIdentity]time.Time)
	}
	for _, record := range records {
		identity := admittedRuntimeIdentity{
			runtimeName:  record.RuntimeName,
			generation:   record.Generation,
			connectorPID: record.ConnectorPID,
		}
		live[identity] = struct{}{}
		if _, runtimePresent := present[record.RuntimeName]; runtimePresent {
			pids, authoritative := sourcePIDs[record.RuntimeName]
			if !authoritative {
				// A mixed-version or malformed Mist response cannot prove this connector
				// vanished. Reset dwell so only consecutive authoritative observations reap it.
				delete(pm.admittedRuntimeMissing, identity)
				continue
			}
			if _, sourcePresent := pids[record.ConnectorPID]; sourcePresent {
				delete(pm.admittedRuntimeMissing, identity)
				continue
			}
		}
		// Admission age is a grace period, not part of the absence dwell. A
		// connected encoder may boot its Mist buffer before it claims a track;
		// require the complete minimum age before beginning consecutive
		// authoritative missing-PID observations.
		if now.Sub(record.UpdatedAt) < admittedRuntimeMinimumAge {
			delete(pm.admittedRuntimeMissing, identity)
			continue
		}
		missingSince := pm.admittedRuntimeMissing[identity]
		if missingSince.IsZero() {
			pm.admittedRuntimeMissing[identity] = now
			continue
		}
		if now.Before(missingSince.Add(admittedRuntimeMissingDwell)) {
			continue
		}
		if len(eligible) < maxMissingRuntimeReportsPerPoll {
			eligible = append(eligible, record)
		}
	}
	for identity := range pm.admittedRuntimeMissing {
		if _, ok := live[identity]; !ok {
			delete(pm.admittedRuntimeMissing, identity)
		}
	}
	pm.streamDiffMu.Unlock()

	markEnded := pm.markGenerationEnded
	if markEnded == nil {
		markEnded = control.MarkAdmittedIngestGenerationEndedExact
	}
	for _, record := range eligible {
		internalName, ok := offlineReportableInternalName(record.RuntimeName)
		if !ok {
			continue
		}
		trigger := buildAcknowledgedOfflineLifecycleTriggerAt(nodeID, internalName, record.Generation, record.ConnectorPID, now)
		result, sendErr := pm.sendMistTriggerContext(ctx, trigger)
		if sendErr != nil || result == nil || result.Abort {
			monitorLogger.WithError(sendErr).WithFields(logging.Fields{
				"runtime_name":      record.RuntimeName,
				"ingest_generation": record.Generation,
				"aborted":           result != nil && result.Abort,
			}).Warn("Failed to reconcile admitted runtime absent from Mist; will retry")
			continue
		}
		if markErr := markEnded(record.RuntimeName, record.Generation, record.ConnectorPID); markErr != nil {
			monitorLogger.WithError(markErr).WithFields(logging.Fields{
				"runtime_name":      record.RuntimeName,
				"ingest_generation": record.Generation,
			}).Warn("Foghorn retired missing runtime but local generation tombstone failed; will retry")
			continue
		}
		identity := admittedRuntimeIdentity{
			runtimeName:  record.RuntimeName,
			generation:   record.Generation,
			connectorPID: record.ConnectorPID,
		}
		pm.streamDiffMu.Lock()
		delete(pm.admittedRuntimeMissing, identity)
		pm.streamDiffMu.Unlock()
		monitorLogger.WithFields(logging.Fields{
			"runtime_name":      record.RuntimeName,
			"ingest_generation": record.Generation,
		}).Info("Reconciled admitted runtime absent from authoritative Mist inventory")
	}
}

// buildOfflineLifecycleTrigger reports a vanished stream to Foghorn. Only
// internal_name, node_id and the explicit offline status matter; stream_id
// and tenant_id are deliberately left unset — Foghorn's applyStreamContext
// enriches them (identity registry → stream cache → Commodore fallback),
// and the stream was live one poll interval ago so its context is warm.
// BufferState EMPTY and the zero counters describe the vanished session
// for the analytics row; Foghorn's offline branch does not read them.
func buildOfflineLifecycleTrigger(nodeID, internalName string) *ipcpb.MistTrigger {
	return buildOfflineLifecycleTriggerAt(nodeID, internalName, time.Now(), false)
}

func buildAcknowledgedOfflineLifecycleTriggerAt(nodeID, internalName, generation string, connectorPID int64, observedAt time.Time) *ipcpb.MistTrigger {
	trigger := buildOfflineLifecycleTriggerAt(nodeID, internalName, observedAt, true)
	lifecycle := trigger.GetStreamLifecycleUpdate()
	trigger.TriggerType = "INGEST_RUNTIME_ABSENT"
	trigger.TriggerPayload = &ipcpb.MistTrigger_IngestRuntimeAbsent{IngestRuntimeAbsent: &ipcpb.IngestRuntimeAbsent{
		Lifecycle: lifecycle, IngestGeneration: generation, IngestConnectorPid: connectorPID,
	}}
	return trigger
}

func buildOfflineLifecycleTriggerAt(nodeID, internalName string, observedAt time.Time, acknowledged bool) *ipcpb.MistTrigger {
	bufferState := "EMPTY"
	zeroInputs := uint32(0)
	zeroViewers := uint32(0)
	trigger := &ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE",
		NodeId:      nodeID,
		Timestamp:   observedAt.Unix(),
		Blocking:    acknowledged,
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				NodeId:       nodeID,
				InternalName: internalName,
				Status:       "offline",
				BufferState:  &bufferState,
				TotalInputs:  &zeroInputs,
				TotalViewers: &zeroViewers,
			},
		},
	}
	if acknowledged {
		requestID := uuid.NewString()
		trigger.RequestId = requestID
		trigger.TriggerUuid = requestID
		trigger.TriggerUnixMillis = observedAt.UnixMilli()
	}
	return trigger
}

// dvrRollingResolver maps a rolling-DVR playback token (the internal_name after
// the "dvr+" prefix) to its dvr_hash and rolling manifest path. Production passes
// control.LookupActiveDVRByInternalName; tests inject a stub. It is passed in per
// call so the seam carries no shared state.
type dvrRollingResolver func(internalName string) (dvrHash, manifestPath string, ok bool)

// rebuildSourceLeasesFromMist installs SourceLeases for active Mist streams
// that don't currently have one. Used in two situations:
//
//  1. Helmsman just restarted: Mist sessions survived, but the in-process
//     lease tracker is empty. Without this, cleanup unpauses after the
//     first reconciliation and can delete files still being read by Mist.
//  2. STREAM_SOURCE was not observed for some reason (e.g. Mist served a
//     cached source resolution). Reconciliation backfills.
//
// VOD streams resolve via prometheusMonitor.artifactIndex (file scan).
// DVR streams (dvr+<internal_name>) resolve via the recovered DVR manager's
// race-safe snapshot (internal_name → {dvr_hash, manifest path}). Anything
// unresolved gets a degraded source lease so cleanup paths see the protection
// — for DVR via DegradedDvrCleanupActive; VOD path-keyed cleanup is safe
// because nothing pins the (unknown) path, but operator DeleteVOD refuses
// because the asset-keyed lease entry exists.
func sourceOutcomeLabel(o leases.SourceOutcome) string {
	switch o {
	case leases.SourceCreated:
		return "created"
	case leases.SourceUpgraded:
		return "upgraded"
	default:
		return "unchanged"
	}
}

func rebuildSourceLeasesFromMist(tracker *leases.Tracker, present map[string]struct{}, resolveDVR dvrRollingResolver) {
	if tracker == nil {
		return
	}
	for streamName := range present {
		// Every present VOD/DVR stream is reconciled each poll through
		// tracker.ReconcileResolvedSource — there is no separate
		// has-lease/is-degraded pre-check, which could tear against a concurrent
		// release and leave a present stream unprotected. Reconciliation creates a
		// missing lease or upgrades a degraded one, but PRESERVES a resolved
		// (STREAM_SOURCE-authoritative) lease — the artifact scan may lag and must
		// never rewrite the authoritative path. Lease and source-registry entry
		// update under one lock. Streams the poller cannot resolve get a degraded
		// lease so destructive cleanup stays paused fail-closed.
		if internalName, ok := leases.ParseVODInternalName(streamName); ok {
			localPath := ""
			if prometheusMonitor != nil {
				prometheusMonitor.mutex.RLock()
				if info, found := prometheusMonitor.artifactIndex[internalName]; found {
					localPath = info.FilePath
				}
				prometheusMonitor.mutex.RUnlock()
			}
			key := leases.AssetKey{Type: "vod", Hash: internalName}
			if localPath != "" {
				paths := []string{localPath}
				for _, sidecar := range []string{localPath + ".dtsh", localPath + ".gop"} {
					if _, statErr := os.Stat(sidecar); statErr == nil {
						paths = append(paths, sidecar)
					}
				}
				entry := leases.SourceEntry{
					StreamName:   streamName,
					LocalPath:    localPath,
					AssetType:    "vod",
					InternalName: internalName,
				}
				// Reconcile: create if missing / upgrade if degraded, but never
				// overwrite a resolved (STREAM_SOURCE-authoritative) lease with
				// this possibly-stale scan path. Lease + registry update together.
				if outcome := tracker.ReconcileResolvedSource(streamName, paths, key, nil, entry); outcome != leases.SourceUnchanged {
					monitorLogger.WithFields(logging.Fields{
						"stream_name": streamName,
						"local_path":  localPath,
						"outcome":     sourceOutcomeLabel(outcome),
					}).Info("Rebuilt VOD source lease for active Mist stream")
				}
				continue
			}
			// VOD path unresolved: install a degraded asset-only lease so
			// operator DeleteVOD refuses. Path-keyed cleanup paths cannot
			// match this lease (no LocalPaths). Warn only on the transition to
			// degraded, not every poll.
			if tracker.EnsureDegradedSource(streamName, key) == leases.SourceCreated {
				monitorLogger.WithFields(logging.Fields{
					"stream_name":   streamName,
					"internal_name": internalName,
				}).Warn("Active VOD stream has no local-path mapping; installed degraded lease (DeleteVOD will refuse; path-keyed cleanup cannot protect)")
			}
			continue
		}
		// Active rolling DVR surface — dvr+<internal_name>. Resolve it
		// through the recovered DVR manager (jobs keyed by dvr_hash; a
		// race-safe snapshot lookup maps internal_name → {dvr_hash,
		// manifest path}). Chapter playback flows through vod+ now, not
		// dvr+.
		if internalName, ok := leases.ParseDVRRollingPlaybackID(streamName); ok {
			if dvrHash, manifestPath, resolved := resolveDVR(internalName); resolved && dvrHash != "" {
				// Resolved: install a NORMAL artifact-level lease pinning
				// the rolling manifest. The DVR Manager owns rotation and
				// per-segment cleanup, not the lease layer.
				key := leases.AssetKey{Type: "dvr", Hash: dvrHash}
				var paths []string
				if manifestPath != "" {
					paths = []string{manifestPath}
				}
				entry := leases.SourceEntry{
					StreamName: streamName,
					LocalPath:  manifestPath,
					AssetType:  "dvr",
					DvrHash:    dvrHash,
				}
				if outcome := tracker.ReconcileResolvedSource(streamName, paths, key, nil, entry); outcome != leases.SourceUnchanged {
					monitorLogger.WithFields(logging.Fields{
						"stream_name":   streamName,
						"internal_name": internalName,
						"dvr_hash":      dvrHash,
						"outcome":       sourceOutcomeLabel(outcome),
					}).Info("Rebuilt DVR source lease for active Mist stream")
				}
				continue
			}
			// Unresolved: the DVR job is not (yet) recovered, so we cannot
			// map the stream to a dvr_hash or manifest path. Install a
			// TYPE-ONLY degraded DVR lease so degradedDvrCount rises and
			// DegradedDvrCleanupActive() becomes true — DVR destructive
			// cleanup pauses fail-closed. Warn only on the transition.
			if tracker.EnsureDegradedSource(streamName, leases.AssetKey{Type: "dvr"}) == leases.SourceCreated {
				monitorLogger.WithFields(logging.Fields{
					"stream_name":   streamName,
					"internal_name": internalName,
				}).Warn("Active DVR stream has no recovered DVR job; installed degraded DVR lease (destructive DVR cleanup paused fail-closed)")
			}
		}
	}
}

// processActiveStreamData processes individual stream data from MistServer API
func (pm *PrometheusMonitor) processActiveStreamData(nodeID, streamName string, streamData map[string]any) {
	pm.processActiveStreamDataContext(context.Background(), nodeID, streamName, streamData)
}

func (pm *PrometheusMonitor) processActiveStreamDataContext(ctx context.Context, nodeID, streamName string, streamData map[string]any) {
	// Extract internal name from wildcard stream
	var internalName string
	if _, after, ok := strings.Cut(streamName, "+"); ok {
		internalName = after
	} else {
		internalName = streamName
	}

	// Get current viewers
	viewers := 0
	if v, ok := streamData["viewers"].(float64); ok {
		viewers = int(v)
	}

	// Get client count (total connections)
	clients := 0
	if c, ok := streamData["clients"].(float64); ok {
		clients = int(c)
	}

	// Get track count
	trackCount := 0
	if t, ok := streamData["tracks"].(float64); ok {
		trackCount = int(t)
	}

	// Get input count
	inputs := 0
	if i, ok := streamData["inputs"].(float64); ok {
		inputs = int(i)
	}

	// Get output count
	outputs := 0
	if o, ok := streamData["outputs"].(float64); ok {
		outputs = int(o)
	}

	// Get bandwidth data
	var upbytes, downbytes int64
	if ub, ok := streamData["upbytes"].(float64); ok {
		upbytes = int64(ub)
	}
	if db, ok := streamData["downbytes"].(float64); ok {
		downbytes = int64(db)
	}

	// Get timing data (for potential future use in health calculations)
	var firstMs, lastMs int64
	if fm, ok := streamData["firstms"].(float64); ok {
		firstMs = int64(fm)
	}
	if lm, ok := streamData["lastms"].(float64); ok {
		lastMs = int64(lm)
	}
	_ = firstMs // Available for future timing calculations
	_ = lastMs  // Available for future timing calculations

	// Parse health data for detailed track information
	var healthData map[string]any
	var trackDetails []map[string]any

	if health, ok := streamData["health"].(map[string]any); ok {
		healthData = health
		healthJSON, _ := json.MarshalIndent(health, "", "  ")
		monitorLogger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"stream_name": streamName,
			"body":        string(healthJSON),
		}).Debug("Raw health data for stream")

		// Parse individual tracks from health data
		for trackName, trackInfo := range health {
			if trackMap, ok := trackInfo.(map[string]any); ok {
				// Skip non-track fields (buffer, jitter, maxkeepaway)
				if trackName == "buffer" || trackName == "jitter" || trackName == "maxkeepaway" {
					continue
				}

				// Check if this looks like a track (has codec field)
				if codec, hasCodec := trackMap["codec"].(string); hasCodec {
					trackDetail := map[string]any{
						"track_name": trackName,
						"codec":      codec,
					}

					// Extract bitrate (this is the real-time accurate bitrate!)
					if kbits, ok := trackMap["kbits"].(float64); ok {
						trackDetail["bitrate_kbps"] = int(kbits)
						trackDetail["bitrate_bps"] = int64(kbits * 1000)
					}

					// Extract buffer info
					if buffer, ok := trackMap["buffer"].(float64); ok {
						trackDetail["buffer"] = int(buffer)
					}

					// Extract jitter
					if jitter, ok := trackMap["jitter"].(float64); ok {
						trackDetail["jitter"] = int(jitter)
					}

					// Determine track type and extract type-specific fields
					if strings.Contains(trackName, "video_") || codec == "H264" || codec == "H265" || codec == "AV1" {
						trackDetail["type"] = "video"

						// Extract video-specific fields
						if width, ok := trackMap["width"].(float64); ok {
							trackDetail["width"] = int(width)
						}
						if height, ok := trackMap["height"].(float64); ok {
							trackDetail["height"] = int(height)
						}
						if fpks, ok := trackMap["fpks"].(float64); ok {
							trackDetail["fps"] = fpks / 1000 // fpks is frames per kilosecond
						}
						if bframes, ok := trackMap["bframes"].(bool); ok {
							trackDetail["has_bframes"] = bframes
						}

						// Create resolution string
						if width, hasWidth := trackDetail["width"].(int); hasWidth {
							if height, hasHeight := trackDetail["height"].(int); hasHeight {
								trackDetail["resolution"] = fmt.Sprintf("%dx%d", width, height)
							}
						}

					} else if strings.Contains(trackName, "audio_") || codec == "AAC" || codec == "opus" || codec == "MP3" {
						trackDetail["type"] = "audio"

						// Extract audio-specific fields
						if channels, ok := trackMap["channels"].(float64); ok {
							trackDetail["channels"] = int(channels)
						}
						if rate, ok := trackMap["rate"].(float64); ok {
							trackDetail["sample_rate"] = int(rate)
						}

					} else if strings.Contains(trackName, "meta_") || codec == "JSON" {
						trackDetail["type"] = "meta"
					} else {
						trackDetail["type"] = "unknown"
					}

					// Extract timing/frame info from keys if available
					if keys, ok := trackMap["keys"].(map[string]any); ok {
						if frameMax, ok := keys["frames_max"].(float64); ok {
							trackDetail["frames_max"] = int(frameMax)
						}
						if frameMin, ok := keys["frames_min"].(float64); ok {
							trackDetail["frames_min"] = int(frameMin)
						}
					}

					trackDetails = append(trackDetails, trackDetail)
					monitorLogger.WithFields(logging.Fields{
						"node_id":     nodeID,
						"stream_name": streamName,
						"track_name":  trackName,
						"type":        trackDetail["type"],
						"codec":       codec,
						"bitrate":     trackDetail["bitrate_kbps"],
					}).Debug("Parsed track")
				}
			}
		}

		monitorLogger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"stream_name": streamName,
			"count":       len(trackDetails),
		}).Debug("Extracted tracks from health data")
	} else {
		monitorLogger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"stream_name": streamName,
		}).Warn("No health data found for stream")
	}

	// Get node geographic information for logging context
	pm.mutex.RLock()
	latitude := pm.latitude
	longitude := pm.longitude
	location := pm.location
	pm.mutex.RUnlock()

	// Log stream location context (geographic data not included in StreamLifecycle payload)
	geoContext := "unknown"
	if location != "" {
		geoContext = location
	} else if latitude != nil && longitude != nil {
		geoContext = fmt.Sprintf("%.2f,%.2f", *latitude, *longitude)
	}

	activeStreamLog := monitorLogger.WithFields(logging.Fields{
		"node_id":       nodeID,
		"stream_name":   streamName,
		"viewers":       viewers,
		"clients":       clients,
		"tracks":        trackCount,
		"inputs":        inputs,
		"outputs":       outputs,
		"upbytes":       upbytes,
		"downbytes":     downbytes,
		"health_tracks": len(trackDetails),
		"location":      geoContext,
	})
	if isInternalMistRuntimeStream(streamName) {
		activeStreamLog.Debug("Processing internal active stream")
		return
	}
	activeStreamLog.Info("Processing active stream")

	// Analytics data forwarded via MistTrigger below

	// Convert API response to MistTrigger using converter
	mistTrigger := convertStreamAPIToMistTrigger(nodeID, streamName, internalName, streamData, healthData, trackDetails, trackCount, monitorLogger)

	if _, err := pm.sendMistTriggerContext(ctx, mistTrigger); err != nil {
		monitorLogger.WithFields(logging.Fields{
			"error":         err,
			"internal_name": internalName,
		}).Error("Failed to send stream lifecycle update to Foghorn")
	}
}

func (pm *PrometheusMonitor) sendMistTriggerContext(ctx context.Context, trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
	if pm.sendControlTriggerCtx != nil {
		return pm.sendControlTriggerCtx(ctx, trigger, monitorLogger)
	}
	if pm.sendControlTrigger != nil {
		return pm.sendControlTrigger(trigger, monitorLogger)
	}
	return control.SendMistTriggerContext(ctx, trigger, monitorLogger)
}

// processUpdates processes node updates from the update channel
func (pm *PrometheusMonitor) processUpdates() {
	for {
		var update models.NodeUpdate
		var ok bool
		select {
		case <-pm.stopChannel:
			return
		case update, ok = <-pm.updateChannel:
			if !ok {
				return
			}
		}
		if pm.stopped.Load() {
			return
		}
		pm.mutex.Lock()

		// Check if this is our monitored node
		if pm.nodeID != update.NodeID || pm.baseURL != update.BaseURL {
			pm.mutex.Unlock()
			continue
		}

		// Update node information
		pm.lastSeen = time.Now()

		if update.Error != nil {
			pm.isHealthy = false
			monitorLogger.WithFields(logging.Fields{
				"node_id": update.NodeID,
				"error":   update.Error,
			}).Error("Error monitoring node")
		} else {
			pm.isHealthy = true
			pm.lastJSONData = update.JSONData // Store the fetched JSON data

			// Extract geographic coordinates from JSON data (only update if changed)
			if jsonData := update.JSONData; jsonData != nil {
				monitorLogger.WithFields(logging.Fields{
					"node_id":       update.NodeID,
					"has_json_data": true,
					"json_keys":     getMapKeys(jsonData),
				}).Debug("Processing JSON data from Mist metrics JSON endpoint")

				if locData, ok := jsonData["loc"].(map[string]any); ok {
					monitorLogger.WithFields(logging.Fields{
						"node_id":  update.NodeID,
						"loc_data": locData,
					}).Info("Found location data in Mist metrics JSON")

					oldLat := pm.latitude
					oldLon := pm.longitude
					oldLoc := pm.location

					if lat, ok := locData["lat"].(float64); ok {
						pm.latitude = &lat
					}
					if lon, ok := locData["lon"].(float64); ok {
						pm.longitude = &lon
					}
					if name, ok := locData["name"].(string); ok && name != "" {
						pm.location = name
					}

					monitorLogger.WithFields(logging.Fields{
						"node_id":      update.NodeID,
						"old_lat":      oldLat,
						"new_lat":      pm.latitude,
						"old_lon":      oldLon,
						"new_lon":      pm.longitude,
						"old_location": oldLoc,
						"new_location": pm.location,
					}).Info("Updated PrometheusMonitor location data")
				} else {
					// If no location data from MistServer, log it with details
					monitorLogger.WithFields(logging.Fields{
						"node_id":     update.NodeID,
						"json_keys":   getMapKeys(jsonData),
						"has_loc_key": jsonData["loc"] != nil,
					}).Warn("No location data from MistServer for node")
				}
			} else {
				monitorLogger.WithFields(logging.Fields{
					"node_id": update.NodeID,
				}).Error("No JSON data received from Mist metrics JSON endpoint")
			}
		}

		pm.nodeMetricsVersion.Add(1)
		pm.mutex.Unlock()

		// Forwarding has its own coalescing, deadline-bounded worker so a control
		// outage cannot block this single update consumer and fill updateChannel.
		pm.requestNodeMetricsForward()
	}
}

func (pm *PrometheusMonitor) requestNodeMetricsForward() {
	select {
	case pm.nodeMetricsWake <- struct{}{}:
	default:
	}
}

func (pm *PrometheusMonitor) forwardNodeMetricsLoop() {
	for {
		select {
		case <-pm.stopChannel:
			return
		case <-pm.nodeMetricsWake:
		}
		runtime := pm.currentNodeRuntime()
		if runtime == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(runtime.ctx, nodeMetricsForwardTimeout)
		pm.forwardNodeMetricsContext(ctx, runtime)
		cancel()
	}
}

// forwardNodeMetricsContext forwards the latest coalesced node snapshot. The
// immutable runtime prevents a replaced node's identity and data from mixing.
func (pm *PrometheusMonitor) forwardNodeMetricsContext(ctx context.Context, runtime *mistNodeRuntime) {
	if pm.stopped.Load() || !pm.nodeRuntimeCurrent(runtime) {
		return
	}
	pm.mutex.RLock()
	if pm.nodeID != runtime.nodeID {
		pm.mutex.RUnlock()
		return
	}
	jsonData := pm.lastJSONData
	version := pm.nodeMetricsVersion.Load()
	pm.mutex.RUnlock()
	// Capabilities from environment (fallback defaults: all true in dev)
	capIngest := os.Getenv("HELMSMAN_CAP_INGEST")
	capEdge := os.Getenv("HELMSMAN_CAP_EDGE")
	capStorage := os.Getenv("HELMSMAN_CAP_STORAGE")
	capProcessing := os.Getenv("HELMSMAN_CAP_PROCESSING")
	roles := rolesFromCapabilityFlags(capIngest, capEdge, capStorage, capProcessing)

	// Convert API response to MistTrigger using converter
	mistTrigger := pm.convertNodeAPIToMistTrigger(runtime.nodeID, jsonData, monitorLogger)

	// Enrich with Helmsman-specific capabilities, storage, limits
	enrichNodeLifecycleTrigger(mistTrigger, capIngest, capEdge, capStorage, capProcessing, roles)

	// Send
	if pm.stopped.Load() || !pm.nodeRuntimeCurrent(runtime) {
		return
	}
	send := pm.sendControlTriggerCtx
	if send == nil {
		send = control.SendMistTriggerContext
	}
	_, err, started := pm.sendNodeMetricsAttempt(ctx, runtime, version, send, mistTrigger)
	if !started {
		return
	}
	if err != nil {
		if ctx.Err() != nil || !pm.nodeRuntimeCurrent(runtime) {
			return
		}
		monitorLogger.WithError(err).Error("Failed to send node lifecycle update via gRPC")
		return
	}
	monitorLogger.WithFields(logging.Fields{
		"node_id":  runtime.nodeID,
		"bw_limit": mistTrigger.GetNodeLifecycleUpdate().GetBwLimit(),
		"ram_max":  mistTrigger.GetNodeLifecycleUpdate().GetRamMax(),
	}).Info("Sent node lifecycle update to Foghorn")
}

func (pm *PrometheusMonitor) sendNodeMetricsAttempt(
	ctx context.Context,
	runtime *mistNodeRuntime,
	version uint64,
	send func(context.Context, *ipcpb.MistTrigger, logging.Logger) (*control.MistTriggerResult, error),
	trigger *ipcpb.MistTrigger,
) (*control.MistTriggerResult, error, bool) {
	// gRPC SendMsg has no per-message cancellation once it enters the transport.
	// Quarantine at most one such attempt so the joined monitor worker still
	// honors its deadline and repeated updates cannot accumulate goroutines.
	if !pm.nodeMetricsSendInFlight.CompareAndSwap(false, true) {
		return nil, nil, false
	}
	type sendResult struct {
		result *control.MistTriggerResult
		err    error
	}
	done := make(chan sendResult, 1)
	go func() {
		result, err := send(ctx, trigger, monitorLogger)
		pm.nodeMetricsSendInFlight.Store(false)
		done <- sendResult{result: result, err: err}
		// If updates arrived while this attempt occupied the single transport
		// slot, schedule exactly one fresh snapshot after it becomes available.
		if pm.nodeMetricsVersion.Load() > version && pm.nodeRuntimeCurrent(runtime) && !pm.stopped.Load() {
			pm.requestNodeMetricsForward()
		}
	}()
	select {
	case result := <-done:
		return result.result, result.err, true
	case <-ctx.Done():
		return nil, ctx.Err(), true
	}
}

// Stop stops the Prometheus monitor
func (pm *PrometheusMonitor) Stop() {
	pm.stopOnce.Do(func() {
		pm.nodeLifecycleMu.Lock()
		pm.stopped.Store(true)
		streamWSDone := pm.detachStreamWS()
		retiredRuntime := pm.detachNodeRuntime()
		if pm.stopChannel != nil {
			close(pm.stopChannel)
		}
		pm.nodeLifecycleMu.Unlock()
		if streamWSDone != nil {
			<-streamWSDone
		}
		if retiredRuntime != nil {
			retiredRuntime.waitStopped()
		}
		pm.monitorWorkers.Wait()
		streamWSConnections.Set(0)
	})
}

// HTTP handlers for Prometheus monitoring endpoints

// GetPrometheusNodes returns information about all monitored nodes
func GetPrometheusNodes(c *gin.Context) {
	if prometheusMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Prometheus monitor not initialized"})
		return
	}

	prometheusMonitor.mutex.RLock()
	nodeInfo := map[string]any{
		"node_id":   prometheusMonitor.nodeID,
		"base_url":  prometheusMonitor.baseURL,
		"latitude":  prometheusMonitor.latitude,
		"longitude": prometheusMonitor.longitude,
		"location":  prometheusMonitor.location,
		"last_seen": prometheusMonitor.lastSeen,
		"healthy":   prometheusMonitor.isHealthy,
	}
	prometheusMonitor.mutex.RUnlock()

	c.JSON(http.StatusOK, gin.H{"nodes": []any{nodeInfo}})
}

// AddPrometheusNode adds a new node to monitor
func AddPrometheusNode(c *gin.Context) {
	if prometheusMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Prometheus monitor not initialized"})
		return
	}

	var request struct {
		NodeID        string   `json:"node_id" binding:"required"`
		BaseURL       string   `json:"base_url" binding:"required"`
		EdgePublicURL string   `json:"edge_public_url"` // Optional, falls back to base_url
		Latitude      *float64 `json:"latitude"`
		Longitude     *float64 `json:"longitude"`
		Location      string   `json:"location"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	edgeURL := request.EdgePublicURL
	if edgeURL == "" {
		edgeURL = request.BaseURL // Fallback to base_url if not provided
	}
	prometheusMonitor.AddNode(request.NodeID, request.BaseURL, edgeURL)

	c.JSON(http.StatusOK, gin.H{"message": "Node added successfully"})
}

// AddPrometheusNodeDirect adds a node directly to the monitor (not via HTTP)
// baseURL is the internal MistServer URL, edgePublicURL is the client-facing URL
func AddPrometheusNodeDirect(nodeID, baseURL, edgePublicURL string) {
	if prometheusMonitor != nil {
		prometheusMonitor.AddNode(nodeID, baseURL, edgePublicURL)
	}
}

// RemovePrometheusNode removes a node from monitoring
func RemovePrometheusNode(c *gin.Context) {
	if prometheusMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Prometheus monitor not initialized"})
		return
	}

	nodeID := c.Param("node_id")
	if nodeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node_id parameter required"})
		return
	}

	prometheusMonitor.RemoveNode(nodeID)
	c.JSON(http.StatusOK, gin.H{"message": "Node removed successfully"})
}

// getFloat64PointerValue safely dereferences a *float64, returning 0 if nil (for embedded structs)
func getFloat64PointerValue(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// getFloat64 safely converts any to float64
func getFloat64(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

// getInt64 safely converts any to int64
func getInt64(v any) int64 {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	if i, ok := v.(int64); ok {
		return i
	}
	return 0
}

// getString safely converts any to string
func getString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// getMapKeys returns the keys of a map[string]any for debugging
func getMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func (pm *PrometheusMonitor) emitClientLifecycle(nodeID, mistURL string) error {
	runtime := pm.currentNodeRuntime()
	if runtime == nil || runtime.nodeID != nodeID || runtime.baseURL != mistURL {
		return nil
	}
	return pm.emitClientLifecycleRuntime(runtime)
}

func (pm *PrometheusMonitor) emitClientLifecycleRuntime(runtime *mistNodeRuntime) error {
	if !pm.nodeRuntimeCurrent(runtime) {
		return nil
	}
	// Query MistServer clients API for detailed metrics using shared client
	runtime.clientMu.Lock()
	result, err := runtime.client.GetClientsContext(runtime.ctx)
	runtime.clientMu.Unlock()
	if err != nil {
		if runtime.ctx.Err() != nil || !pm.nodeRuntimeCurrent(runtime) {
			return err
		}
		monitorLogger.WithFields(logging.Fields{
			"error": err,
			"url":   runtime.baseURL + "/api2",
		}).Error("Failed to query MistServer clients API")
		return err
	}
	if !pm.nodeRuntimeCurrent(runtime) {
		return nil
	}
	nodeID := runtime.nodeID

	// Process client metrics.
	presentSessions := make(map[string]struct{})
	// Aggregate viewer count per stream this poll; Mist returns one row
	// per client connection, so we count rows ourselves rather than
	// Inc()'ing the gauge per row (which never reset between polls).
	currStreamViewers := map[string]int{}
	// Track per-client labelsets observed this poll for stale cleanup
	// of bandwidth/connection/packet gauges.
	currStreamClients := map[streamClientKey]struct{}{}
	if clients, ok := result["clients"].(map[string]any); ok {
		if data, ok := clients["data"].([]any); ok {
			fields, ok := clients["fields"].([]any)
			if !ok {
				monitorLogger.Error("Failed to parse client fields as []any")
				return err
			}

			// Map field names to indices
			fieldMap := make(map[string]int)
			for i, field := range fields {
				fieldStr, ok := field.(string)
				if !ok {
					monitorLogger.WithField("field", field).Error("Failed to parse field name as string")
					continue
				}
				fieldMap[fieldStr] = i
			}

			// Process each client connection
			for _, clientData := range data {
				if !pm.nodeRuntimeCurrent(runtime) {
					return nil
				}
				client, ok := clientData.([]any)
				if !ok {
					monitorLogger.WithField("clientData", clientData).Error("Failed to parse client data as []any")
					continue
				}

				// Safely extract required fields with bounds checking
				streamIdx, hasStream := fieldMap["stream"]
				protocolIdx, hasProtocol := fieldMap["protocol"]
				hostIdx, hasHost := fieldMap["host"]

				if !hasStream || !hasProtocol || !hasHost {
					monitorLogger.Error("Missing required fields in client data")
					continue
				}

				if streamIdx >= len(client) || protocolIdx >= len(client) || hostIdx >= len(client) {
					monitorLogger.Error("Client data array too short for required indices")
					continue
				}

				streamName, ok := client[streamIdx].(string)
				if !ok {
					monitorLogger.WithField("streamData", client[streamIdx]).Error("Failed to parse stream name as string")
					continue
				}
				if isInternalMistRuntimeStream(streamName) {
					continue
				}

				protocol, ok := client[protocolIdx].(string)
				if !ok {
					monitorLogger.WithField("protocolData", client[protocolIdx]).Error("Failed to parse protocol as string")
					continue
				}

				host, ok := client[hostIdx].(string)
				if !ok {
					monitorLogger.WithField("hostData", client[hostIdx]).Error("Failed to parse host as string")
					continue
				}

				// Update Prometheus metrics. streamViewers is aggregated
				// after the loop; here we just count rows per stream.
				currStreamViewers[streamName]++
				currStreamClients[streamClientKey{streamName, protocol, host}] = struct{}{}

				// Bandwidth metrics
				if idx, ok := fieldMap["downbps"]; ok {
					if downBps, ok := client[idx].(float64); ok {
						streamBandwidthDown.WithLabelValues(streamName, protocol, host).Set(downBps)
					}
				}
				if idx, ok := fieldMap["upbps"]; ok {
					if upBps, ok := client[idx].(float64); ok {
						streamBandwidthUp.WithLabelValues(streamName, protocol, host).Set(upBps)
					}
				}

				// Connection time
				if idx, ok := fieldMap["conntime"]; ok {
					if connTime, ok := client[idx].(float64); ok {
						streamConnectionTime.WithLabelValues(streamName, protocol, host).Set(connTime)
					}
				}

				// Packet statistics (support both old and new field names)
				if idx, ok := fieldMap["pktcount"]; ok {
					if pktCount, ok := client[idx].(float64); ok {
						streamPacketsTotal.WithLabelValues(streamName, protocol, host).Set(pktCount)
					}
				} else if idx, ok := fieldMap["packet_count"]; ok {
					if pktCount, ok := client[idx].(float64); ok {
						streamPacketsTotal.WithLabelValues(streamName, protocol, host).Set(pktCount)
					}
				}
				if idx, ok := fieldMap["pktlost"]; ok {
					if pktLost, ok := client[idx].(float64); ok {
						streamPacketsLost.WithLabelValues(streamName, protocol, host).Set(pktLost)
					}
				} else if idx, ok := fieldMap["packet_lost"]; ok {
					if pktLost, ok := client[idx].(float64); ok {
						streamPacketsLost.WithLabelValues(streamName, protocol, host).Set(pktLost)
					}
				}
				if idx, ok := fieldMap["pktretransmit"]; ok {
					if pktRetransmit, ok := client[idx].(float64); ok {
						streamPacketsRetransmitted.WithLabelValues(streamName, protocol, host).Set(pktRetransmit)
					}
				} else if idx, ok := fieldMap["packet_retransmit"]; ok {
					if pktRetransmit, ok := client[idx].(float64); ok {
						streamPacketsRetransmitted.WithLabelValues(streamName, protocol, host).Set(pktRetransmit)
					}
				}

				// Extract internal name from stream name
				internalName := streamName
				if idx := strings.Index(streamName, "+"); idx != -1 && idx+1 < len(streamName) {
					internalName = streamName[idx+1:]
				}

				// Extract client data directly for protobuf
				sessionID := getString(client[fieldMap["sessid"]])
				if sessionID != "" {
					presentSessions[sessionID] = struct{}{}
				}
				connectionTime := getFloat64(client[fieldMap["conntime"]])
				position := getFloat64(client[fieldMap["position"]])

				bandwidthIn := func() float64 {
					if idx, ok := fieldMap["upbps"]; ok {
						if v, ok := client[idx].(float64); ok {
							return v
						}
					}
					return 0
				}()

				bandwidthOut := func() float64 {
					if idx, ok := fieldMap["downbps"]; ok {
						if v, ok := client[idx].(float64); ok {
							return v
						}
					}
					return 0
				}()

				bytesDown := func() int64 {
					if idx, ok := fieldMap["down"]; ok {
						return getInt64(client[idx])
					}
					if idx, ok := fieldMap["bytes_down"]; ok {
						return getInt64(client[idx])
					}
					return 0
				}()

				bytesUp := func() int64 {
					if idx, ok := fieldMap["up"]; ok {
						return getInt64(client[idx])
					}
					if idx, ok := fieldMap["bytes_up"]; ok {
						return getInt64(client[idx])
					}
					return 0
				}()

				packetsSent := func() int64 {
					if idx, ok := fieldMap["pktcount"]; ok {
						return getInt64(client[idx])
					}
					if idx, ok := fieldMap["packet_count"]; ok {
						return getInt64(client[idx])
					}
					return 0
				}()

				packetsLost := func() int64 {
					if idx, ok := fieldMap["pktlost"]; ok {
						return getInt64(client[idx])
					}
					if idx, ok := fieldMap["packet_lost"]; ok {
						return getInt64(client[idx])
					}
					return 0
				}()

				packetsRetransmitted := func() int64 {
					if idx, ok := fieldMap["pktretransmit"]; ok {
						return getInt64(client[idx])
					}
					if idx, ok := fieldMap["packet_retransmit"]; ok {
						return getInt64(client[idx])
					}
					return 0
				}()

				// Convert API response to MistTrigger using converter
				mistTrigger := convertClientAPIToMistTrigger(nodeID, streamName, internalName, protocol, host, sessionID, connectionTime, position, bandwidthIn, bandwidthOut, bytesDown, bytesUp, packetsSent, packetsLost, packetsRetransmitted, monitorLogger)

				// Send
				if _, err := pm.sendMistTriggerContext(runtime.ctx, mistTrigger); err != nil {
					monitorLogger.WithFields(logging.Fields{
						"error":  err,
						"stream": streamName,
						"type":   "client-lifecycle",
					}).Error("Failed to send client lifecycle update to Foghorn")
				}
			}
		}
	}

	// Publish aggregated viewer counts and clean up labelsets for streams
	// or per-client tuples that have disappeared since the last poll. Mist
	// is the source of truth for which clients are currently connected;
	// anything missing this poll must be removed so the gauge reflects
	// current state instead of accumulating stale rows.
	prevStreamsMu.Lock()
	for stream, count := range currStreamViewers {
		streamViewers.WithLabelValues(stream).Set(float64(count))
	}
	for stream := range prevStreams {
		if _, present := currStreamViewers[stream]; !present {
			streamViewers.DeleteLabelValues(stream)
		}
	}
	for key := range prevStreamClients {
		if _, present := currStreamClients[key]; !present {
			streamBandwidthDown.DeleteLabelValues(key.stream, key.protocol, key.host)
			streamBandwidthUp.DeleteLabelValues(key.stream, key.protocol, key.host)
			streamConnectionTime.DeleteLabelValues(key.stream, key.protocol, key.host)
			streamPacketsTotal.DeleteLabelValues(key.stream, key.protocol, key.host)
			streamPacketsLost.DeleteLabelValues(key.stream, key.protocol, key.host)
			streamPacketsRetransmitted.DeleteLabelValues(key.stream, key.protocol, key.host)
		}
	}
	prevStreams = make(map[string]struct{}, len(currStreamViewers))
	for stream := range currStreamViewers {
		prevStreams[stream] = struct{}{}
	}
	prevStreamClients = currStreamClients
	prevStreamsMu.Unlock()

	// Reconcile viewer leases against Mist's authoritative client list. Only
	// reached after a successful GetClients call.
	if tracker := leases.GlobalTracker(); tracker != nil {
		if released := tracker.ReconcileViewers(presentSessions); len(released) > 0 {
			monitorLogger.WithField("released", released).Info("Viewer leases released by Mist reconciliation")
		}
	}
	markMistClientsPolled()

	return nil
}

// getLastJSONData safely gets the last JSON data from the Mist metrics endpoint
func (pm *PrometheusMonitor) getLastJSONData() map[string]any {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	if jsonData := pm.lastJSONData; jsonData != nil {
		return jsonData
	}
	return nil
}

// scanLocalArtifacts rebuilds the whole-node artifact index from the filesystem. complete is false
// when any directory listing failed, meaning the freshly-built index may be MISSING real artifacts.
// In that case the index is NOT replaced — the last-good index is retained — so a transient IO error
// can never truncate the node's reported inventory and make Foghorn orphan live copies.
func scanLocalArtifacts(basePath string) (totalSize uint64, artifactCount int, complete bool) {
	if basePath == "" {
		return 0, 0, true
	}

	// This scan's generation, taken at START. At commit, a stale (lower-generation) result is discarded
	// so a slow older scan never overwrites a newer scan's already-published inventory.
	gen := artifactScanGen.Add(1)
	// Point-mutation generation at scan START. This scan enumerates disk WITHOUT the lock; if a
	// delete/DTSH point mutation commits before we publish, artifactMutationGen changes and the scan's
	// whole-index replacement is stale (it could resurrect a deleted entry or erase a newer mutation),
	// so it is discarded at commit.
	baseMut := artifactMutationGen.Load()

	newArtifactIndex := make(map[string]*ClipInfo)
	complete = true

	// Snapshot the last-good index so a transient young/unstable file (which is NOT a traversal
	// failure) can be RETAINED rather than dropped. This is a DEEP copy taken under the lock: the live
	// map and its *ClipInfo values are mutated in place by the DTSH/deletion handlers (also under the
	// lock), so aliasing the map and reading it after unlock would be a data race / concurrent-map
	// panic. The copy is an immutable read-only view the scan can consult without the lock.
	var priorIndex map[string]*ClipInfo
	if prometheusMonitor != nil {
		prometheusMonitor.mutex.RLock()
		priorIndex = make(map[string]*ClipInfo, len(prometheusMonitor.artifactIndex))
		for k, v := range prometheusMonitor.artifactIndex {
			cp := *v
			priorIndex[k] = &cp
		}
		prometheusMonitor.mutex.RUnlock()
	}

	// A missing/unreadable storage ROOT (e.g. an unmounted disk) must NOT be reported as an empty
	// inventory — that would orphan every real local copy at Foghorn. Treat it as an incomplete scan so
	// the last-good index is retained. An intentionally-absent optional subdirectory (clips/dvr/vod) with
	// the root present is still handled per-subdir as legitimately empty. Fail closed: any stat error
	// (not-exist, permission, mount gone) marks the scan incomplete.
	if _, err := os.Stat(basePath); err != nil {
		monitorLogger.WithError(err).WithField("base_path", basePath).Warn("Storage root unavailable; marking artifact scan incomplete, retaining last-good index")
		complete = false
	}

	// Scan clips directory
	clipsDir := fmt.Sprintf("%s/clips", basePath)
	clipSize, clipCount, clipOK := scanClipsDirectory(clipsDir, newArtifactIndex, priorIndex)
	totalSize += clipSize
	artifactCount += clipCount
	complete = complete && clipOK

	// Scan DVR directory
	dvrDir := fmt.Sprintf("%s/dvr", basePath)
	dvrSize, dvrCount, dvrOK := scanDVRDirectory(dvrDir, newArtifactIndex)
	totalSize += dvrSize
	artifactCount += dvrCount
	complete = complete && dvrOK

	// Scan VOD directory
	vodDir := fmt.Sprintf("%s/vod", basePath)
	vodSize, vodCount, vodOK := scanVODDirectory(vodDir, newArtifactIndex, priorIndex)
	totalSize += vodSize
	artifactCount += vodCount
	complete = complete && vodOK

	if prometheusMonitor != nil {
		prometheusMonitor.mutex.Lock()
		// Generation-checked publication: discard this scan's result if a NEWER scan already published
		// (out-of-order slow scan) OR a point mutation (delete/DTSH) raced it after enumeration. Either
		// way replacing the whole index would resurrect a stale/deleted entry or clobber a fresher one.
		stale := gen <= prometheusMonitor.lastScanGen || artifactMutationGen.Load() != baseMut
		if !stale {
			prometheusMonitor.lastScanGen = gen
			prometheusMonitor.lastArtifactScan = time.Now()
			if complete {
				// Only a complete scan may replace the authoritative index and mark it trusted + healthy.
				prometheusMonitor.artifactIndex = newArtifactIndex
				prometheusMonitor.artifactIndexTrusted = true
				prometheusMonitor.artifactScanHealthy = true
			} else {
				// Retain the last-good index for observability, but mark the CURRENT inventory unhealthy so
				// the next report is flagged incomplete and Foghorn cordons this node from artifact routing.
				prometheusMonitor.artifactScanHealthy = false
			}
		}
		trusted := prometheusMonitor.artifactIndexTrusted
		healthy := prometheusMonitor.artifactScanHealthy
		retained := len(prometheusMonitor.artifactIndex)
		prometheusMonitor.mutex.Unlock()

		switch {
		case stale:
			monitorLogger.WithField("base_path", basePath).Debug("Discarded out-of-order artifact scan result; a newer scan already published")
		case complete:
			monitorLogger.WithFields(logging.Fields{
				"total_artifacts": len(newArtifactIndex),
				"total_size":      totalSize,
			}).Debug("Updated artifact index from filesystem scan")
		default:
			monitorLogger.WithFields(logging.Fields{
				"scanned_artifacts": artifactCount,
				"retained_index":    retained,
				"index_trusted":     trusted,
				"scan_healthy":      healthy,
			}).Warn("Artifact scan incomplete (mount/read error); retaining last-good index, marking inventory unhealthy so Foghorn cordons this node")
		}
	}

	return totalSize, artifactCount, complete
}

func markLocalDtshPresent(kind, hash, localPath string) {
	if prometheusMonitor == nil || hash == "" || localPath == "" || !strings.HasSuffix(localPath, ".dtsh") {
		return
	}
	if !validLocalDtsh(localPath) {
		return
	}
	var artifactType ipcpb.ArtifactEvent_ArtifactType
	switch kind {
	case "vod":
		artifactType = ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD
	case "clip":
		artifactType = ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP
	default:
		return
	}

	mediaPath := strings.TrimSuffix(localPath, ".dtsh")
	info, err := os.Stat(mediaPath)
	if err != nil || info == nil || info.IsDir() {
		return
	}
	ext := filepath.Ext(mediaPath)
	if ext == "" {
		return
	}

	prometheusMonitor.mutex.Lock()
	defer prometheusMonitor.mutex.Unlock()

	existing := prometheusMonitor.artifactIndex[hash]
	if existing == nil {
		existing = &ClipInfo{
			CreatedAt:    info.ModTime(),
			LastAccessed: info.ModTime(),
		}
	}
	existing.FilePath = mediaPath
	existing.Format = strings.TrimPrefix(ext, ".")
	existing.SizeBytes = uint64(info.Size())
	existing.HasDtsh = true
	artifactMutationGen.Add(1) // point mutation — invalidate any in-flight scan's publish
	existing.ArtifactType = artifactType
	if existing.CreatedAt.IsZero() {
		existing.CreatedAt = info.ModTime()
	}
	if existing.LastAccessed.IsZero() {
		existing.LastAccessed = info.ModTime()
	}
	if kind == "clip" {
		if streamName := streamNameFromClipPath(localPath); streamName != "" {
			existing.StreamName = streamName
		}
	}
	prometheusMonitor.artifactIndex[hash] = existing
	prometheusMonitor.lastArtifactScan = time.Now()
}

func streamNameFromClipPath(path string) string {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "clips" {
			return parts[i+1]
		}
	}
	return ""
}

// scanVODDirectory scans the VOD directory for user-uploaded assets. The bool return is false when a
// directory listing OR a per-file metadata read failed (other than the file legitimately vanishing),
// so the caller can retain the last-good index instead of truncating and orphaning live copies.
func scanVODDirectory(vodDir string, artifactIndex, priorIndex map[string]*ClipInfo) (uint64, int, bool) {
	if _, err := os.Stat(vodDir); os.IsNotExist(err) {
		return 0, 0, true
	}

	var totalSize uint64
	artifactCount := 0
	complete := true

	entries, err := os.ReadDir(vodDir)
	if err != nil {
		monitorLogger.WithError(err).Error("Failed to read VOD directory")
		return 0, 0, false
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		ext := filepath.Ext(name)
		if ext == "" || ext == ".dtsh" {
			continue
		}
		hash := strings.TrimSuffix(name, ext)
		if len(hash) < 18 || !isHex(hash) {
			continue
		}
		if _, exists := artifactIndex[hash]; exists {
			monitorLogger.WithFields(logging.Fields{
				"artifact_hash": hash,
				"file_name":     name,
			}).Warn("Ignoring duplicate VOD artifact file for hash")
			continue
		}

		info, err := entry.Info()
		if err != nil {
			// A file that vanished between ReadDir and Info legitimately left the inventory; any other
			// metadata error means we may be OMITTING a live artifact, so mark the scan incomplete.
			if !os.IsNotExist(err) {
				monitorLogger.WithError(err).WithField("file_name", name).Warn("VOD file metadata read failed; marking scan incomplete")
				complete = false
			}
			continue
		}
		if time.Since(info.ModTime()) < fileStabilityThreshold {
			// A just-finalized VOD is still settling. A transient young file is NOT a traversal failure, so
			// do NOT mark the whole scan incomplete — that would cordon EVERY artifact on the node for a
			// routine finalization. Retain the last-good entry if we already reported this artifact (so a
			// rewritten live copy is never dropped); a brand-new file is simply omitted until it stabilises.
			if prior, ok := priorIndex[hash]; ok {
				artifactIndex[hash] = prior
				artifactCount++
				totalSize += prior.SizeBytes
			}
			monitorLogger.WithField("file_name", name).Debug("VOD file too new to be stable; retained last-good entry (scan stays complete)")
			continue
		}

		filePath := fmt.Sprintf("%s/%s", vodDir, name)
		hasDtsh := validLocalDtsh(filePath + ".dtsh")
		vodInfo := &ClipInfo{
			FilePath:     filePath,
			StreamName:   "", // VOD assets are not tied to a live stream name
			Format:       strings.TrimPrefix(ext, "."),
			SizeBytes:    uint64(info.Size()),
			CreatedAt:    info.ModTime(),
			SegmentCount: 0,
			HasDtsh:      hasDtsh,
			ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD,
		}
		artifactIndex[hash] = vodInfo
		totalSize += uint64(info.Size())
		artifactCount++
	}

	return totalSize, artifactCount, complete
}

func isHex(value string) bool {
	if value == "" {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// scanClipsDirectory scans the clips directory for clip artifacts. The bool return is false when a
// directory listing failed (root or a per-stream subdir), signalling a possibly-truncated result.
func scanClipsDirectory(clipsDir string, artifactIndex, priorIndex map[string]*ClipInfo) (uint64, int, bool) {
	// Check if clips directory exists
	if _, err := os.Stat(clipsDir); os.IsNotExist(err) {
		return 0, 0, true
	}

	vodDir := filepath.Clean(filepath.Join(filepath.Dir(clipsDir), "vod"))

	var totalSize uint64
	artifactCount := 0
	complete := true

	// Walk the clips directory structure
	entries, err := os.ReadDir(clipsDir)
	if err != nil {
		monitorLogger.WithError(err).Error("Failed to read clips directory")
		return 0, 0, false
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			// Check if this is a direct VOD link (clips/abc123def456.mp4)
			ext := filepath.Ext(entry.Name())
			if IsVideoFile(ext) {
				clipHash := strings.TrimSuffix(entry.Name(), ext)
				if len(clipHash) >= 18 { // Artifact hash: timestamp(14) + hex(4+)
					format := strings.TrimPrefix(ext, ".")
					filePath := fmt.Sprintf("%s/%s", clipsDir, entry.Name())

					// Get file info
					fileInfo, err := os.Stat(filePath)
					if err != nil {
						// A file that vanished mid-scan legitimately left; any other metadata error means we may
						// be OMITTING a live clip, so mark the scan incomplete rather than truncate.
						if !os.IsNotExist(err) {
							monitorLogger.WithError(err).WithField("file_path", filePath).Warn("Clip file metadata read failed; marking scan incomplete")
							complete = false
						}
						continue
					}
					{
						if time.Since(fileInfo.ModTime()) < fileStabilityThreshold {
							// A just-finalized clip is still settling. A transient young file is NOT a traversal
							// failure, so do NOT cordon the whole node — retain the last-good entry if already
							// reported (never drop a rewritten live copy); a brand-new file is omitted until stable.
							if prior, ok := priorIndex[clipHash]; ok {
								artifactIndex[clipHash] = prior
								artifactCount++
								totalSize += prior.SizeBytes
							}
							monitorLogger.WithField("file_path", filePath).Debug("Clip file too new to be stable; retained last-good entry (scan stays complete)")
							continue
						}
						// Try to determine stream name from symlink target
						streamName := "unknown"
						if target, err := os.Readlink(filePath); err == nil {
							absTarget := target
							if !filepath.IsAbs(target) {
								absTarget = filepath.Join(filepath.Dir(filePath), target)
							}
							if resolved, err := filepath.EvalSymlinks(absTarget); err == nil {
								resolved = filepath.Clean(resolved)
								// Skip direct VOD symlinks (clips/* -> <base>/vod/*). Avoid false positives like clips/vod/... when stream name is "vod".
								if strings.HasPrefix(resolved, vodDir+string(filepath.Separator)) || resolved == vodDir {
									continue
								}
							}
							parts := strings.Split(target, "/")
							for i, part := range parts {
								if part == "clips" && i+1 < len(parts) {
									streamName = parts[i+1]
									break
								}
							}
						}

						hasDtsh := validLocalDtsh(filePath + ".dtsh")

						clipInfo := &ClipInfo{
							FilePath:     filePath,
							StreamName:   streamName,
							Format:       format,
							SizeBytes:    uint64(fileInfo.Size()),
							CreatedAt:    fileInfo.ModTime(),
							HasDtsh:      hasDtsh,
							AccessCount:  0,
							LastAccessed: fileInfo.ModTime(),
							ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
						}

						artifactIndex[clipHash] = clipInfo
						totalSize += uint64(fileInfo.Size())
						artifactCount++
					}
				}
			}
			continue
		}

		// This is a stream directory - scan for organized clips
		streamName := entry.Name()
		streamDir := fmt.Sprintf("%s/%s", clipsDir, streamName)

		streamEntries, err := os.ReadDir(streamDir)
		if err != nil {
			// A per-stream listing failure would silently drop that stream's clips from the report.
			monitorLogger.WithError(err).WithField("stream_dir", streamDir).Warn("Failed to read clip stream directory; marking scan incomplete")
			complete = false
			continue
		}

		for _, clipFile := range streamEntries {
			if clipFile.IsDir() {
				continue
			}

			// Check if this looks like a clip file
			ext := filepath.Ext(clipFile.Name())
			if !IsVideoFile(ext) {
				continue
			}
			clipHash := strings.TrimSuffix(clipFile.Name(), ext)
			if len(clipHash) < 18 { // Artifact hash: timestamp(14) + hex(4+)
				continue
			}
			format := strings.TrimPrefix(ext, ".")
			filePath := fmt.Sprintf("%s/%s", streamDir, clipFile.Name())

			// Get file info
			fileInfo, err := os.Stat(filePath)
			if err != nil {
				// A file that vanished mid-scan legitimately left; any other metadata error means we may
				// be OMITTING a live clip, so mark the scan incomplete rather than truncate.
				if !os.IsNotExist(err) {
					monitorLogger.WithError(err).WithField("file_path", filePath).Warn("Clip file metadata read failed; marking scan incomplete")
					complete = false
				}
				continue
			}
			{
				if time.Since(fileInfo.ModTime()) < fileStabilityThreshold {
					// A just-finalized clip is still settling. A transient young file is NOT a traversal
					// failure, so do NOT cordon the whole node — retain the last-good entry if already
					// reported (never drop a rewritten live copy); a brand-new file is omitted until stable.
					if prior, ok := priorIndex[clipHash]; ok {
						artifactIndex[clipHash] = prior
						artifactCount++
						totalSize += prior.SizeBytes
					}
					monitorLogger.WithField("file_path", filePath).Debug("Clip file too new to be stable; retained last-good entry (scan stays complete)")
					continue
				}
				hasDtsh := validLocalDtsh(filePath + ".dtsh")

				clipInfo := &ClipInfo{
					FilePath:     filePath,
					StreamName:   streamName,
					Format:       format,
					SizeBytes:    uint64(fileInfo.Size()),
					CreatedAt:    fileInfo.ModTime(),
					HasDtsh:      hasDtsh,
					AccessCount:  0,
					LastAccessed: fileInfo.ModTime(),
					ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
				}

				artifactIndex[clipHash] = clipInfo
				totalSize += uint64(fileInfo.Size())
				artifactCount++
			}
		}
	}

	return totalSize, artifactCount, complete
}

// calculateDVRSegmentSize parses an HLS manifest and sums up segment file sizes
func calculateDVRSegmentSize(manifestPath, baseDir string) (uint64, int) {
	var totalSize uint64
	var segmentCount int

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return 0, 0
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		// Skip empty lines and tags
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// This is a segment reference (relative path like "segments/1234_0.ts")
		segPath := filepath.Join(baseDir, line)
		if info, err := os.Stat(segPath); err == nil && !info.IsDir() {
			if time.Since(info.ModTime()) >= fileStabilityThreshold {
				totalSize += uint64(info.Size())
				segmentCount++
			}
		}
	}

	return totalSize, segmentCount
}

// scanDVRDirectory scans the DVR directory for DVR manifest files. The bool return is false when a
// directory listing failed (root or a per-stream subdir), signalling a possibly-truncated result.
func scanDVRDirectory(dvrDir string, artifactIndex map[string]*ClipInfo) (uint64, int, bool) {
	// Check if DVR directory exists
	if _, err := os.Stat(dvrDir); os.IsNotExist(err) {
		return 0, 0, true
	}

	var totalSize uint64
	artifactCount := 0
	complete := true

	// Walk the DVR directory structure: /dvr/{stream_id}/{dvr_hash}/{dvr_hash}.m3u8
	entries, err := os.ReadDir(dvrDir)
	if err != nil {
		monitorLogger.WithError(err).Error("Failed to read DVR directory")
		return 0, 0, false
	}

	activeDVRs := control.GetActiveDVRHashes()

	for _, entry := range entries {
		if !entry.IsDir() {
			continue // Skip files in the DVR root directory
		}

		// This is a stream directory (stream_id) - scan for DVR hash subdirectories
		streamID := entry.Name()
		streamDVRDir := filepath.Join(dvrDir, streamID)

		streamEntries, err := os.ReadDir(streamDVRDir)
		if err != nil {
			// A per-stream listing failure would silently drop that stream's DVRs from the report.
			monitorLogger.WithError(err).WithField("stream_dvr_dir", streamDVRDir).Warn("Failed to read DVR stream directory; marking scan incomplete")
			complete = false
			continue
		}

		for _, dvrHashDir := range streamEntries {
			if !dvrHashDir.IsDir() {
				continue // Skip non-directories
			}

			dvrHash := dvrHashDir.Name()
			if len(dvrHash) < 18 {
				continue // Not a valid DVR hash
			}
			if activeDVRs[dvrHash] {
				continue
			}

			// DVR directory: /dvr/{stream_id}/{dvr_hash}/
			dvrPath := filepath.Join(streamDVRDir, dvrHash)
			manifestPath := filepath.Join(dvrPath, dvrHash+".m3u8")

			// Check if manifest exists
			fileInfo, err := os.Stat(manifestPath)
			if err != nil {
				// No manifest (not-exist) legitimately skips; any other error means we may be OMITTING a
				// live DVR whose manifest we simply couldn't read, so mark the scan incomplete.
				if !os.IsNotExist(err) {
					monitorLogger.WithError(err).WithField("manifest_path", manifestPath).Warn("DVR manifest metadata read failed; marking scan incomplete")
					complete = false
				}
				continue // No manifest in this directory
			}

			// Calculate total size including segments referenced by manifest
			manifestSize := uint64(fileInfo.Size())
			segmentSize, segmentCount := calculateDVRSegmentSize(manifestPath, dvrPath)
			dvrTotalSize := manifestSize + segmentSize

			// Check if any valid .dtsh index files exist in the DVR directory
			hasDtsh := false
			if dirEntries, err := os.ReadDir(dvrPath); err == nil {
				for _, de := range dirEntries {
					if !de.IsDir() && strings.HasSuffix(de.Name(), ".dtsh") && validLocalDtsh(filepath.Join(dvrPath, de.Name())) {
						hasDtsh = true
						break
					}
				}
			}

			// Add DVR manifest to artifact index using same ClipInfo structure
			dvrInfo := &ClipInfo{
				FilePath:     dvrPath,
				StreamName:   streamID,
				Format:       "m3u8",
				SizeBytes:    dvrTotalSize,
				CreatedAt:    fileInfo.ModTime(),
				SegmentCount: segmentCount,
				HasDtsh:      hasDtsh,
				AccessCount:  0,
				LastAccessed: fileInfo.ModTime(),
				ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR,
			}

			artifactIndex[dvrHash] = dvrInfo
			totalSize += dvrTotalSize
			artifactCount++
		}
	}

	return totalSize, artifactCount, complete
}

// GetStoredArtifacts returns artifacts from the global prometheusMonitor's artifactIndex
func GetStoredArtifacts() []*ipcpb.StoredArtifact {
	if prometheusMonitor == nil {
		return nil
	}
	prometheusMonitor.mutex.RLock()
	defer prometheusMonitor.mutex.RUnlock()
	return storedArtifactsLocked()
}

// storedArtifactsLocked builds the whole-node artifact slice from artifactIndex. The caller MUST hold
// prometheusMonitor.mutex. A caller that also reads the readiness flags (artifactIndexTrusted /
// artifactScanHealthy) must do so under the SAME lock acquisition, so the reported inventory and its
// flags are one consistent snapshot — an incomplete scan can never commit between the two reads.
func storedArtifactsLocked() []*ipcpb.StoredArtifact {
	var artifacts []*ipcpb.StoredArtifact
	for clipHash, clipInfo := range prometheusMonitor.artifactIndex {
		artifact := &ipcpb.StoredArtifact{
			ClipHash:   clipHash,
			StreamName: clipInfo.StreamName,
			FilePath:   clipInfo.FilePath,
			SizeBytes:  clipInfo.SizeBytes,
			CreatedAt:  clipInfo.CreatedAt.Unix(),
			Format:     clipInfo.Format,
			HasDtsh:    clipInfo.HasDtsh,
			ArtifactType: func() ipcpb.ArtifactEvent_ArtifactType {
				if clipInfo.ArtifactType != ipcpb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED {
					return clipInfo.ArtifactType
				}
				return ipcpb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED
			}(),
			AccessCount: func() uint64 {
				if clipInfo.AccessCount < 0 {
					return 0
				}
				return uint64(clipInfo.AccessCount)
			}(),
			LastAccessed: func() int64 {
				if clipInfo.LastAccessed.IsZero() {
					return 0
				}
				return clipInfo.LastAccessed.Unix()
			}(),
		}

		// Add S3 URL if available
		if clipInfo.S3URL != "" {
			artifact.S3Url = clipInfo.S3URL
		}

		artifacts = append(artifacts, artifact)
	}

	return artifacts
}

// convertNodeAPIToMistTrigger converts MistServer JSON API response to MistTrigger
func (pm *PrometheusMonitor) convertNodeAPIToMistTrigger(nodeID string, jsonData map[string]any, logger logging.Logger) *ipcpb.MistTrigger {
	// Use public edge URL for client-facing BaseUrl (for playback URLs)
	// Fall back to internal URL if public not configured
	baseURL := pm.edgePublicURL
	if baseURL == "" {
		baseURL = pm.baseURL
	}

	nodeUpdate := &ipcpb.NodeLifecycleUpdate{
		NodeId:            nodeID,
		BaseUrl:           baseURL, // Client-facing URL for playback
		EventType:         "node_lifecycle_update",
		Timestamp:         time.Now().Unix(),
		ComponentVersions: currentComponentVersions(),
		DeployMode:        firstNonEmptyString(os.Getenv("DEPLOY_MODE"), "native"),
		Os:                runtime.GOOS,
		Arch:              runtime.GOARCH,
		// artifacts_report_revision is assigned atomically WITH the artifact snapshot in
		// enrichNodeLifecycleTrigger (captureArtifactSnapshot), not here — pairing them here would
		// let a concurrent report attach a newer snapshot to this older revision.
	}

	if jsonData != nil {
		// Extract CPU usage (Mist provides integer percentage 0-100 or more)
		if cpu, ok := jsonData["cpu"].(float64); ok {
			nodeUpdate.CpuTenths = uint32(normalizeMistCPUPercent(cpu) * 10) // Convert % to tenths (e.g. 14% -> 140)
		}

		// Extract RAM info (Mist provides bytes)
		if memTotal, ok := jsonData["mem_total"].(float64); ok {
			nodeUpdate.RamMax = uint64(memTotal) // Bytes
		} else if ram, ok := jsonData["ram"].(map[string]any); ok {
			// Fallback to old 'ram' object if 'mem_total' missing
			if max, ok := ram["max"].(float64); ok {
				nodeUpdate.RamMax = uint64(max)
			}
		}

		if memUsed, ok := jsonData["mem_used"].(float64); ok {
			nodeUpdate.RamCurrent = uint64(memUsed) // Bytes
		} else if ram, ok := jsonData["ram"].(map[string]any); ok {
			// Fallback
			if current, ok := ram["current"].(float64); ok {
				nodeUpdate.RamCurrent = uint64(current)
			}
		}

		// Extract Shared Memory info
		if shmTotal, ok := jsonData["shm_total"].(float64); ok {
			nodeUpdate.ShmTotalBytes = uint64(shmTotal)
		}
		if shmUsed, ok := jsonData["shm_used"].(float64); ok {
			nodeUpdate.ShmUsedBytes = uint64(shmUsed)
		}

		// Extract bandwidth data from bw array: [up_total, down_total]
		if bw, ok := jsonData["bw"].([]any); ok && len(bw) >= 2 {
			var currentUp, currentDown uint64
			if up, ok := bw[0].(float64); ok {
				currentUp = uint64(up)
			}
			if down, ok := bw[1].(float64); ok {
				currentDown = uint64(down)
			}

			// Store cumulative totals
			nodeUpdate.BandwidthOutTotal = currentUp
			nodeUpdate.BandwidthInTotal = currentDown

			// Compute rates (bytes/sec) from delta
			elapsed := time.Since(pm.lastPollTime).Seconds()
			if pm.lastBwUp > 0 && elapsed > 1.0 && currentUp >= pm.lastBwUp {
				// Normal case: compute rate from delta
				nodeUpdate.UpSpeed = uint64(float64(currentUp-pm.lastBwUp) / elapsed)
				nodeUpdate.DownSpeed = uint64(float64(currentDown-pm.lastBwDown) / elapsed)
			}
			// Else: first poll or counter reset - leave rates at 0

			// Store for next poll
			pm.lastBwUp = currentUp
			pm.lastBwDown = currentDown
			pm.lastPollTime = time.Now()
		} else if bandwidth, ok := jsonData["bandwidth"].(map[string]any); ok {
			// Older payloads report aggregate bandwidth under a nested object.
			if up, ok := bandwidth["up"].(float64); ok {
				nodeUpdate.UpSpeed = uint64(up)
			}
			if down, ok := bandwidth["down"].(float64); ok {
				nodeUpdate.DownSpeed = uint64(down)
			}
		}

		// Extract current connections from curr array
		// curr = [viewers, inputs, outgoing, unspecified, cached]
		if curr, ok := jsonData["curr"].([]any); ok {
			if len(curr) > 0 {
				if viewers, ok := curr[0].(float64); ok {
					nodeUpdate.ConnectionsCurrent = uint32(viewers)
				}
			}
			if len(curr) > 1 {
				if inputs, ok := curr[1].(float64); ok {
					nodeUpdate.ConnectionsInputs = uint32(inputs)
				}
			}
			if len(curr) > 2 {
				if outgoing, ok := curr[2].(float64); ok {
					nodeUpdate.ConnectionsOutgoing = uint32(outgoing)
				}
			}
			if len(curr) > 4 {
				if cached, ok := curr[4].(float64); ok {
					nodeUpdate.ConnectionsCached = uint32(cached)
				}
			}
		}

		// Extract MistServer trigger health statistics (for monitoring/debugging)
		if triggers, ok := jsonData["triggers"].(map[string]any); ok {
			if triggersJSON, err := json.Marshal(triggers); err == nil {
				nodeUpdate.TriggersJson = string(triggersJSON)
			}
		}

		if limit := sidecarcfg.ConfiguredBandwidthLimitBytesPerSec(); limit > 0 {
			nodeUpdate.BwLimit = limit
		} else if limit, ok := jsonData["bwlimit"].(float64); ok && limit > 0 {
			nodeUpdate.BwLimit = uint64(limit)
		} else {
			// Default to 1Gbps when MistServer doesn't report bwlimit (same as C++ default)
			nodeUpdate.BwLimit = 128 * 1024 * 1024 // 128 MB/s = ~1 Gbps
		}

		// Extract location data
		if locData, ok := jsonData["loc"].(map[string]any); ok {
			if lat, ok := locData["lat"].(float64); ok {
				nodeUpdate.Latitude = lat
			}
			if lon, ok := locData["lon"].(float64); ok {
				nodeUpdate.Longitude = lon
			}
			if name, ok := locData["name"].(string); ok && name != "" {
				nodeUpdate.Location = name
			}
		}

		// Extract outputs configuration
		if outputs, ok := jsonData["outputs"]; ok {
			if outputsJSON, err := json.Marshal(outputs); err == nil {
				nodeUpdate.OutputsJson = string(outputsJSON)
			}
		}
	}
	if nodeUpdate.BwLimit == 0 {
		nodeUpdate.BwLimit = sidecarcfg.ConfiguredBandwidthLimitBytesPerSec()
	}

	// Get Disk Usage from OS.
	storagePath := os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH")
	if storagePath == "" {
		storagePath = sidecarcfg.GetStoragePath()
	}

	info, err := os.Stat(storagePath)
	if err != nil {
		if os.IsNotExist(err) {
			logger.WithField("path", storagePath).Warn("Disk metrics path does not exist; set HELMSMAN_STORAGE_LOCAL_PATH to a valid mount point")
		} else {
			logger.WithError(err).WithField("path", storagePath).Warn("Failed to stat disk metrics path")
		}
	} else if !info.IsDir() {
		logger.WithField("path", storagePath).Warn("Disk metrics path is not a directory")
	} else if total, used, err := getDiskUsage(storagePath); err == nil {
		nodeUpdate.DiskTotalBytes = total
		nodeUpdate.DiskUsedBytes = used
	} else {
		logger.WithError(err).WithField("path", storagePath).Warn("Failed to get disk usage")
	}

	// Determine node health based on resource utilization thresholds.
	cpuPercent := float64(nodeUpdate.CpuTenths) / 10.0
	memPercent := float64(0)
	if nodeUpdate.RamMax > 0 {
		memPercent = float64(nodeUpdate.RamCurrent) / float64(nodeUpdate.RamMax) * 100
	}
	shmPercent := float64(0)
	if nodeUpdate.ShmTotalBytes > 0 {
		shmPercent = float64(nodeUpdate.ShmUsedBytes) / float64(nodeUpdate.ShmTotalBytes) * 100
	}

	hasMistData := jsonData != nil
	isHealthy := evaluateNodeHealth(hasMistData, cpuPercent, memPercent, shmPercent)
	nodeUpdate.IsHealthy = isHealthy

	logger.WithFields(logging.Fields{
		"node_id":       nodeID,
		"has_mist_data": hasMistData,
		"cpu_percent":   cpuPercent,
		"mem_percent":   memPercent,
		"shm_percent":   shmPercent,
		"is_healthy":    isHealthy,
	}).Info("Node health determination")

	// Populate full Streams map from MistServer data
	// This is CRITICAL for load balancing - balancer checks stream.Inputs > 0
	if jsonData != nil {
		if streams, ok := jsonData["streams"].(map[string]any); ok {
			nodeUpdate.Streams = make(map[string]*ipcpb.StreamData)
			for streamName, streamData := range streams {
				if streamInfo, ok := streamData.(map[string]any); ok {
					sd := &ipcpb.StreamData{}

					// Extract from curr array: [viewers, inputs, outgoing, unspecified, cached]
					if curr, ok := streamInfo["curr"].([]any); ok {
						if len(curr) > 0 {
							if viewers, ok := curr[0].(float64); ok {
								sd.Total = uint64(viewers)
							}
						}
						if len(curr) > 1 {
							if inputs, ok := curr[1].(float64); ok {
								sd.Inputs = uint32(inputs)
							}
						}
					}

					// Extract from bw array: [bandwidth_in, bandwidth_out]
					if bw, ok := streamInfo["bw"].([]any); ok && len(bw) >= 2 {
						if bandwidthIn, ok := bw[0].(float64); ok {
							sd.BytesUp = uint64(bandwidthIn)
						}
						if bandwidthOut, ok := bw[1].(float64); ok {
							sd.BytesDown = uint64(bandwidthOut)
						}
						// Calculate bandwidth per viewer (bytes/sec per viewer)
						if sd.Total > 0 && sd.BytesDown > 0 {
							sd.Bandwidth = uint32(sd.BytesDown / sd.Total)
						}
					}

					sd.Replicated = mistStreamReplicated(streamInfo)

					// Extract packet counts from pkts array
					if pkts, ok := streamInfo["pkts"].([]any); ok {
						sd.PacketCounts = make([]int64, len(pkts))
						for i, pkt := range pkts {
							if v, ok := pkt.(float64); ok {
								sd.PacketCounts[i] = int64(v)
							}
						}
					}

					// Extract total connections from tot array
					if tot, ok := streamInfo["tot"].([]any); ok {
						sd.TotalConnections = make([]int64, len(tot))
						for i, t := range tot {
							if v, ok := t.(float64); ok {
								sd.TotalConnections[i] = int64(v)
							}
						}
					}

					// Use normalized internal name as key (e.g., "live+demo_stream" -> "demo_stream")
					internalName := mist.ExtractInternalName(streamName)
					nodeUpdate.Streams[internalName] = sd
				}
			}
			nodeUpdate.ActiveStreams = uint32(len(nodeUpdate.Streams))
			logger.WithFields(logging.Fields{
				"node_id":      nodeID,
				"stream_count": len(nodeUpdate.Streams),
			}).Debug("Populated streams map for NodeLifecycleUpdate")
		}
	}

	return &ipcpb.MistTrigger{
		TriggerType: "NODE_LIFECYCLE_UPDATE",
		NodeId:      nodeID,
		Timestamp:   time.Now().Unix(),
		Blocking:    false,
		RequestId:   "", // Non-blocking
		TriggerPayload: &ipcpb.MistTrigger_NodeLifecycleUpdate{
			NodeLifecycleUpdate: nodeUpdate,
		},
	}
}

func validLocalDtsh(path string) bool {
	if err := dtsh.ValidateFile(path); err != nil {
		if os.IsNotExist(err) {
			return false
		}
		if monitorLogger != nil {
			monitorLogger.WithError(err).WithField("local_path", path).Warn("Removing invalid local .dtsh")
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) && monitorLogger != nil {
			monitorLogger.WithError(removeErr).WithField("local_path", path).Warn("Failed to remove invalid local .dtsh")
		}
		return false
	}
	return true
}

func normalizeMistCPUPercent(rawCPU float64) float64 {
	if rawCPU <= 0 {
		return 0
	}
	if rawCPU > 100 {
		cores := runtime.NumCPU()
		if cores > 1 {
			rawCPU = rawCPU / float64(cores)
		}
	}
	if rawCPU > 100 {
		return 100
	}
	return rawCPU
}

func evaluateNodeHealth(hasMistData bool, cpuPercent, memPercent, shmPercent float64) bool {
	if !hasMistData {
		return false
	}
	return memPercent <= 90 && shmPercent <= 90
}

// getDiskUsage returns total and used bytes for the file system containing path
func getDiskUsage(path string) (total, used uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}

	// Available blocks * size per block = available space in bytes
	// Total blocks * size per block = total space in bytes
	total = stat.Blocks * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	used = total - free
	return total, used, nil
}

// enrichNodeLifecycleTrigger enriches node lifecycle trigger with Helmsman-specific data
func enrichNodeLifecycleTrigger(mistTrigger *ipcpb.MistTrigger, capIngest, capEdge, capStorage, capProcessing string, roles []string) {
	if nodeUpdate := mistTrigger.GetNodeLifecycleUpdate(); nodeUpdate != nil {
		// Report the authoritative mode from ConfigSeed (set by Foghorn), not the env var
		nodeUpdate.OperationalMode = sidecarcfg.GetOperationalMode()

		// Add capabilities
		nodeUpdate.Capabilities = &ipcpb.NodeCapabilities{
			Ingest:     capIngest == "" || capIngest == "1" || strings.ToLower(capIngest) == "true",
			Edge:       capEdge == "" || capEdge == "1" || strings.ToLower(capEdge) == "true",
			Storage:    capStorage == "" || capStorage == "1" || strings.ToLower(capStorage) == "true",
			Processing: capProcessing == "" || capProcessing == "1" || strings.ToLower(capProcessing) == "true",
			Roles:      roles,
		}

		// Add storage info
		nodeUpdate.Storage = &ipcpb.StorageInfo{
			LocalPath: os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH"),
			S3Bucket:  os.Getenv("HELMSMAN_STORAGE_S3_BUCKET"),
			S3Prefix:  os.Getenv("HELMSMAN_STORAGE_S3_PREFIX"),
		}

		// Add limits from environment + live processing capacity.
		limits := &ipcpb.NodeLimits{}
		hasLimits := false
		// A processing-capable node always advertises its video_transcode class so
		// Foghorn can route VOD/clip/DVR jobs to it and balance by live load.
		// slots_total is the configured ceiling (0 = unbounded, from
		// HELMSMAN_MAX_TRANSCODES); slots_used is the live in-flight job count.
		capProc := capProcessing == "" || capProcessing == "1" || strings.ToLower(capProcessing) == "true"
		if capProc {
			total := 0
			if maxT, err := strconv.Atoi(os.Getenv("HELMSMAN_MAX_TRANSCODES")); err == nil && maxT > 0 {
				total = maxT
			}
			limits.ProcessingClasses = []*ipcpb.ProcessingClassCapacity{{
				Class:      mist.ProcessingClassVideoTranscode,
				SlotsTotal: safeInt32(total),
				SlotsUsed:  safeInt32(CountPendingJobs()),
			}}
			hasLimits = true
		}
		if capBytes, err := strconv.ParseUint(os.Getenv("HELMSMAN_STORAGE_CAPACITY_BYTES"), 10, 64); err == nil && capBytes > 0 {
			limits.StorageCapacityBytes = capBytes
			hasLimits = true
		}
		if hasLimits {
			nodeUpdate.Limits = limits
		}

		// Add artifacts from artifactIndex, paired atomically with this report's sequence. incomplete
		// tells Foghorn not to treat a pre-first-complete-scan snapshot as authoritative inventory.
		nodeUpdate.Artifacts, nodeUpdate.ArtifactsReportSeq, nodeUpdate.ArtifactsReportedAtMs,
			nodeUpdate.ArtifactsSnapshotIncomplete = captureArtifactSnapshot()

		// Attach tenant_id from last ConfigSeed (provided by Foghorn)
		if t := sidecarcfg.GetTenantID(); t != "" {
			nodeUpdate.TenantId = &t
		}
	}
}

func rolesFromCapabilityFlags(capIngest, capEdge, capStorage, capProcessing string) []string {
	var roles []string
	if interpretCapabilityFlag(capIngest, true) {
		roles = append(roles, "ingest")
	}
	if interpretCapabilityFlag(capEdge, true) {
		roles = append(roles, "edge")
	}
	if interpretCapabilityFlag(capStorage, true) {
		roles = append(roles, "storage")
	}
	if interpretCapabilityFlag(capProcessing, true) {
		roles = append(roles, "processing")
	}
	return roles
}

func interpretCapabilityFlag(value string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes"
}

// convertStreamAPIToMistTrigger converts stream API response data to a MistTrigger protobuf
func convertStreamAPIToMistTrigger(nodeID, _streamName, internalName string, streamData, healthData map[string]any, trackDetails []map[string]any, trackCount int, _logger logging.Logger) *ipcpb.MistTrigger {
	status := "waiting"
	if streamAPIHasLiveMedia(streamData, trackDetails, trackCount) {
		status = "live"
	}
	streamLifecycleUpdate := &ipcpb.StreamLifecycleUpdate{
		NodeId:       nodeID,
		InternalName: internalName,
		Status:       status,
	}

	// Extract basic metrics from stream data
	// Note: 0 is a valid value for all these metrics (e.g., stream just started, no viewers yet)
	if viewers, ok := streamData["viewers"].(float64); ok {
		totalViewers := uint32(viewers)
		streamLifecycleUpdate.TotalViewers = &totalViewers
	}
	if inputs, ok := streamData["inputs"].(float64); ok {
		totalInputs := uint32(inputs)
		streamLifecycleUpdate.TotalInputs = &totalInputs
	}
	if upbytes, ok := streamData["upbytes"].(float64); ok {
		uploadedBytes := uint64(upbytes)
		streamLifecycleUpdate.UploadedBytes = &uploadedBytes
	}
	if downbytes, ok := streamData["downbytes"].(float64); ok {
		downloadedBytes := uint64(downbytes)
		streamLifecycleUpdate.DownloadedBytes = &downloadedBytes
	}
	if replicated, ok := mistStreamReplicatedValue(streamData); ok {
		streamLifecycleUpdate.Replicated = &replicated
	}

	// Add health data as stream details
	if len(healthData) > 0 {
		if healthDataBytes, err := json.Marshal(healthData); err == nil {
			streamDetails := string(healthDataBytes)
			streamLifecycleUpdate.StreamDetails = &streamDetails
		}
	}

	// Extract packet statistics from streamData (MistServer active_streams API fields)
	// Note: These are stream-level totals, NOT in the health blob
	// 0 is valid (e.g., HLS streams don't track packets at stream level)
	if packsent, ok := streamData["packsent"].(float64); ok {
		ps := uint64(packsent)
		streamLifecycleUpdate.PacketsSent = &ps
	}
	if packloss, ok := streamData["packloss"].(float64); ok {
		pl := uint64(packloss)
		streamLifecycleUpdate.PacketsLost = &pl
	}
	if packretrans, ok := streamData["packretrans"].(float64); ok {
		pr := uint64(packretrans)
		streamLifecycleUpdate.PacketsRetransmitted = &pr
	}

	// Extract viewseconds if available (cumulative viewer time)
	// 0 is valid (stream just started)
	if viewseconds, ok := streamData["viewseconds"].(float64); ok {
		vs := uint64(viewseconds)
		streamLifecycleUpdate.ViewerSeconds = &vs
	}

	// Extract top-level health blob metrics (stream-wide summary)
	// 0 is valid (perfect conditions with no buffer latency or jitter)
	if buffer, ok := healthData["buffer"].(float64); ok {
		buf := uint32(buffer)
		streamLifecycleUpdate.BufferMs = &buf
	}
	if jitter, ok := healthData["jitter"].(float64); ok {
		jit := uint32(jitter)
		streamLifecycleUpdate.JitterMs = &jit
	}
	if maxkeepaway, ok := healthData["maxkeepaway"].(float64); ok {
		mka := uint32(maxkeepaway)
		streamLifecycleUpdate.MaxKeepawayMs = &mka
	}

	// Extract quality metrics from track details
	var qualityTier string
	var primaryWidth, primaryHeight int32
	var primaryFPS float64
	var primaryBitrate int32
	var primaryCodec string
	var primaryVideoBufferMs, primaryVideoJitterMs uint32
	var foundVideo, foundAudio bool

	if len(trackDetails) > 0 {
		// Serialize full track details to JSON for storage
		if trackJSON, err := json.Marshal(trackDetails); err == nil {
			trackDetailsStr := string(trackJSON)
			streamLifecycleUpdate.TrackDetailsJson = &trackDetailsStr
		}

		for _, track := range trackDetails {
			trackType, _ := track["type"].(string)

			// Extract primary video track info
			if trackType == "video" && !foundVideo {
				foundVideo = true
				if width, ok := track["width"].(int); ok {
					primaryWidth = int32(width)
				}
				if height, ok := track["height"].(int); ok {
					primaryHeight = int32(height)
				}
				if fps, ok := track["fps"].(float64); ok {
					primaryFPS = fps
				}
				if bitrate, ok := track["bitrate_kbps"].(int); ok {
					primaryBitrate = int32(bitrate)
				}
				if codec, ok := track["codec"].(string); ok {
					primaryCodec = codec
				}
				// Per-track buffer/jitter for primary video
				// 0 is valid (perfect conditions with no buffer delay or jitter)
				if buffer, ok := track["buffer"].(int); ok {
					primaryVideoBufferMs = uint32(buffer)
					streamLifecycleUpdate.VideoBufferMs = &primaryVideoBufferMs
				}
				if jitter, ok := track["jitter"].(int); ok {
					primaryVideoJitterMs = uint32(jitter)
					streamLifecycleUpdate.VideoJitterMs = &primaryVideoJitterMs
				}
				// Build rich quality tier label: "1080p60 H264 @ 6Mbps"
				if primaryHeight > 0 {
					// Resolution tier
					var resolution string
					if primaryHeight >= 2160 {
						resolution = "2160p"
					} else if primaryHeight >= 1440 {
						resolution = "1440p"
					} else if primaryHeight >= 1080 {
						resolution = "1080p"
					} else if primaryHeight >= 720 {
						resolution = "720p"
					} else if primaryHeight >= 480 {
						resolution = "480p"
					} else {
						resolution = "SD"
					}

					// Append FPS if available
					if primaryFPS > 0 {
						resolution = fmt.Sprintf("%s%d", resolution, int(primaryFPS+0.5))
					}

					qualityTier = resolution

					// Add codec if available
					if primaryCodec != "" {
						qualityTier += " " + primaryCodec
					}

					// Add bitrate if available
					if primaryBitrate > 0 {
						if primaryBitrate >= 1000 {
							qualityTier += fmt.Sprintf(" @ %.1fMbps", float64(primaryBitrate)/1000)
						} else {
							qualityTier += fmt.Sprintf(" @ %dkbps", primaryBitrate)
						}
					}
				}
			}

			// Extract primary audio track info
			if trackType == "audio" && !foundAudio {
				foundAudio = true
				if channels, ok := track["channels"].(int); ok && channels > 0 {
					ch := uint32(channels)
					streamLifecycleUpdate.AudioChannels = &ch
				}
				if sampleRate, ok := track["sample_rate"].(int); ok && sampleRate > 0 {
					sr := uint32(sampleRate)
					streamLifecycleUpdate.AudioSampleRate = &sr
				}
				if codec, ok := track["codec"].(string); ok && codec != "" {
					streamLifecycleUpdate.AudioCodec = &codec
				}
				if bitrate, ok := track["bitrate_kbps"].(int); ok && bitrate > 0 {
					br := uint32(bitrate)
					streamLifecycleUpdate.AudioBitrate = &br
				}
			}
		}
	}

	// Start with MistServer's native issues (primary source of truth)
	// e.g., "HLSnoaudio!", "VeryLowBuffer", etc.
	hasIssues := false
	var issuesDesc []string

	if mistIssues, ok := healthData["issues"].(string); ok && mistIssues != "" {
		hasIssues = true
		issuesDesc = append(issuesDesc, mistIssues)
	}

	// Calculate packet loss ratio from streamData (already extracted above)
	var packetLossRatio float64
	if packsent, ok := streamData["packsent"].(float64); ok && packsent > 0 {
		if packloss, ok := streamData["packloss"].(float64); ok {
			packetLossRatio = packloss / packsent
		}
	}

	// Append Helmsman's derived analysis (supplementary diagnostics)
	if packetLossRatio > 0.05 {
		hasIssues = true
		issuesDesc = append(issuesDesc, fmt.Sprintf("High packet loss: %.2f%%", packetLossRatio*100))
	} else if packetLossRatio > 0.01 {
		hasIssues = true
		issuesDesc = append(issuesDesc, fmt.Sprintf("Moderate packet loss: %.2f%%", packetLossRatio*100))
	}

	for _, track := range trackDetails {
		if jitter, ok := track["jitter"].(int); ok && jitter > 100 {
			hasIssues = true
			issuesDesc = append(issuesDesc, fmt.Sprintf("High jitter on track %v", track["track_name"]))
		}
		if buffer, ok := track["buffer"].(int); ok && buffer < 50 {
			hasIssues = true
			issuesDesc = append(issuesDesc, fmt.Sprintf("Low buffer on track %v", track["track_name"]))
		}
	}

	// Set issue indicators
	streamLifecycleUpdate.HasIssues = &hasIssues
	if len(issuesDesc) > 0 {
		issues := strings.Join(issuesDesc, "; ")
		streamLifecycleUpdate.IssuesDescription = &issues
	}
	if qualityTier != "" {
		streamLifecycleUpdate.QualityTier = &qualityTier
	}
	if primaryWidth > 0 {
		streamLifecycleUpdate.PrimaryWidth = &primaryWidth
	}
	if primaryHeight > 0 {
		streamLifecycleUpdate.PrimaryHeight = &primaryHeight
	}
	if primaryFPS > 0 {
		primaryFPSFloat32 := float32(primaryFPS)
		streamLifecycleUpdate.PrimaryFps = &primaryFPSFloat32
	}
	if primaryBitrate > 0 {
		streamLifecycleUpdate.PrimaryBitrate = &primaryBitrate
	}
	if primaryCodec != "" {
		streamLifecycleUpdate.PrimaryCodec = &primaryCodec
	}
	if trackCount > 0 {
		count := int32(trackCount)
		streamLifecycleUpdate.TrackCount = &count
	}

	return &ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE",
		NodeId:      nodeID,
		Timestamp:   time.Now().Unix(),
		Blocking:    false,
		RequestId:   "",
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: streamLifecycleUpdate,
		},
	}
}

func streamAPIHasLiveMedia(streamData map[string]any, trackDetails []map[string]any, trackCount int) bool {
	if inputs, ok := streamData["inputs"].(float64); ok && inputs > 0 {
		return true
	}
	if _, ok := streamData["inputs"]; ok {
		return false
	}
	if trackCount > 0 || len(trackDetails) > 0 {
		return true
	}
	return false
}

func mistStreamReplicated(streamData map[string]any) bool {
	replicated, ok := mistStreamReplicatedValue(streamData)
	return ok && replicated
}

func mistStreamReplicatedValue(streamData map[string]any) (bool, bool) {
	if streamData == nil {
		return false, false
	}
	if replicated, ok := streamData["replicated"].(bool); ok {
		return replicated, true
	}
	if replicated, ok := streamData["rep"].(bool); ok {
		return replicated, true
	}
	if hasMistTag(streamData["tags"], "replicated") {
		return true, true
	}
	return false, false
}

func hasMistTag(value any, want string) bool {
	switch tags := value.(type) {
	case []any:
		for _, tag := range tags {
			if s, ok := tag.(string); ok && s == want {
				return true
			}
		}
	case []string:
		for _, tag := range tags {
			if tag == want {
				return true
			}
		}
	case string:
		for tag := range strings.FieldsSeq(tags) {
			if tag == want {
				return true
			}
		}
	}
	return false
}

// convertClientAPIToMistTrigger converts client API response data to a MistTrigger protobuf
func convertClientAPIToMistTrigger(nodeID, _streamName, internalName, protocol, host, sessionID string, connectionTime, position float64, bandwidthIn, bandwidthOut float64, bytesDown, bytesUp, packetsSent, packetsLost, packetsRetransmitted int64, _logger logging.Logger) *ipcpb.MistTrigger {
	clientLifecycleUpdate := &ipcpb.ClientLifecycleUpdate{
		NodeId:       nodeID,
		InternalName: internalName,
		Action:       "connect",
		Protocol:     protocol,
		Host:         host,
	}

	// Add optional fields if present
	if sessionID != "" {
		clientLifecycleUpdate.SessionId = &sessionID
	}
	// Note: 0 is valid for all these metrics (e.g., client just connected)
	connectionTimeFloat32 := float32(connectionTime)
	clientLifecycleUpdate.ConnectionTime = &connectionTimeFloat32
	positionFloat32 := float32(position)
	clientLifecycleUpdate.Position = &positionFloat32
	bandwidthInUint64 := uint64(bandwidthIn)
	clientLifecycleUpdate.BandwidthInBps = &bandwidthInUint64
	bandwidthOutUint64 := uint64(bandwidthOut)
	clientLifecycleUpdate.BandwidthOutBps = &bandwidthOutUint64
	bytesDownUint64 := uint64(bytesDown)
	clientLifecycleUpdate.BytesDownloaded = &bytesDownUint64
	bytesUpUint64 := uint64(bytesUp)
	clientLifecycleUpdate.BytesUploaded = &bytesUpUint64
	// Always set packet stats - 0 is a valid value (e.g., HLS doesn't track packets)
	// These fields are explicitly requested from MistServer, so we always have them
	packetsSentUint64 := uint64(packetsSent)
	clientLifecycleUpdate.PacketsSent = &packetsSentUint64
	packetsLostUint64 := uint64(packetsLost)
	clientLifecycleUpdate.PacketsLost = &packetsLostUint64
	packetsRetransmittedUint64 := uint64(packetsRetransmitted)
	clientLifecycleUpdate.PacketsRetransmitted = &packetsRetransmittedUint64

	eventID := uuid.NewString()
	clientLifecycleUpdate.EventId = &eventID

	return &ipcpb.MistTrigger{
		TriggerType: "CLIENT_LIFECYCLE_UPDATE",
		NodeId:      nodeID,
		Timestamp:   time.Now().Unix(),
		Blocking:    false,
		RequestId:   "",
		TriggerPayload: &ipcpb.MistTrigger_ClientLifecycleUpdate{
			ClientLifecycleUpdate: clientLifecycleUpdate,
		},
	}
}
