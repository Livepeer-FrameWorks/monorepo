package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/balancer"
	foghornconfig "frameworks/api_balancing/internal/config"
	"frameworks/api_balancing/internal/configseedackoutbox"
	"frameworks/api_balancing/internal/control"
	foghornmigrations "frameworks/api_balancing/internal/datamigrations"
	"frameworks/api_balancing/internal/federation"
	foghorngrpc "frameworks/api_balancing/internal/grpc"
	"frameworks/api_balancing/internal/handlers"
	"frameworks/api_balancing/internal/identity"
	"frameworks/api_balancing/internal/jobs"
	"frameworks/api_balancing/internal/managedplacementoutbox"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/orchestrator"
	"frameworks/api_balancing/internal/pushstatusoutbox"
	"frameworks/api_balancing/internal/signingkeyuseoutbox"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/storage"
	"frameworks/api_balancing/internal/triggers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	foghornpool "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	navclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/navigator"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mediakeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"
)

type clientState struct {
	mu               sync.RWMutex
	quartermasterOK  bool
	quartermasterErr error
	commodoreOK      bool
	commodoreErr     error
}

type cacheMetricVectors struct {
	hits   *prometheus.CounterVec
	misses *prometheus.CounterVec
	stale  *prometheus.CounterVec
	stores *prometheus.CounterVec
	errors *prometheus.CounterVec
}

// newServiceCache binds cache activity to a fixed namespace. Cache hooks
// receive the full lookup key, which can be a publishing credential or client
// address and must never become a Prometheus label.
func newServiceCache(namespace string, ttl, swr, negTTL time.Duration, maxEntries int, metrics cacheMetricVectors) *cache.Cache {
	return cache.New(cache.Options{
		TTL:                  ttl,
		StaleWhileRevalidate: swr,
		NegativeTTL:          negTTL,
		MaxEntries:           maxEntries,
	}, cache.MetricsHooks{
		OnHit:   func(map[string]string) { metrics.hits.WithLabelValues(namespace).Inc() },
		OnMiss:  func(map[string]string) { metrics.misses.WithLabelValues(namespace).Inc() },
		OnStale: func(map[string]string) { metrics.stale.WithLabelValues(namespace).Inc() },
		OnStore: func(labels map[string]string) { metrics.stores.WithLabelValues(namespace, labels["ok"]).Inc() },
		OnError: func(map[string]string) { metrics.errors.WithLabelValues(namespace).Inc() },
	})
}

var releaseReconcilerOnce sync.Once
var releaseReconcilerClient atomic.Pointer[qmclient.GRPCClient]

func (cs *clientState) setQuartermaster(ok bool, err error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.quartermasterOK = ok
	cs.quartermasterErr = err
}

func (cs *clientState) setCommodore(ok bool, err error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.commodoreOK = ok
	cs.commodoreErr = err
}

func (cs *clientState) quartermasterStatus() (bool, error) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.quartermasterOK, cs.quartermasterErr
}

func (cs *clientState) commodoreStatus() (bool, error) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.commodoreOK, cs.commodoreErr
}

// localBackendFingerprint returns the fingerprint of this cell's currently-configured local S3 store, so the cleaner
// can fail closed when a recorded backend_id no longer matches (a repoint). Empty when no local S3 is wired or its
// concrete type exposes no descriptor. An empty fingerprint does NOT disable the guard: cleanup treats a recorded
// non-empty backend_id with an empty local fingerprint as an unproven match and fails closed (only a NULL/empty
// recorded id routes to the current store).
func localBackendFingerprint(s3 artifacts.S3Client) string {
	bd, ok := s3.(interface {
		BackendDescriptor() (bucket, endpoint, region, prefix string)
	})
	if !ok {
		return ""
	}
	bucket, endpoint, region, prefix := bd.BackendDescriptor()
	return control.BackendFingerprint("s3", bucket, endpoint, region, prefix)
}

// buildFirstBootBackendAuthority assembles the first-boot authority from Quartermaster, which owns the full
// immutable descriptor tuple (bucket/endpoint/region/prefix). It is only consequential on a first boot of an
// established cluster; EstablishOrEnforceLocalBackend ignores it once an identity exists. Quartermaster is the sole source
// of the authority — no serving component is consulted — so establishment never depends on another service being ready and
// has no boot-ordering coupling. QM unreachable / no client → Established+!Complete (first boot fails closed). QM
// descriptor empty → Established+!Complete as well: Foghorn does not establish an identity from its own env, so an
// S3-enabled cell with no QM descriptor FAILS CLOSED until desired-state bootstrap establishes the descriptor in QM.
// QM descriptor set → established + complete.
func buildFirstBootBackendAuthority(qmClient *qmclient.GRPCClient, clusterID string, logger logging.Logger) control.LocalBackendAuthority {
	if qmClient == nil {
		logger.Warn("No Quartermaster client wired; a first boot cannot prove the existing backend and will fail closed")
		return control.LocalBackendAuthority{Established: true}
	}
	qmCtx, qmCancel := context.WithTimeout(context.Background(), 5*time.Second)
	cr, qErr := qmClient.GetCluster(qmCtx, clusterID)
	qmCancel()
	if qErr != nil {
		logger.WithError(qErr).Warn("Quartermaster unreachable for first-boot backend establishment; a first boot will fail closed until it is reachable")
		return control.LocalBackendAuthority{Established: true}
	}
	cl := cr.GetCluster()
	if cl == nil || strings.TrimSpace(cl.GetS3Bucket()) == "" {
		// Quartermaster has NO descriptor for this cell. Because this Foghorn reached here it HAS S3 configured (the
		// caller only builds the authority when S3 is enabled), and Quartermaster is the sole authority — Foghorn does
		// not establish an identity from its own env. Return Established (so the incomplete-descriptor guard fails the
		// boot closed) rather than committing from env: desired-state bootstrap must establish the descriptor in
		// Quartermaster first.
		logger.WithField("cluster_id", clusterID).Warn("Quartermaster has no S3 descriptor for this S3-enabled cell; first boot fails closed until desired-state bootstrap establishes it")
		return control.LocalBackendAuthority{Established: true}
	}
	if !cl.GetS3PrefixPresent() {
		// The descriptor has a bucket but its s3_prefix is NULL (unset). The tuple is INCOMPLETE: a COALESCE'd empty
		// prefix is not proof the real prefix is empty. Established-but-incomplete → first boot FAILS CLOSED until the
		// prefix is set by desired-state bootstrap, so Foghorn never commits an identity from an unset prefix.
		logger.WithField("cluster_id", clusterID).Warn("Quartermaster descriptor has a bucket but s3_prefix is unset (NULL); first boot fails closed until desired-state bootstrap establishes the complete descriptor")
		return control.LocalBackendAuthority{Established: true}
	}
	return control.LocalBackendAuthority{
		Established: true,
		Complete:    true,
		Bucket:      cl.GetS3Bucket(),
		Endpoint:    cl.GetS3Endpoint(),
		Region:      cl.GetS3Region(),
		Prefix:      cl.GetS3Prefix(),
	}
}

// Readiness-sentinel establishment tuning (package vars so tests can shrink them). Each PutObject attempt is bounded
// by its own timeout; on failure we back off and retry up to the attempt cap before the caller fails closed.
var (
	readinessSentinelAttempts       = 5
	readinessSentinelAttemptTimeout = 5 * time.Second
	readinessSentinelBackoff        = 2 * time.Second
)

// sentinelWriter is the write subset used to establish the readiness sentinel (satisfied by *storage.S3Client).
type sentinelWriter interface {
	PutObject(ctx context.Context, key string, body []byte, contentType string) error
}

// establishReadinessSentinel writes mediakeys.ReadinessSentinelKey CONVERGENTLY: bounded per-attempt timeout + backoff
// retry, so a transient S3 blip does not leave Chandler permanently unready. It returns an error only after the whole
// retry budget is exhausted (the caller then fails closed) or the parent context is cancelled. Idempotent — the PUT
// just overwrites. PutObject prepends the cell prefix, matching Chandler's read path.
func establishReadinessSentinel(ctx context.Context, w sentinelWriter, logger logging.Logger) error {
	var lastErr error
	for attempt := 1; attempt <= readinessSentinelAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, readinessSentinelAttemptTimeout)
		lastErr = w.PutObject(attemptCtx, mediakeys.ReadinessSentinelKey, []byte("ready\n"), "text/plain")
		cancel()
		if lastErr == nil {
			return nil
		}
		logger.WithError(lastErr).WithField("attempt", attempt).Warn("Chandler readiness sentinel write failed; retrying")
		if attempt < readinessSentinelAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(readinessSentinelBackoff):
			}
		}
	}
	return fmt.Errorf("establish readiness sentinel after %d attempts: %w", readinessSentinelAttempts, lastErr)
}

// clusterPeerBacking is the subset of a Quartermaster cluster-peer descriptor needed to derive an S3 backing.
// *cluster_peer.TenantClusterPeer satisfies it; the interface keeps backingFromPeer unit-testable without a full proto.
type clusterPeerBacking interface {
	GetS3Bucket() string
	GetS3Endpoint() string
	GetS3Region() string
	GetS3Prefix() string
	GetS3PrefixPresent() bool
}

// backingFromPeer derives a federation S3 backing from a cluster-peer descriptor, FAILING CLOSED on an incomplete
// one. A peer with no bucket, or a bucket but an incomplete (NULL) prefix, has no usable identity: its collapsed empty
// prefix could false-match a local empty prefix and mint to the wrong keyspace, so it reports (zero, false) and the
// resolver never local-mints against it. Only a fully-adopted descriptor (bucket + present prefix) yields a backing.
func backingFromPeer(peer clusterPeerBacking) (federation.S3Backing, bool) {
	bucket := peer.GetS3Bucket()
	if bucket == "" {
		return federation.S3Backing{}, false
	}
	if !peer.GetS3PrefixPresent() {
		return federation.S3Backing{}, false
	}
	return federation.S3Backing{
		Bucket:   bucket,
		Endpoint: peer.GetS3Endpoint(),
		Region:   peer.GetS3Region(),
		Prefix:   peer.GetS3Prefix(),
	}, true
}

func main() {
	if version.HandleCLI() {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "data-migrations" {
		logger := logging.NewLoggerWithService("foghorn")
		config.LoadEnv(logger)
		foghornmigrations.Register()
		dbConfig := database.DefaultConfig()
		dbConfig.ServiceName = "foghorn"
		dbConfig.URL = config.RequireEnv("DATABASE_URL")
		err := datamigrate.HandleArgv(context.Background(), func() (*sql.DB, error) {
			return database.Connect(dbConfig, logger)
		}, os.Stdout, os.Args[1:])
		if err != nil && !errors.Is(err, datamigrate.ErrNotDataMigrationsCommand) {
			logger.WithError(err).Fatal("Data migration command failed")
		}
		return
	}

	// Initialize logger
	logger := logging.NewLoggerWithService("foghorn")
	state.SetLogger(logger)

	// Load environment variables
	config.LoadEnv(logger)
	if err := control.ConfigureAdmissionEffectEncryption(os.Getenv("FOGHORN_STATE_ENCRYPTION_KEY")); err != nil {
		logger.WithError(err).Fatal("Foghorn durable state encryption is unavailable")
	}
	foghornCfg := foghornconfig.Load()
	control.SetLocalClusterID(foghornCfg.ClusterID)

	// Explicit platform-shared edge clusters: only on these may a tenantless node serve arbitrary tenants'
	// durable bytes (control.nodeMayServeTenant). Comma-separated cluster IDs. This is the operator-declared
	// source; the canonical Quartermaster is_platform_official set is loaded separately below. Unset here ⇒
	// only Quartermaster-official clusters qualify; if both are empty, tenantless nodes serve nothing.
	for _, c := range strings.Split(config.GetEnv("FOGHORN_PLATFORM_SHARED_CLUSTERS", ""), ",") {
		if c = strings.TrimSpace(c); c != "" {
			control.AddPlatformSharedCluster(c)
		}
	}

	// Storage base path for local-path reconstruction (DVR dispatch) when node
	// has no StorageLocal. Must match Helmsman's HELMSMAN_STORAGE_LOCAL_PATH.
	if storageBase := config.GetEnv("FOGHORN_DEFAULT_STORAGE_BASE", ""); storageBase != "" {
		if !filepath.IsAbs(storageBase) {
			logger.WithField("path", storageBase).Fatal("FOGHORN_DEFAULT_STORAGE_BASE must be absolute path")
		}
		control.SetDefaultStorageBase(storageBase)
		logger.WithField("storage_base", storageBase).Info("Using custom default storage base")
	}

	logger.WithField("service", "foghorn").Info("Starting Foghorn Load Balancer")

	// Service token for service-to-service authentication
	serviceToken := config.RequireEnv("SERVICE_TOKEN")

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "foghorn"
	dbURL := config.RequireEnv("DATABASE_URL")
	dbConfig.URL = dbURL
	db := database.MustConnect(dbConfig, logger)
	defer db.Close()
	migrationCtx, stopMigrations := context.WithCancel(context.Background())
	defer stopMigrations()
	go foghornmigrations.RunBackground(migrationCtx, db, logger)
	control.SetDB(db)
	defer control.SetDB(nil)

	// Create load balancer instance
	lb := balancer.NewLoadBalancer(logger)
	lb.SetClusterAccessAuthorizer(control.ClusterAccessibleForTenant)
	lb.SetClusterServeAuthorizer(control.ClusterServeAccessibleForScope)
	relayReady := false
	haRequired := config.GetEnvBool("FOGHORN_HA_REQUIRED", false)

	instanceID := config.GetEnv("FOGHORN_INSTANCE_ID", "")
	if instanceID == "" {
		instanceID = fmt.Sprintf("foghorn-%d", time.Now().UnixNano())
		if foghornCfg.Redis.Mode != "" || foghornCfg.RedisURL != "" {
			logger.Warn("FOGHORN_INSTANCE_ID not set but Redis is configured — ephemeral ID will not persist across restarts, breaking HA state sync and leader election")
		}
	}

	var redisClient goredis.UniversalClient
	if foghornCfg.Redis.Mode != "" {
		var err error
		redisClient, err = pkgredis.NewUniversalClient(context.Background(), foghornCfg.Redis)
		if err != nil {
			logger.WithError(err).Warn("Failed to initialize redis (universal mode)")
			redisClient = nil
		}
	} else if foghornCfg.RedisURL != "" {
		client, err := pkgredis.NewClientFromURL(context.Background(), foghornCfg.RedisURL)
		if err != nil {
			logger.WithError(err).Warn("Failed to initialize redis state store")
		} else {
			redisClient = client
		}
	}
	var redisStore *state.RedisStateStore
	if redisClient != nil {
		redisStore = state.NewRedisStateStore(redisClient, foghornCfg.ClusterID)
		if err := state.DefaultManager().EnableRedisSync(context.Background(), redisStore, instanceID, logger); err != nil {
			logger.WithError(err).Warn("Failed to enable redis state synchronization")
		}
		// Viewer-capacity leases are shared through Redis so USER_NEW and
		// USER_END may land on different replicas. Publisher capacity is
		// authoritative in PostgreSQL ingest sessions, not this cache.
		state.DefaultTenantCapacity().EnableRedisSync(redisClient, foghornCfg.ClusterID)
	}

	// Set weights from environment variables
	cpu := uint64(config.GetEnvInt("CPU_WEIGHT", 500))
	ram := uint64(config.GetEnvInt("RAM_WEIGHT", 500))
	bw := uint64(config.GetEnvInt("BANDWIDTH_WEIGHT", 1000))
	geo := uint64(config.GetEnvInt("GEO_WEIGHT", 1000))
	bonus := uint64(config.GetEnvInt("STREAM_BONUS", 50))

	if cpu > 0 && ram > 0 && bw > 0 && geo > 0 && bonus > 0 {
		lb.SetWeights(cpu, ram, bw, geo, bonus)
	}

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("foghorn", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("foghorn", version.Version, version.GitCommit)
	clients := &clientState{}
	clientStatusGauge := metricsCollector.NewGauge(
		"control_plane_client_status",
		"Control plane client availability (1=ok, 0=unavailable)",
		[]string{"client"},
	)
	clientReconnects := metricsCollector.NewCounter(
		"control_plane_client_reconnect_total",
		"Control plane client reconnect attempts",
		[]string{"client", "status"},
	)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL": dbURL,
	}))
	healthChecker.AddCheck("state_rehydrate", func() monitoring.CheckResult {
		at, errMsg := state.DefaultManager().RehydrateStatus()
		if errMsg != "" {
			return monitoring.CheckResult{
				Status:  "unhealthy",
				Message: fmt.Sprintf("last rehydrate error: %s", errMsg),
				Latency: time.Since(at).String(),
			}
		}
		if at.IsZero() {
			return monitoring.CheckResult{
				Status:  "healthy",
				Message: "rehydrate not yet run",
			}
		}
		return monitoring.CheckResult{
			Status:  "healthy",
			Message: "rehydrate ok",
			Latency: time.Since(at).String(),
		}
	})
	healthChecker.AddCheck("ha_relay", func() monitoring.CheckResult {
		return relayHealthResult(relayReady, haRequired)
	})
	healthChecker.AddCheck("quartermaster", func() monitoring.CheckResult {
		ok, err := clients.quartermasterStatus()
		if !ok {
			msg := "Quartermaster unavailable"
			if err != nil {
				msg = fmt.Sprintf("Quartermaster unavailable: %v", err)
			}
			return monitoring.CheckResult{
				Status:  monitoring.StatusDegraded,
				Message: msg,
			}
		}
		return monitoring.CheckResult{Status: monitoring.StatusHealthy}
	})
	healthChecker.AddCheck("commodore", func() monitoring.CheckResult {
		ok, err := clients.commodoreStatus()
		if !ok {
			msg := "Commodore unavailable"
			if err != nil {
				msg = fmt.Sprintf("Commodore unavailable: %v", err)
			}
			return monitoring.CheckResult{
				Status:  monitoring.StatusDegraded,
				Message: msg,
			}
		}
		return monitoring.CheckResult{Status: monitoring.StatusHealthy}
	})

	// Create custom load balancing metrics
	metrics := &handlers.FoghornMetrics{
		// Distribution across nodes is derived from
		// rate(foghorn_routing_decisions_total{selected_node=...}[5m]) at query
		// time; a per-routing-decision gauge would go stale on nodes that stop
		// being selected.
		RoutingDecisions:      metricsCollector.NewCounter("routing_decisions_total", "Routing decisions made", []string{"algorithm", "selected_node"}),
		NodeSelectionDuration: metricsCollector.NewHistogram("node_selection_duration_seconds", "Node selection latency", []string{}, nil),
		LivepeerAuthRejected: metricsCollector.NewCounter(
			"livepeer_auth_rejected_total",
			"Livepeer gateway auth-webhook rejections by reason",
			[]string{"reason"},
		),
		StorageMint: metricsCollector.NewCounter(
			"storage_mint_total",
			"MintStorageURLs federation-handler outcomes by result",
			[]string{"result"},
		),
		RoutingEventsShed: metricsCollector.NewCounter(
			"routing_events_shed_total",
			"Routing telemetry never delivered to Decklog, by reason",
			[]string{"reason"},
		),
	}

	// Control-plane (HelmsmanControl) and data-plane (Decklog fan-out) observability
	admissionPayloadCrypto := metricsCollector.NewCounter(
		"admission_payload_crypto_total",
		"Durable admission push-target payload opens and legacy-to-v2 migrations",
		[]string{"format", "result"},
	)
	for _, format := range []string{"plaintext", "v1", "v2"} {
		for _, result := range []string{"opened", "migrated", "error"} {
			admissionPayloadCrypto.WithLabelValues(format, result).Add(0)
		}
	}
	artifactDeletionOutcomes := metricsCollector.NewCounter(
		"artifact_deletion_outcomes_total",
		"Node-copy point deletion database decisions",
		[]string{"outcome"},
	)
	for _, outcome := range []string{"applied", "fenced", "absent", "parent_missing", "error"} {
		artifactDeletionOutcomes.WithLabelValues(outcome).Add(0)
	}
	control.SetMetrics(&control.ControlMetrics{
		MistTriggers: metricsCollector.NewCounter(
			"control_mist_triggers_total",
			"MistTrigger messages received/processed over the HelmsmanControl stream",
			[]string{"trigger_type", "blocking", "status"},
		),
		ArtifactSyncOutcomes: metricsCollector.NewCounter(
			"artifact_sync_outcomes_total",
			"Artifact SyncComplete outcomes from Helmsman (success/failed/lost_local/dtsh_failed)",
			[]string{"outcome"},
		),
		ArtifactDeletionOutcomes: artifactDeletionOutcomes,
		MediaRequestCentralRPCs: metricsCollector.NewCounter(
			"media_request_central_rpcs_total",
			"Logical control-plane client invocations made beneath media request paths; internal retries are excluded",
			[]string{"path", "service", "method"},
		),
		NodeAdmissionEvents: metricsCollector.NewCounter(
			"node_admission_events_total",
			"Durable media-cell node admission operations by outcome",
			[]string{"operation", "result"},
		),
		AdmissionPayloadCrypto: admissionPayloadCrypto,
	})
	go control.RunAdmissionEffectEncryptionMigration(context.Background(), logger)

	// Wire state metrics hooks
	stateWrites := metricsCollector.NewCounter("state_writes_total", "State write-through operations", []string{"entity", "op"})
	rehydrateDur := metricsCollector.NewHistogram("state_rehydrate_seconds", "State rehydrate duration", []string{"entity"}, nil)
	state.SetMetricsHooks(
		func(labels map[string]string) { stateWrites.WithLabelValues(labels["entity"], labels["op"]).Inc() },
		func(seconds float64, labels map[string]string) {
			rehydrateDur.WithLabelValues(labels["entity"]).Observe(seconds)
		},
	)

	// Cache metrics and factory
	cacheHits := metricsCollector.NewCounter("cache_hits_total", "Cache hits", []string{"cache"})
	cacheMiss := metricsCollector.NewCounter("cache_misses_total", "Cache misses", []string{"cache"})
	cacheStale := metricsCollector.NewCounter("cache_stale_total", "Cache stale served", []string{"cache"})
	cacheStore := metricsCollector.NewCounter("cache_store_total", "Cache stores", []string{"cache", "ok"})
	cacheError := metricsCollector.NewCounter("cache_errors_total", "Cache load errors", []string{"cache"})

	cacheMetrics := cacheMetricVectors{
		hits: cacheHits, misses: cacheMiss, stale: cacheStale,
		stores: cacheStore, errors: cacheError,
	}

	// Register DB connection-pool stats (open/in-use/idle gauges +
	// wait_count/wait_duration counters) sourced from db.Stats() at
	// scrape time.
	metricsCollector.RegisterDBStats(db)

	// --- Initialize Clients (Lifted from Handlers) ---

	decklogGRPCAddr := config.GetEnv("DECKLOG_GRPC_ADDR", "decklog:18006")
	allowInsecure := config.GetEnvBool("GRPC_ALLOW_INSECURE", false)
	decklogConfig := decklog.BatchedClientConfig{
		Target:        decklogGRPCAddr,
		AllowInsecure: allowInsecure,
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("decklog"),
		Timeout:       10 * time.Second,
		Source:        "foghorn",
		ServiceToken:  serviceToken,
		ClusterID:     config.GetEnv("CLUSTER_ID", ""),
		SourceRegion:  config.GetEnv("REGION", ""),
	}
	decklogClient, err := decklog.NewBatchedClient(decklogConfig, logger)
	if err != nil {
		logger.WithError(err).Error("Failed to initialize Decklog gRPC client")
	}

	// Artifact-lifecycle + federation events enqueue into
	// foghorn.artifact_event_outbox; the drain worker dispatches to
	// Decklog with exponential backoff.
	artifactoutbox.Init(db, logger, decklogClient)
	go artifactoutbox.RunWorker(context.Background())

	// Quartermaster (gRPC)
	quartermasterGRPCURL := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
	qmClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      quartermasterGRPCURL,
		Timeout:       30 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("quartermaster"),
	})
	if err != nil {
		logger.WithError(err).Error("Failed to create Quartermaster gRPC client - starting in degraded mode")
		clients.setQuartermaster(false, err)
		clientStatusGauge.WithLabelValues("quartermaster").Set(0)
		qmClient = nil
	} else {
		clients.setQuartermaster(true, nil)
		clientStatusGauge.WithLabelValues("quartermaster").Set(1)
	}
	if qmClient != nil {
		defer func() { _ = qmClient.Close() }()
	}
	control.SetQuartermasterClient(qmClient)
	startReleaseReconciler(qmClient, logger)

	// Commodore (gRPC)
	commodoreGRPCURL := config.GetEnv("COMMODORE_GRPC_ADDR", "commodore:19001")

	// Commodore Cache
	ttl := 60 * time.Second
	if v := config.GetEnv("COMMODORE_CACHE_TTL", ""); v != "" {
		if d, errParse := time.ParseDuration(v); errParse == nil {
			ttl = d
		}
	}
	swr := 30 * time.Second
	if v := config.GetEnv("COMMODORE_CACHE_SWR", ""); v != "" {
		if d, errParse := time.ParseDuration(v); errParse == nil {
			swr = d
		}
	}
	neg := 10 * time.Second
	if v := config.GetEnv("COMMODORE_CACHE_NEG_TTL", ""); v != "" {
		if d, errParse := time.ParseDuration(v); errParse == nil {
			neg = d
		}
	}
	maxEntries := 10000
	if v := config.GetEnv("COMMODORE_CACHE_MAX", ""); v != "" {
		if n, errParse := strconv.Atoi(v); errParse == nil && n > 0 {
			maxEntries = n
		}
	}
	// Use the cache factory from main
	commodoreCache := newServiceCache("commodore", ttl, swr, neg, maxEntries, cacheMetrics)

	commodoreClient, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:      commodoreGRPCURL,
		Timeout:       30 * time.Second,
		Logger:        logger,
		Cache:         commodoreCache,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("commodore"),
	})
	if err != nil {
		logger.WithError(err).Error("Failed to create Commodore gRPC client - starting in degraded mode")
		clients.setCommodore(false, err)
		clientStatusGauge.WithLabelValues("commodore").Set(0)
		commodoreClient = nil
	} else {
		clients.setCommodore(true, nil)
		clientStatusGauge.WithLabelValues("commodore").Set(1)
	}
	if commodoreClient != nil {
		defer func() { _ = commodoreClient.Close() }()
	}

	configSeedApplyAckMetrics := &configseedackoutbox.Metrics{
		Pending: metricsCollector.NewGauge(
			"config_seed_apply_ack_outbox_pending",
			"ConfigSeed apply ACK outbox rows awaiting Navigator delivery",
			[]string{},
		),
		OldestPendingSeconds: metricsCollector.NewGauge(
			"config_seed_apply_ack_outbox_oldest_pending_seconds",
			"Age in seconds of the oldest ConfigSeed apply ACK awaiting Navigator delivery",
			[]string{},
		),
		Quarantined: metricsCollector.NewGauge(
			"config_seed_apply_ack_outbox_quarantined",
			"ConfigSeed apply ACK outbox rows quarantined for invalid durable payloads",
			[]string{},
		),
		Outcomes: metricsCollector.NewCounter(
			"config_seed_apply_ack_outbox_outcomes_total",
			"ConfigSeed apply ACK durable outbox operations by bounded outcome",
			[]string{"outcome"},
		),
	}
	for _, outcome := range []string{"enqueued", "deduplicated", "stale", "enqueue_error", "delivered", "retry", "retry_error", "settle_error", "superseded", "quarantined", "quarantine_error", "scan_error"} {
		configSeedApplyAckMetrics.Outcomes.WithLabelValues(outcome).Add(0)
	}
	configSeedApplyAckWriter := configseedackoutbox.NewWriter(db, configSeedApplyAckMetrics)
	control.SetConfigSeedApplyAckWriter(configSeedApplyAckWriter)
	configSeedAckOutboxCtx, cancelConfigSeedAckOutbox := context.WithCancel(context.Background())
	var configSeedAckOutboxWG sync.WaitGroup
	startConfigSeedAckOutboxLoop := func(run func(context.Context)) {
		configSeedAckOutboxWG.Add(1)
		go func() {
			defer configSeedAckOutboxWG.Done()
			run(configSeedAckOutboxCtx)
		}()
	}
	var stopConfigSeedAckOutboxOnce sync.Once
	stopConfigSeedAckOutbox := func() {
		stopConfigSeedAckOutboxOnce.Do(func() {
			cancelConfigSeedAckOutbox()
			configSeedAckOutboxWG.Wait()
		})
	}
	startConfigSeedAckOutboxLoop(func(ctx context.Context) {
		configseedackoutbox.RunMetrics(ctx, db, configSeedApplyAckMetrics, logger)
	})

	// Navigator is optional; dev compose does not run it, and the client
	// connects lazily, so an unset address is the only reliable off switch.
	navigatorAddr := strings.TrimSpace(config.GetEnv("NAVIGATOR_GRPC_ADDR", ""))
	if navigatorAddr == "" {
		logger.Info("Navigator gRPC address not configured; TLS bundles will not be seeded")
	} else {
		navigatorClient, navErr := navclient.NewClient(navclient.Config{
			Addr:          navigatorAddr,
			Timeout:       10 * time.Second,
			Logger:        logger,
			ServiceToken:  serviceToken,
			AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
			CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
			ServerName:    config.GetServiceGRPCTLSServerName("navigator"),
		})
		if navErr != nil {
			logger.WithError(navErr).Warn("Failed to create Navigator gRPC client - TLS bundles will not be seeded")
			navigatorClient = nil
		} else {
			defer navigatorClient.Close()
			control.SetNavigatorClient(navigatorClient)
			configSeedAckWorker := configseedackoutbox.NewWorker(db, navigatorClient, logger, configSeedApplyAckMetrics)
			startConfigSeedAckOutboxLoop(configSeedAckWorker.Run)
		}
	}
	// Register after the Navigator client's Close defer so LIFO shutdown cancels
	// and joins every worker before closing the RPC transport it may still use.
	defer stopConfigSeedAckOutbox()
	// Purser (gRPC) - x402 settlement + billing checks
	purserGRPCURL := config.GetEnv("PURSER_GRPC_ADDR", "purser:19003")
	purserClient, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:      purserGRPCURL,
		Timeout:       30 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("purser"),
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Purser gRPC client - x402 payments will be unavailable")
		purserClient = nil
	} else {
		defer purserClient.Close()
	}

	// GeoIP
	var geoipReader *geoip.Reader
	var geoipCache *cache.Cache
	geoipReader = geoip.GetSharedReader()
	if geoipReader != nil {
		gttl := 300 * time.Second
		gswr := 120 * time.Second
		gneg := 60 * time.Second
		gmax := 50000
		if v := config.GetEnv("GEOIP_CACHE_TTL", ""); v != "" {
			if d, errParse := time.ParseDuration(v); errParse == nil {
				gttl = d
			}
		}
		if v := config.GetEnv("GEOIP_CACHE_SWR", ""); v != "" {
			if d, errParse := time.ParseDuration(v); errParse == nil {
				gswr = d
			}
		}
		if v := config.GetEnv("GEOIP_CACHE_NEG_TTL", ""); v != "" {
			if d, errParse := time.ParseDuration(v); errParse == nil {
				gneg = d
			}
		}
		if v := config.GetEnv("GEOIP_CACHE_MAX", ""); v != "" {
			if n, errParse := strconv.Atoi(v); errParse == nil && n > 0 {
				gmax = n
			}
		}
		geoipCache = newServiceCache("geoip", gttl, gswr, gneg, gmax, cacheMetrics)
		logger.Info("GeoIP reader initialized successfully with cache")
	} else {
		logger.Info("GeoIP disabled (no GEOIP_MMDB_PATH or failed to load)")
	}

	// S3 Cold Storage Client (optional - only if STORAGE_S3_BUCKET is configured)
	// Credentials stay in Foghorn; edge nodes receive presigned URLs only
	//
	// IMPORTANT: We use interface types to avoid Go's typed nil pointer issue.
	// A typed nil (*storage.S3Client)(nil) passed as an interface is NOT nil,
	// but calling methods on it panics. Using interface type ensures true nil.
	// We need separate interface variables because different consumers expect
	// different interface types (foghorngrpc.S3ClientInterface vs jobs.S3Client).
	var s3ForGRPC foghorngrpc.S3ClientInterface
	var s3ForReconciler jobs.ReconcilerS3Client
	var s3ForFederation *storage.S3Client
	// localS3Backing captures the bucket/endpoint/region tuple this Foghorn
	// pool is configured against. Used by federation ownership checks
	// (PrepareArtifact redirect emit, MintStorageURLs callee validation)
	// and by the storage resolver factory to decide local vs federated mint.
	var localS3Backing storage.S3Backing
	if s3Bucket := config.GetEnv("STORAGE_S3_BUCKET", ""); s3Bucket != "" {
		s3Config := storage.S3Config{
			Bucket:    s3Bucket,
			Prefix:    config.GetEnv("STORAGE_S3_PREFIX", ""),
			Region:    config.GetEnv("STORAGE_S3_REGION", "us-east-1"),
			Endpoint:  config.GetEnv("STORAGE_S3_ENDPOINT", ""),
			AccessKey: config.GetEnv("STORAGE_S3_ACCESS_KEY", ""),
			SecretKey: config.GetEnv("STORAGE_S3_SECRET_KEY", ""),
		}
		localS3Backing = storage.S3Backing{
			Bucket:   s3Config.Bucket,
			Endpoint: s3Config.Endpoint,
			Region:   s3Config.Region,
			Prefix:   s3Config.Prefix,
		}
		client, s3Err := storage.NewS3Client(s3Config, logger)
		if s3Err != nil {
			// STORAGE_S3_BUCKET is configured, so durable storage is REQUIRED. Starting with S3 disabled would let a
			// cell run without its committed backend (and bypass the immutability guard). Fail closed.
			logger.WithError(s3Err).Fatal("STORAGE_S3_BUCKET is configured but the S3 client failed to initialize; refusing to start without durable storage")
		} else {
			// Only assign to interfaces if successfully created (avoids typed nil issue)
			s3ForGRPC = client
			s3ForReconciler = client
			s3ForFederation = client
			control.SetS3Client(client)
			// Enforce the immutable-backend invariant. On steady-state boots the committed cell_storage_identity governs
			// (exact descriptor match), so no Quartermaster lookup belongs on the restart path. On a FIRST boot
			// (freshly-created identity table) of an ESTABLISHED cluster
			// (existing data — the Quartermaster row carries a descriptor), Foghorn must PROVE the existing backend
			// before recording an identity: Quartermaster is the SOLE authority and supplies the FULL tuple
			// (bucket/endpoint/region/prefix), and the env must match all four. No serving component is consulted, so
			// establishment has no dependency on another service being ready. If the authority cannot be positively read (Quartermaster
			// unreachable or its descriptor incomplete), the first boot FAILS CLOSED — no identity is recorded — so a
			// repointed/unproven descriptor can never strand historical bytes. An S3-enabled cell whose Quartermaster
			// descriptor is EMPTY also fails closed (buildFirstBootBackendAuthority returns established-but-incomplete):
			// Quartermaster is the sole authority, so desired-state bootstrap must establish the descriptor there first.
			// Foghorn does not establish an identity from its own env.
			committed, committedErr := control.LocalBackendCommitted(context.Background(), db)
			if committedErr != nil {
				logger.WithError(committedErr).Fatal("Refusing to start: could not read this cell's immutable S3 backend identity")
			}
			auth := control.LocalBackendAuthority{}
			if !committed {
				auth = buildFirstBootBackendAuthority(qmClient, foghornCfg.ClusterID, logger)
			}
			if bErr := control.EstablishOrEnforceLocalBackend(context.Background(), db, auth); bErr != nil {
				logger.WithError(bErr).Fatal("Refusing to start: could not prove or match this cell's immutable S3 backend (backend repointing is not supported)")
			}
			logger.WithFields(logging.Fields{
				"bucket": s3Bucket,
				"prefix": s3Config.Prefix,
			}).Info("S3 cold storage enabled")
			// Establish the readiness sentinel Chandler reads to prove GetObject on the served namespace, BEFORE this
			// process starts serving — so the object exists ahead of Chandler's readiness gate (the planner also orders
			// Chandler after Foghorn). CONVERGENT and BOUNDED: each attempt has its own deadline and we retry with
			// backoff; a persistent failure is FATAL so the service manager restarts and reconverges (Foghorn cannot
			// publish thumbnails without a writable backend anyway). Idempotent — a re-boot just re-PUTs it.
			if sErr := establishReadinessSentinel(context.Background(), client, logger); sErr != nil {
				logger.WithError(sErr).Fatal("Refusing to start: could not establish the Chandler readiness sentinel after retries; the thumbnail backend is not writable")
			}
		}
	} else {
		// No STORAGE_S3_BUCKET. If this cell ALREADY committed an immutable backend identity, starting S3-disabled
		// would strand every artifact recorded against that backend — refuse. A genuinely storage-less cell (that
		// never committed one) may start.
		if committed, cErr := control.LocalBackendCommitted(context.Background(), db); cErr != nil {
			logger.WithError(cErr).Fatal("Could not check committed storage identity; refusing to start")
		} else if committed {
			logger.Fatal("This cell committed an immutable S3 backend on a prior boot, but STORAGE_S3_BUCKET is now unset; refusing to start S3-disabled (restore the original descriptor — credentials may rotate)")
		}
		// On a clean redeploy cell_storage_identity is empty, so the committed check above cannot catch a MISCONFIGURED
		// storage-less start. A storage-less start requires POSITIVE PROOF — a missing/unreachable authority is NOT
		// proof, and silently disabling durable storage would strand every durable write after a config omission. Valid
		// only when: STORAGE_MODE=none is explicitly set, OR a REACHABLE Quartermaster declares NO S3 backend for this
		// cluster. Otherwise (unreachable QM, no QM client, or QM declares a bucket) fail closed.
		if strings.EqualFold(strings.TrimSpace(config.GetEnv("STORAGE_MODE", "")), "none") {
			logger.Info("S3 cold storage disabled (STORAGE_MODE=none)")
		} else if qmClient == nil {
			logger.Fatal("No STORAGE_S3_BUCKET and no Quartermaster client to confirm this cell is storage-less; refusing to start S3-disabled (set STORAGE_MODE=none for a genuinely storage-less cell, or set STORAGE_S3_*)")
		} else {
			qmCtx, qmCancel := context.WithTimeout(context.Background(), 5*time.Second)
			cr, qErr := qmClient.GetCluster(qmCtx, foghornCfg.ClusterID)
			qmCancel()
			switch {
			case qErr != nil:
				logger.WithError(qErr).Fatal("No STORAGE_S3_BUCKET and Quartermaster is unreachable to confirm this cell is storage-less; refusing to start S3-disabled (durable writes would silently fail). Restore connectivity, set STORAGE_S3_*, or set STORAGE_MODE=none for a genuinely storage-less cell")
			case cr.GetCluster() != nil && strings.TrimSpace(cr.GetCluster().GetS3Bucket()) != "":
				logger.WithField("cluster_id", foghornCfg.ClusterID).Fatal("Quartermaster declares an S3 backend for this cluster but STORAGE_S3_BUCKET is unset; refusing to start S3-disabled (durable writes would silently fail). Set STORAGE_S3_* to the cluster descriptor, or clear the cluster descriptor for a storage-less cell")
			default:
				logger.Info("S3 cold storage disabled (Quartermaster confirms no S3 backend for this cluster)")
			}
		}
	}

	// Initialize handlers with injected clients before bootstrap metadata is applied.
	handlers.Init(db, logger, lb, metrics, decklogClient, commodoreClient, purserClient, qmClient, geoipReader, geoipCache)

	internalGRPCBindAddr := config.GetEnv("FOGHORN_INTERNAL_GRPC_BIND_ADDR", ":18019")
	externalGRPCBindAddr := config.GetEnv("FOGHORN_EXTERNAL_GRPC_BIND_ADDR", ":18029")

	// Register at QM before federation starts so leadership events have stable
	// cluster ownership metadata. QM resolves the address as wireguard_ip >
	// internal_ip > external_ip.
	advertiseAddr := ""
	if qmClient != nil {
		grpcPort := config.GetEnvInt("FOGHORN_INTERNAL_GRPC_PORT", controlPortFromBindAddr(internalGRPCBindAddr, 18019))
		bsReq := &quartermasterpb.BootstrapServiceRequest{
			Type:      "foghorn",
			Version:   version.Version,
			Protocol:  "grpc",
			Port:      int32(grpcPort),
			ClusterId: &foghornCfg.ClusterID,
			Metadata: map[string]string{
				"foghorn_listener": "internal_control",
				"instance_id":      instanceID,
			},
		}
		if nodeID := config.GetEnv("NODE_ID", ""); nodeID != "" {
			bsReq.NodeId = &nodeID
		}
		if host := config.GetEnv("FOGHORN_HOST", ""); host != "" {
			bsReq.AdvertiseHost = &host
		}

		// Service discovery is a central-plane projection, not a prerequisite
		// for serving from durable local authority. Give a healthy Quartermaster
		// one short startup opportunity, then reconcile registration in the
		// background while Foghorn opens its local listeners.
		retryCfg := qmbootstrap.DefaultRetryConfig("foghorn")
		retryCfg.AttemptTimeout = 2 * time.Second
		retryCfg.MaxAttempts = 1
		bootstrapCtx, bootstrapCancel := context.WithTimeout(context.Background(), 2*time.Second)
		bsResp, bsErr := qmbootstrap.BootstrapServiceWithRetry(bootstrapCtx, qmClient, bsReq, logger, retryCfg)
		bootstrapCancel()
		if bsErr != nil {
			logger.WithError(bsErr).Warn("BootstrapService unavailable at startup; local media is starting and service registration will reconcile in the background")
			go func() {
				backgroundCfg := qmbootstrap.DefaultRetryConfig("foghorn")
				if _, reconcileErr := qmbootstrap.BootstrapServiceWithRetry(context.Background(), qmClient, bsReq, logger, backgroundCfg); reconcileErr != nil {
					logger.WithError(reconcileErr).Warn("Background Foghorn service registration stopped")
					return
				}
				logger.Info("Foghorn service registration reconciled after Quartermaster recovery")
			}()
		}
		if bsResp != nil {
			advertiseAddr = bsResp.GetAdvertiseAddr()
			handlers.ApplyBootstrapMetadata(bsResp)
		}
	}
	_, bootstrapOwnerTenantID := handlers.GetClusterInfo()

	// --- Federation (cross-cluster stream routing) ---
	federationEnabled := config.GetEnv("FEDERATION_ENABLED", "false") == "true"
	if federationEnabled {
		if qmClient == nil {
			logger.Fatal("FEDERATION_ENABLED requires Quartermaster authority")
		}
		federationCtx, federationCancel := context.WithTimeout(context.Background(), 2*time.Second)
		clusterResp, clusterErr := qmClient.GetCluster(federationCtx, foghornCfg.ClusterID)
		federationCancel()
		if clusterErr != nil || clusterResp.GetCluster() == nil {
			logger.WithError(clusterErr).Fatal("FEDERATION_ENABLED requires a current Quartermaster cluster descriptor")
		}
		cluster := clusterResp.GetCluster()
		if !cluster.GetIsActive() || !cluster.GetIsPlatformOfficial() {
			logger.WithFields(logging.Fields{
				"cluster_id":           foghornCfg.ClusterID,
				"is_active":            cluster.GetIsActive(),
				"is_platform_official": cluster.GetIsPlatformOfficial(),
			}).Fatal("FEDERATION_ENABLED is restricted to active platform-operated Foghorn clusters")
		}
		if config.GetEnvBool("GRPC_ALLOW_INSECURE", false) && !config.GetEnvBool("FEDERATION_ALLOW_INSECURE_DEV", false) {
			logger.Fatal("FEDERATION_ENABLED requires authenticated TLS; FEDERATION_ALLOW_INSECURE_DEV is permitted only for isolated development")
		}
	}
	var federationServer *federation.FederationServer
	var peerManager *federation.PeerManager
	var remoteEdgeCache *federation.RemoteEdgeCache
	var fedClient *federation.FederationClient
	var relayServer *foghorngrpc.RelayServer

	// advertisedBackingForTenant resolves the S3 backing tuple Quartermaster
	// has on record for (tenantID, clusterID) via cluster_peers metadata.
	// Used by the federation server's ownership checks and by the storage
	// resolver factory to decide local-mint vs federated-mint.
	tenantRoutingCache := cache.New(cache.Options{
		TTL:                  60 * time.Second,
		StaleWhileRevalidate: 0,
		NegativeTTL:          5 * time.Second,
		MaxEntries:           10000,
	}, cache.MetricsHooks{})
	resolveTenantRouting := func(ctx context.Context, tenantID string) *quartermasterpb.ClusterRoutingResponse {
		if qmClient == nil || tenantID == "" {
			return nil
		}
		v, ok, cacheErr := tenantRoutingCache.Get(ctx, "tenant:"+tenantID, func(loadCtx context.Context, _ string) (any, bool, error) {
			rctx, cancel := context.WithTimeout(loadCtx, 1*time.Second)
			defer cancel()
			resp, qErr := qmClient.GetClusterRouting(rctx, &quartermasterpb.GetClusterRoutingRequest{TenantId: tenantID})
			if qErr != nil {
				return nil, false, qErr
			}
			return resp, true, nil
		})
		if cacheErr != nil || !ok {
			return nil
		}
		routing, ok := v.(*quartermasterpb.ClusterRoutingResponse)
		if !ok {
			return nil
		}
		return routing
	}
	advertisedBackingForTenant := func(ctx context.Context, tenantID, clusterID string) (federation.S3Backing, bool) {
		routing := resolveTenantRouting(ctx, tenantID)
		if routing == nil {
			return federation.S3Backing{}, false
		}
		for _, peer := range routing.GetClusterPeers() {
			if peer.GetClusterId() != clusterID {
				continue
			}
			return backingFromPeer(peer)
		}
		return federation.S3Backing{}, false
	}

	if federationEnabled && redisClient != nil && qmClient != nil && bootstrapOwnerTenantID != "" {
		remoteEdgeCache = federation.NewRemoteEdgeCache(redisClient, foghornCfg.ClusterID, logger)

		federationServer = federation.NewFederationServer(federation.FederationServerConfig{
			Logger:                   logger,
			LB:                       lb,
			ClusterID:                foghornCfg.ClusterID,
			Cache:                    remoteEdgeCache,
			DB:                       db,
			S3Client:                 s3ForFederation,
			AllowFederationMutations: federationEnabled,
			LocalS3Backing: federation.S3Backing{
				Bucket:   localS3Backing.Bucket,
				Endpoint: localS3Backing.Endpoint,
				Region:   localS3Backing.Region,
				Prefix:   localS3Backing.Prefix,
			},
			AdvertisedBacking: advertisedBackingForTenant,
			IsServedCluster:   control.IsServedCluster,
		})

		fedPool := foghornpool.NewPool(foghornpool.PoolConfig{
			ServiceToken:  serviceToken,
			Logger:        logger,
			AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
			CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
			ServerName:    config.GetServiceGRPCTLSServerName("foghorn"),
		})
		defer fedPool.Close()

		peerManager = federation.NewPeerManager(federation.PeerManagerConfig{
			ClusterID:           foghornCfg.ClusterID,
			InstanceID:          instanceID,
			Pool:                fedPool,
			QM:                  qmClient,
			Cache:               remoteEdgeCache,
			Logger:              logger,
			DecklogClient:       decklogClient,
			OwnerTenantID:       bootstrapOwnerTenantID,
			SelfGeoFunc:         handlers.GetSelfGeo,
			CanPurgeMemberships: control.PurgeableAdmissionEffectFences,
			// Late-bound through the identity facade (wired further down,
			// after the stream registry exists) so ad attribution shares
			// the same front door and metrics as every other consumer.
			ArtifactTenantResolver: func(ctx context.Context, hashes []string) (map[string]string, error) {
				if r := identity.Default(); r != nil {
					return r.ResolveArtifactTenants(ctx, hashes)
				}
				return nil, nil
			},
		})
		defer peerManager.Close()

		fedClient = federation.NewFederationClient(federation.FederationClientConfig{
			Pool:   fedPool,
			Logger: logger,
		})

		logger.WithField("cluster_id", foghornCfg.ClusterID).Info("Federation enabled")
	} else if federationEnabled {
		logger.WithField("has_owner_tenant", bootstrapOwnerTenantID != "").Warn("Federation enabled but missing prerequisites (redis, quartermaster, and/or bootstrap owner tenant)")
	}

	// Initialize trigger processor (Lifted from Handlers)
	// Shared across the trigger processor (livepeer-gateway resolution) and
	// the per-request storage cluster resolvers below — same metric name,
	// label values distinguish service ("livepeer-gateway", "storage", ...).
	serviceResolutionRejected := metricsCollector.NewCounter(
		"service_resolution_rejected_total",
		"Service-discovery resolutions that ended without a usable target",
		[]string{"reason", "service"},
	)
	triggerProcessor := triggers.NewProcessor(logger, commodoreClient, decklogClient, lb, geoipReader)
	control.SetManagedStreamPlacementWriter(managedplacementoutbox.NewWriter(db))
	signingKeyUseWriter := signingkeyuseoutbox.NewWriter(db)
	signingKeyUseRecorder := signingkeyuseoutbox.NewAsyncRecorder(signingKeyUseWriter, logger)
	go signingKeyUseRecorder.Run(context.Background())
	triggerProcessor.SetSigningKeyUseRecorder(signingKeyUseRecorder)
	mediaAuthorityLocalReads := metricsCollector.NewCounter(
		"media_authority_local_reads_total",
		"Durable signed media-authority lookup outcomes",
		[]string{"index", "outcome"},
	)
	for _, index := range []string{"playback_id", "internal_name", "tenant", "publishing_credential", "tenant_ingest"} {
		for _, outcome := range []string{"absent", "unready", "valid", "soft_expired", "hard_expired", "denied", "error"} {
			mediaAuthorityLocalReads.WithLabelValues(index, outcome).Add(0)
		}
	}
	mediaAuthorityShadow := metricsCollector.NewCounter(
		"media_authority_shadow_total",
		"Connected-to-local authority comparison outcomes",
		[]string{"outcome"},
	)
	for _, outcome := range []string{
		"object_mismatch", "policy_not_comparable", "policy_mismatch", "tenant_unavailable", "object_promoted", "pair_promoted",
		"ingest_object_mismatch", "ingest_output_unavailable", "ingest_output_mismatch", "ingest_tenant_mismatch", "ingest_pair_promoted",
		"pull_source_mismatch", "pull_source_pair_promoted", "artifact_source_mismatch", "artifact_source_pair_promoted",
		"tenant_mismatch_lifecycle", "tenant_mismatch_billing_decision", "tenant_mismatch_billing_model",
		"tenant_mismatch_official_cluster", "tenant_mismatch_cluster_grants", "tenant_mismatch_resource_limits", "tenant_mismatch_allowances",
	} {
		mediaAuthorityShadow.WithLabelValues(outcome).Add(0)
	}
	triggerProcessor.SetMetrics(&triggers.ProcessorMetrics{
		BillingCacheEvents: metricsCollector.NewCounter(
			"billing_cache_events_total",
			"Tenant billing authority cache outcomes",
			[]string{"outcome"},
		),
		DecklogTriggerSends: metricsCollector.NewCounter(
			"decklog_trigger_sends_total",
			"Attempts and results when forwarding MistTriggers to Decklog",
			[]string{"trigger_type", "status"},
		),
		ServiceResolutionRejected: serviceResolutionRejected,
		PlaybackDenyTotal: metricsCollector.NewCounter(
			"playback_deny_total",
			"USER_NEW denies bucketed by structured reason",
			[]string{"reason"},
		),
		PlaybackWebhookErrors: metricsCollector.NewCounter(
			"playback_webhook_errors_total",
			"Customer-webhook failures during playback policy enforcement",
			[]string{"class"},
		),
		ClientLifecycleBatchDrops: metricsCollector.NewCounter(
			"client_lifecycle_batch_drops_total",
			"CLIENT_LIFECYCLE batcher outcomes (send_failed/retry_succeeded)",
			[]string{"reason"},
		),
		DrainDispatch: metricsCollector.NewCounter(
			"drain_dispatch_total",
			"Drain dispatches to the prior owner node when the DB-ordered source projection moves to a new node (ok/failed)",
			[]string{"result"},
		),
		MediaAuthorityLocalReads: mediaAuthorityLocalReads,
		MediaAuthorityShadow:     mediaAuthorityShadow,
	})
	if geoipReader != nil && geoipCache != nil {
		triggerProcessor.SetGeoIPCache(geoipCache)
	}
	triggerProcessor.SetQuartermasterClient(qmClient)
	if peerManager != nil {
		triggerProcessor.SetPeerNotifier(peerManager)
	}
	// Drain dispatcher: wire the projection-change (old-owner) drain path to the
	// long-lived Helmsman control stream. Uses the HA-aware SendDrainStream
	// so a replacement admitted on one Foghorn instance can still drain an
	// old owner connected to a peer instance.
	triggerProcessor.SetDrainStreamDispatcher(func(ctx context.Context, nodeID, runtimeName, reason, sourceGeneration, priorOwnerSourceGeneration string) error {
		return control.SendDrainStream(ctx, nodeID, &ipcpb.DrainStreamRequest{
			RuntimeName:                runtimeName,
			Reason:                     reason,
			SourceGeneration:           sourceGeneration,
			PriorOwnerSourceGeneration: priorOwnerSourceGeneration,
		})
	})
	logger.Info("Initialized trigger processor with Commodore, Decklog and Quartermaster clients")
	handlers.SetTriggerProcessor(triggerProcessor)

	if qmClient == nil {
		go reconnectQuartermaster(quartermasterGRPCURL, serviceToken, logger, clients, clientStatusGauge, clientReconnects, triggerProcessor)
	}
	commodoreReconnectNeeded := commodoreClient == nil

	// Start Helmsman control gRPC server with injected dependencies
	control.Init(logger, commodoreClient, triggerProcessor)
	control.SetGeoIPCache(geoipCache)

	// Unified stream registry: identity (push, pull, mist-native, federated
	// peers) + artifact bookkeeping + replication state, with a periodic
	// sweeper that ages stale Locations out.
	streamRegistry := control.NewStreamRegistry(commodoreClient, foghornCfg.ClusterID, 30*time.Second)
	streamRegistry.SetLivePresence(control.NewLivePresence(state.DefaultManager()))
	streamRegistry.SetMissLogger(func(_ context.Context, refKind, key string) {
		logger.WithFields(logging.Fields{
			"ref_kind": refKind,
			"key":      key,
		}).Debug("stream_registry.miss")
	})
	if redisClient != nil {
		registryRedis := control.NewRedisRegistryStore(redisClient, foghornCfg.ClusterID)
		if sources, artifacts, syncErr := streamRegistry.EnableRedisSync(context.Background(), registryRedis, instanceID, logger); syncErr != nil {
			logger.WithError(syncErr).Warn("Failed to enable stream-registry Redis sync")
		} else {
			logger.WithFields(logging.Fields{
				"sources":   sources,
				"artifacts": artifacts,
			}).Info("Stream-registry rehydrated from Redis")
		}
	}
	control.SetStreamRegistry(streamRegistry)
	streamRegistry.StartSweeper(context.Background(), 30*time.Second, 5*time.Minute)

	// Stale-on-transient-error window: expired registry entries may serve as
	// fallback while Commodore/SQL re-hydration fails transiently (never on
	// authoritative not-found). 0 disables stale serving.
	if staleMaxRaw := config.GetEnv("FOGHORN_REGISTRY_STALE_MAX", ""); staleMaxRaw != "" {
		if staleMax, parseErr := time.ParseDuration(staleMaxRaw); parseErr == nil && staleMax >= 0 {
			streamRegistry.SetStaleMax(staleMax)
		} else {
			logger.WithField("value", staleMaxRaw).Warn("Invalid FOGHORN_REGISTRY_STALE_MAX; keeping 5m default")
		}
	}
	registryResolutions := metricsCollector.NewCounter(
		"stream_registry_resolutions_total",
		"Stream registry resolve outcomes by entity and outcome",
		[]string{"entity", "outcome"},
	)
	for _, entity := range []string{"source", "artifact"} {
		for _, outcome := range []string{"cache_hit", "hydrated", "stale_served", "miss", "unavailable", "error"} {
			registryResolutions.WithLabelValues(entity, outcome).Add(0)
		}
	}
	var staleServeLogMu sync.Mutex
	var staleServeLogLast time.Time
	streamRegistry.SetResolveObserver(func(entity, outcome, key string) {
		registryResolutions.WithLabelValues(entity, outcome).Inc()
		if outcome != "stale_served" {
			return
		}
		// Rate-limited: during an outage every expired-entry lookup serves
		// stale; one warning per interval is signal, per lookup is noise.
		staleServeLogMu.Lock()
		shouldLog := time.Since(staleServeLogLast) > 30*time.Second
		if shouldLog {
			staleServeLogLast = time.Now()
		}
		staleServeLogMu.Unlock()
		if shouldLog {
			logger.WithFields(logging.Fields{
				"entity": entity,
				"key":    key,
			}).Warn("stream_registry serving stale entry; upstream resolver failing transiently")
		}
	})

	// Identity resolver facade: the single front door for stream/artifact
	// → tenant/cluster attribution, layered state → registry → Commodore.
	// All trigger handlers, gRPC surfaces, and federation paths resolve
	// through this instead of hand-rolling lookup chains.
	identityResolutions := metricsCollector.NewCounter(
		"identity_resolutions_total",
		"Identity resolver layer consults by kind, layer and outcome",
		[]string{"kind", "layer", "outcome"},
	)
	identityCfg := identity.Config{
		StreamState: func(internalName string) (identity.StreamStateView, bool) {
			ss := state.DefaultManager().GetStreamState(internalName)
			if ss == nil {
				return identity.StreamStateView{}, false
			}
			return identity.StreamStateView{
				StreamID:   ss.StreamID,
				PlaybackID: ss.PlaybackID,
				TenantID:   ss.TenantID,
				NodeID:     ss.NodeID,
			}, true
		},
		NodeCluster: func(nodeID string) string {
			if ns := state.DefaultManager().GetNodeState(nodeID); ns != nil {
				return ns.ClusterID
			}
			return ""
		},
		RegistrySource: func(ctx context.Context, internalName string) (identity.StreamIdentity, error) {
			entry, resolveErr := streamRegistry.ResolveSourceByInternalName(ctx, internalName)
			if resolveErr != nil {
				// The registry's sentinel is the authoritative "does not
				// exist" answer; everything else (Commodore RPC failure,
				// SQL outage) is transient and must not be negative-cached.
				if errors.Is(resolveErr, control.ErrUnknownStream) {
					return identity.StreamIdentity{}, identity.ErrNotFound
				}
				return identity.StreamIdentity{}, resolveErr
			}
			return identity.StreamIdentity{
				InternalName:    entry.InternalName,
				StreamID:        entry.StreamID,
				PlaybackID:      entry.PlaybackID,
				TenantID:        entry.TenantID,
				OriginClusterID: entry.OriginClusterID,
			}, nil
		},
		RegistryArtifact: func(ctx context.Context, artifactHash string) (identity.ArtifactIdentity, error) {
			entry, resolveErr := streamRegistry.ResolveArtifactByHash(ctx, db, artifactHash)
			if resolveErr != nil {
				if errors.Is(resolveErr, control.ErrUnknownArtifact) {
					return identity.ArtifactIdentity{}, identity.ErrNotFound
				}
				return identity.ArtifactIdentity{}, resolveErr
			}
			return identity.ArtifactIdentity{
				ArtifactHash:       entry.ArtifactHash,
				Kind:               entry.Kind.String(),
				InternalName:       entry.InternalName,
				StreamInternalName: entry.StreamInternal,
				StreamID:           entry.StreamID,
				TenantID:           entry.TenantID,
				OriginClusterID:    entry.OriginClusterID,
				StorageClusterID:   entry.StorageCluster,
			}, nil
		},
		ArtifactTenants: federation.NewDBArtifactTenantResolver(db),
		Observe: func(kind, layer, outcome string) {
			identityResolutions.WithLabelValues(kind, layer, outcome).Inc()
		},
	}
	if commodoreClient != nil {
		identityCfg.CommodoreArtifact = func(ctx context.Context, kind, artifactHash string) (identity.ArtifactIdentity, error) {
			switch kind {
			case "clip":
				resp, resolveErr := commodoreClient.ResolveClipHash(ctx, artifactHash)
				if resolveErr != nil || resp == nil || !resp.GetFound() {
					return identity.ArtifactIdentity{}, resolveErr
				}
				return identity.ArtifactIdentity{
					ArtifactHash:       artifactHash,
					Kind:               kind,
					InternalName:       resp.GetInternalName(),
					StreamInternalName: resp.GetStreamInternalName(),
					StreamID:           resp.GetStreamId(),
					TenantID:           resp.GetTenantId(),
					OriginClusterID:    resp.GetOriginClusterId(),
				}, nil
			case "vod", "chapter":
				resp, resolveErr := commodoreClient.ResolveVodHash(ctx, artifactHash)
				if resolveErr != nil || resp == nil || !resp.GetFound() {
					if resolveErr != nil {
						return identity.ArtifactIdentity{}, resolveErr
					}
					return identity.ArtifactIdentity{}, identity.ErrNotFound
				}
				resolvedKind := strings.TrimSpace(resp.GetContentType())
				if resolvedKind == "" {
					resolvedKind = "vod"
				}
				if kind == "chapter" && resolvedKind != "chapter" {
					return identity.ArtifactIdentity{}, identity.ErrNotFound
				}
				// VOD uploads have no parent stream; existing consumers
				// (freeze, mint) use the asset's own internal_name when no
				// parent exists. Chapters carry their live-stream parent.
				streamInternalName := strings.TrimSpace(resp.GetParentStreamInternalName())
				if streamInternalName == "" {
					streamInternalName = resp.GetInternalName()
				}
				return identity.ArtifactIdentity{
					ArtifactHash:       artifactHash,
					Kind:               resolvedKind,
					InternalName:       resp.GetInternalName(),
					StreamInternalName: streamInternalName,
					TenantID:           resp.GetTenantId(),
					OriginClusterID:    resp.GetOriginClusterId(),
				}, nil
			case "dvr":
				resp, resolveErr := commodoreClient.ResolveDVRHash(ctx, artifactHash)
				if resolveErr != nil || resp == nil || !resp.GetFound() {
					return identity.ArtifactIdentity{}, resolveErr
				}
				return identity.ArtifactIdentity{
					ArtifactHash:       artifactHash,
					Kind:               kind,
					InternalName:       resp.GetInternalName(),
					StreamInternalName: resp.GetStreamInternalName(),
					StreamID:           resp.GetStreamId(),
					TenantID:           resp.GetTenantId(),
					OriginClusterID:    resp.GetOriginClusterId(),
				}, nil
			default:
				return identity.ArtifactIdentity{}, identity.ErrNotFound
			}
		}
	}
	identity.SetDefault(identity.NewResolver(identityCfg))

	// Peer-relay capability grants: Redis-backed when available (any HA
	// instance can authorize a grant another minted), in-memory otherwise.
	// Without Redis a grant minted on one instance is invisible to peers, so a
	// serving edge whose AuthorizeRelayPull lands on a different instance gets a
	// false deny (401→502). That is fatal under FOGHORN_HA_REQUIRED and a loud
	// warning otherwise (single-instance deployments are unaffected).
	if redisClient == nil {
		if haRequired {
			logger.Fatal("FOGHORN_HA_REQUIRED is true but Redis is not configured — peer-relay grants cannot be shared across instances")
		}
		logger.Warn("Redis not configured — peer-relay capability grants are in-memory only; cross-instance AuthorizeRelayPull will deny. Safe for single-instance Foghorn, broken under HA.")
	}
	control.SetRelayGrantRedis(redisClient)
	control.StartRelayGrantSweeper(context.Background())

	// Configure unified state policies and rehydrate from DB (nodes, DVR, clips, artifacts)
	state.DefaultManager().ConfigurePolicies(state.PoliciesConfig{
		WritePolicies: map[state.EntityType]state.WritePolicy{
			state.EntityClip: {Enabled: true, Mode: state.WriteThrough},
			state.EntityDVR:  {Enabled: true, Mode: state.WriteThrough},
		},
		SyncPolicies: map[state.EntityType]state.SyncPolicy{
			state.EntityClip: {BootRehydrate: true, ReconcileInterval: 180 * time.Second},
			state.EntityDVR:  {BootRehydrate: true, ReconcileInterval: 180 * time.Second},
		},
		ClipRepo:     control.NewClipRepository(),
		DVRRepo:      control.NewDVRRepository(),
		NodeRepo:     control.NewNodeRepository(),
		ArtifactRepo: control.NewArtifactRepository(),
	})

	// Set artifact repository for control server handlers (dual-storage sync)
	control.SetArtifactRepository(control.NewArtifactRepository())

	// Create Foghorn control plane gRPC server (for Commodore: clips, DVR, viewer resolution, VOD uploads)
	foghornServer := foghorngrpc.NewFoghornGRPCServer(db, logger, lb, geoipReader, geoipCache, decklogClient, s3ForGRPC, purserClient)
	foghornServer.SetSigningKeyUseRecorder(signingKeyUseRecorder)
	foghornServer.SetClusterID(foghornCfg.ClusterID)
	foghornServer.SetLocalIngestResolver(triggerProcessor)
	foghornServer.SetLocalPlaybackPolicyEvaluator(triggerProcessor)
	var authorityStore *localauthority.Store
	mediaAuthorityCellID := strings.TrimSpace(os.Getenv("MEDIA_AUTHORITY_CELL_ID"))
	if encodedTrust := strings.TrimSpace(os.Getenv("MEDIA_AUTHORITY_TRUST_SET")); encodedTrust != "" {
		if mediaAuthorityCellID == "" {
			logger.Fatal("MEDIA_AUTHORITY_CELL_ID is required when signed media authority is enabled")
		}
		trust, trustErr := sharedauthority.ParseTrustSet(encodedTrust)
		if trustErr != nil {
			logger.WithError(trustErr).Fatal("Invalid MEDIA_AUTHORITY_TRUST_SET")
		}
		var storeErr error
		authorityStore, storeErr = localauthority.NewStore(db, mediaAuthorityCellID, trust)
		if storeErr != nil {
			logger.WithError(storeErr).Fatal("Failed to initialize media authority store")
		}
		if peerManager != nil {
			authorityStore.SetRuntimePeerResolver(peerManager)
		}
		mediaAuthorityApplyOutcomes := metricsCollector.NewCounter(
			"media_authority_apply_total",
			"Signed media-authority apply outcomes",
			[]string{"authority_kind", "outcome"},
		)
		for _, kind := range []string{"tenant", "media_object", "unknown"} {
			for _, outcome := range []string{"applied", "duplicate", "verification_rejected", "rollback_rejected", "conflict_rejected", "persist_error"} {
				mediaAuthorityApplyOutcomes.WithLabelValues(kind, outcome).Add(0)
			}
		}
		authorityStore.SetApplyOutcomeMetric(mediaAuthorityApplyOutcomes)
		sealKeyID := strings.TrimSpace(os.Getenv("MEDIA_AUTHORITY_SEAL_KEY_ID"))
		sealPrivateEncoded := strings.TrimSpace(os.Getenv("MEDIA_AUTHORITY_SEAL_PRIVATE_KEY_PEM_B64"))
		if sealKeyID != "" || sealPrivateEncoded != "" {
			if sealKeyID == "" || sealPrivateEncoded == "" {
				logger.Fatal("MEDIA_AUTHORITY_SEAL_KEY_ID and MEDIA_AUTHORITY_SEAL_PRIVATE_KEY_PEM_B64 must be configured together")
			}
			sealPrivateKey, sealErr := sharedauthority.ParseSealPrivateKey(sealPrivateEncoded)
			if sealErr != nil {
				logger.WithError(sealErr).Fatal("Invalid media authority seal private key")
			}
			if sealErr := authorityStore.SetSealPrivateKey(sealKeyID, sealPrivateKey); sealErr != nil {
				logger.WithError(sealErr).Fatal("Failed to configure media authority sealed-secret reader")
			}
		}
		foghornServer.SetMediaAuthorityStore(authorityStore)
		triggerProcessor.SetMediaAuthorityStore(authorityStore)
		control.SetLocalMediaAuthorityStore(authorityStore)
		go authorityStore.RunAuditRetention(context.Background(), logger)
		logger.WithFields(logging.Fields{"trusted_signers": len(trust), "control_cell_id": mediaAuthorityCellID}).Info("Signed media authority apply enabled")
	} else {
		logger.Warn("MEDIA_AUTHORITY_TRUST_SET is not configured; signed media authority apply is disabled")
	}
	var commodoreDependentWorkers sync.Once
	onCommodoreConnected := func(client *commodore.GRPCClient) {
		if client == nil {
			return
		}
		if authorityStore != nil {
			authorityStore.SetRefreshRequester(func(ctx context.Context) error {
				_, refreshErr := client.RequestMediaAuthorityReplay(ctx, mediaAuthorityCellID)
				if refreshErr != nil {
					logger.WithError(refreshErr).Warn("Soft-expired local media authority refresh request failed; valid authority remains active")
				}
				return refreshErr
			})
		}
		commodoreDependentWorkers.Do(func() {
			go pushstatusoutbox.NewWorker(db, client, logger).Run(context.Background())
			go managedplacementoutbox.NewWorker(db, client, logger).Run(context.Background())
			go signingkeyuseoutbox.NewWorker(db, client, logger).Run(context.Background())
		})
	}
	onCommodoreConnected(commodoreClient)
	if qmClient != nil {
		foghornServer.SetQuartermasterClient(qmClient)
	}

	// Storage resolver factory: builds a per-request storage.ClusterResolver
	// using the local Foghorn S3 backing tuple and the tenant's advertised
	// cluster_peers backings. Used by CreateVodUpload, freeze, and thumbnail
	// upload paths to pick local-mint vs federated-mint.
	foghornServer.SetStorageResolverFactory(func(ctx context.Context, tenantID string) *storage.ClusterResolver {
		return &storage.ClusterResolver{
			LocalClusterID:       foghornCfg.ClusterID,
			LocalClusterServed:   control.IsServedCluster,
			LocalS3Backing:       localS3Backing,
			LocalS3ClientPresent: s3ForGRPC != nil,
			AdvertisedBacking: func(clusterID string) (storage.S3Backing, bool) {
				b, ok := advertisedBackingForTenant(ctx, tenantID, clusterID)
				if !ok {
					return storage.S3Backing{}, false
				}
				return storage.S3Backing{Bucket: b.Bucket, Endpoint: b.Endpoint, Region: b.Region, Prefix: b.Prefix}, true
			},
			Logger:  logger,
			Metrics: serviceResolutionRejected,
		}
	})
	foghornServer.SetRedisStateStore(redisStore)

	// Wire DVR service to trigger processor for auto-start recordings on stream start
	triggerProcessor.SetDVRService(foghornServer)

	// Wire the trigger processor's cache + DVR machinery to the managed-stream
	// reconciler so mist_native Apply events populate the same caches and
	// start the same DVR side-effects PUSH_REWRITE drives for push streams.
	// Without this, STREAM_PROCESS would miss the per-stream process policy
	// for mist_native streams (which never fire PUSH_REWRITE).
	control.SetManagedStreamMaterializer(triggerProcessor.ManagedStreamMaterializer())

	// Wire cache invalidator for instant tenant reactivation (Purser → Commodore → Foghorn)
	foghornServer.SetCacheInvalidator(triggerProcessor)

	// Wire federation remote edge cache for cross-cluster viewer routing
	if remoteEdgeCache != nil {
		foghornServer.SetRemoteEdgeCache(remoteEdgeCache, foghornCfg.ClusterID, instanceID)
		handlers.SetRemoteEdgeCache(remoteEdgeCache)
		handlers.SetOriginPullInstanceID(instanceID)
	}
	if fedClient != nil {
		handlers.SetFederationClient(fedClient)
		foghornServer.SetFederationClient(fedClient)
	}
	if peerManager != nil {
		handlers.SetPeerManager(peerManager)
		foghornServer.SetPeerManager(peerManager)
	}

	// Wire the process-wide arrange-origin-pull deps so the trigger
	// processor can federate cross-cluster DVR origin-pulls without
	// constructing its own deps struct per call. Only set when all
	// three federation primitives are available — without them
	// federation.DefaultArrange returns ErrOriginPullDepsMissing and
	// callers fall back to their non-federated path.
	if remoteEdgeCache != nil && peerManager != nil && fedClient != nil {
		federation.SetDefaultArrangeDeps(&federation.ArrangeOriginPullDeps{
			Cache:        remoteEdgeCache,
			PeerResolver: peerManager,
			FedClient:    fedClient,
			InstanceID:   instanceID,
			Logger:       logger,
		})
	}

	// Wire the cross-cluster artifact resolver so RelayResolve can
	// federate vod+/processing+ reads to the origin cluster when the
	// artifact isn't local — served from peer S3 (synced) or a peer-relay
	// grant (hot-but-unsynced). Without these deps RelayResolve silently
	// 404s on non-local artifacts (today's behavior).
	if peerManager != nil && fedClient != nil {
		control.SetCrossClusterArtifactDeps(&control.CrossClusterArtifactDeps{
			FedClient:      fedClient,
			PeerResolver:   peerManager,
			LocalClusterID: foghornCfg.ClusterID,
		})
	}

	// Wire the storage resolver factory + federated mint delegate into the
	// control package so processFreezePermissionRequest and
	// processThumbnailUploadRequest pick local-mint vs federated-mint per
	// artifact, using the same rule the federation server uses for
	// PrepareArtifact redirect emission.
	control.SetStorageResolverFactory(func(ctx context.Context, tenantID string) *storage.ClusterResolver {
		return &storage.ClusterResolver{
			LocalClusterID:       foghornCfg.ClusterID,
			LocalClusterServed:   control.IsServedCluster,
			LocalS3Backing:       localS3Backing,
			LocalS3ClientPresent: s3ForGRPC != nil,
			AdvertisedBacking: func(clusterID string) (storage.S3Backing, bool) {
				b, ok := advertisedBackingForTenant(ctx, tenantID, clusterID)
				if !ok {
					return storage.S3Backing{}, false
				}
				return storage.S3Backing{Bucket: b.Bucket, Endpoint: b.Endpoint, Region: b.Region, Prefix: b.Prefix}, true
			},
			Logger:  logger,
			Metrics: serviceResolutionRejected,
		}
	})
	if fedClient != nil && peerManager != nil {
		control.SetStorageDeleteDelegate(func(ctx context.Context, targetClusterID string, req *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error) {
			addr := peerManager.GetPeerAddr(targetClusterID)
			if addr == "" {
				return &foghornfederationpb.DeleteStorageObjectsResponse{Accepted: false, Reason: "peer_unreachable"}, nil
			}
			return fedClient.DeleteStorageObjects(ctx, targetClusterID, addr, req)
		})
	}

	// Wire the shared artifact cleaner used by gRPC delete handlers and
	// the purge job. Uses the federation delete delegate when an
	// artifact's storage_cluster_id points to a peer cluster, the local
	// S3 client otherwise. Wired even when local S3 is absent
	// (storage-via-federation deployments) so remote rows still get
	// cleaned via the delegate.
	var deleteDelegate artifacts.DeleteDelegate
	if d := control.GetStorageDeleteDelegate(); d != nil {
		deleteDelegate = func(ctx context.Context, targetClusterID string, req *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error) {
			return d(ctx, targetClusterID, req)
		}
	}
	// One fully-populated cleaner, shared by the gRPC delete handlers, the purge job, and stream cleanup so every
	// path agrees on backend identity. LocalBackendID (this cell's immutable store fingerprint) is what cleanup and the
	// multipart-ownership fence compare an object's recorded backend_id against (backend_id itself is written at upload
	// creation, and adopted at boot for pre-cut in-flight rows) to fail closed on a foreign or unattributed row.
	var artifactCleaner *artifacts.Cleaner
	if s3ForFederation != nil || deleteDelegate != nil {
		var localS3 artifacts.S3Client
		if s3ForFederation != nil {
			localS3 = s3ForFederation
		}
		artifactCleaner = &artifacts.Cleaner{
			LocalCluster:   foghornCfg.ClusterID,
			S3:             localS3,
			Delegate:       deleteDelegate,
			LocalBackendID: localBackendFingerprint(localS3),
		}
		foghornServer.SetArtifactCleaner(artifactCleaner)
	}
	if federationServer != nil {
		federationServer.SetStorageMintMetric(metrics.StorageMint)
		federationServer.SetClipCreator(foghornServer)
		federationServer.SetDVRCreator(foghornServer)
		federationServer.SetArtifactCommandHandler(foghornServer)
	}

	relayAdvertiseAddr := foghornRelayAdvertiseAddr(internalGRPCBindAddr, advertiseAddr)
	if redisStore != nil && relayAdvertiseAddr != "" {
		relayPool := foghornpool.NewPool(foghornpool.PoolConfig{
			ServiceToken:  serviceToken,
			Timeout:       10 * time.Second,
			Logger:        logger,
			AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
			CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
			ServerName:    config.GetServiceGRPCTLSServerName("foghorn"),
		})
		defer relayPool.Close()

		control.InitRelay(redisStore, instanceID, relayAdvertiseAddr, &relayPoolAdapter{pool: relayPool}, logger)
		relayServer = foghorngrpc.NewRelayServer(logger)

		logger.WithFields(logging.Fields{
			"external_advertise_addr": advertiseAddr,
			"instance_id":             instanceID,
			"relay_advertise_addr":    relayAdvertiseAddr,
		}).Info("HA command relay enabled")
		relayReady = true
	} else if haRequired {
		logger.WithFields(logging.Fields{
			"redis_configured": redisStore != nil,
			"advertise_addr":   relayAdvertiseAddr,
		}).Fatal("FOGHORN_HA_REQUIRED is true but HA command relay could not be enabled")
	}

	// Load the initial central projections before the external TLS listener
	// snapshots the served set. Both calls are independently bounded and
	// preserve the local/last-good snapshot on failure, so a control-plane
	// outage cannot prevent local media startup while a healthy multi-cluster
	// assignment is available to SNI immediately rather than an hour later.
	var initialClusterLoad sync.WaitGroup
	initialClusterLoad.Add(2)
	go func() {
		defer initialClusterLoad.Done()
		control.LoadServedClusters()
	}()
	go func() {
		defer initialClusterLoad.Done()
		control.LoadPlatformSharedClusters()
	}()
	initialClusterLoad.Wait()

	internalRegistrars := []control.ServiceRegistrar{foghornServer.RegisterServices}
	if federationServer != nil {
		internalRegistrars = append(internalRegistrars, federationServer.RegisterServices)
	}
	if relayServer != nil {
		internalRegistrars = append(internalRegistrars, relayServer.RegisterServices)
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	grpcServers, err := control.StartGRPCServers(context.Background(), control.GRPCServerConfig{
		InternalBindAddr:   internalGRPCBindAddr,
		ExternalBindAddr:   externalGRPCBindAddr,
		Logger:             logger,
		ServiceToken:       serviceToken,
		JWTSecret:          jwtSecret,
		InternalRegistrars: internalRegistrars,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to start control gRPC server")
	}
	if commodoreClient != nil && authorityStore != nil {
		// Local persisted authority is immediately usable; replay only repairs
		// distribution drift and must not hold HTTP startup behind Commodore.
		go requestMediaAuthorityReplay(commodoreClient, mediaAuthorityCellID, logger)
	} else if commodoreReconnectNeeded {
		go reconnectCommodore(commodoreGRPCURL, serviceToken, mediaAuthorityCellID, logger, commodoreCache, clients, clientStatusGauge, clientReconnects, triggerProcessor, onCommodoreConnected)
	}

	// Start cert refresh loop (re-pushes ConfigSeed when Navigator renews wildcard certs)
	certRefreshCtx, certRefreshCancel := context.WithCancel(context.Background())
	defer certRefreshCancel()
	go control.StartCertRefreshLoop(certRefreshCtx, 1*time.Hour, logger)

	// Refresh served cluster assignments from Quartermaster every 5 minutes.
	clusterRefreshCtx, clusterRefreshCancel := context.WithCancel(context.Background())
	defer clusterRefreshCancel()
	go control.StartServedClustersRefresh(clusterRefreshCtx, 5*time.Minute, logger)

	// Reconcile managed (mist_native) streams onto connected Helmsmen every
	// 30 seconds. Each tick lists Commodore's desired state for every served
	// cluster, runs deterministic placement, and emits Apply / Retract
	// deltas against last_sent[node]. Reconnect handling: ForgetManagedStreamLastSent
	// is called from the connection cleanup so the next tick re-emits Apply
	// for whatever should be on the reconnected node.
	managedStreamCtx, managedStreamCancel := context.WithCancel(context.Background())
	defer managedStreamCancel()
	go control.StartManagedStreamReconciler(managedStreamCtx, 30*time.Second, logger)

	// Relay edge state to Quartermaster. Two paths:
	//   - delta coalescer: drains DNS-relevant deltas (health/caps/cluster/IP)
	//     within ~1s of the lifecycle event or stream disconnect.
	//   - 60s repair sync: re-publishes the full alive set so transient losses
	//     converge without an event.
	if qmClient != nil {
		startEdgeQuartermasterPublisher(qmClient, redisStore, instanceID, logger)
	}

	// Start the hourly storage snapshot scheduler
	go startStorageSnapshotScheduler(triggerProcessor, logger)

	// Start retention job (marks expired assets as deleted)
	retentionJob := jobs.NewRetentionJob(jobs.RetentionConfig{
		DB:            db,
		Logger:        logger,
		Interval:      1 * time.Hour,
		RetentionDays: 30, // Default 30 days
		DecklogClient: decklogClient,
	})
	retentionJob.Start()
	defer retentionJob.Stop()

	// DVR chapter sweeper. Rotates chapter boundaries on every recording
	// DVR: closes the current chapter when its end_ms has passed (the
	// closed row enters the finalization queue) and opens the next.
	chapterSweeper := jobs.NewChapterSweeper(jobs.ChapterSweeperConfig{
		DB:       db,
		Logger:   logger,
		Interval: 1 * time.Minute,
	})
	chapterSweeper.Start()
	defer chapterSweeper.Stop()

	// Dispatches the chapter finalization processing job to the recording
	// origin Helmsman once a chapter row reaches state='closed'. The
	// triggerProcessor doubles as the GatewayResolver and the
	// ProcessConfigCacher (same plumbing the normal VOD processing
	// dispatcher uses below) so chapter jobs carry a substituted
	// processes_json and the STREAM_PROCESS trigger that fires when
	// Mist boots processing+<hash> finds the cached config.
	chapterFinalizer := jobs.NewChapterFinalizationQueue(jobs.ChapterFinalizationQueueConfig{
		DB:              db,
		Logger:          logger,
		GatewayResolver: triggerProcessor,
		ConfigCacher:    triggerProcessor,
	})
	control.SetChapterClosedNotifier(jobs.NotifyChapterFinalizationQueued)
	chapterFinalizer.Start()
	defer chapterFinalizer.Stop()

	// Reclaims source DVR segments after their covering chapters are
	// frozen on S3. Deletes local TS files via Helmsman + the recovery
	// bridge S3 objects directly.
	if s3ForFederation != nil {
		chapterReclaimer := jobs.NewChapterReclaimSweep(jobs.ChapterReclaimSweepConfig{
			DB:       db,
			Logger:   logger,
			S3Delete: chapterReclaimS3Adapter{client: s3ForFederation},
		})
		chapterReclaimer.Start()
		defer chapterReclaimer.Stop()
	}

	// Start stale freeze cleanup job (resets stuck freezing artifacts). It durably ENQUEUES each abandoned
	// attempt's staging object into foghorn.staging_cleanup_queue (transactionally with the reset); the
	// StagingCleanupJob below is what deletes them from S3 with retries.
	staleFreezeJob := jobs.NewStaleFreezeCleanupJob(jobs.StaleFreezeCleanupConfig{
		DB:         db,
		Logger:     logger,
		Interval:   1 * time.Minute,
		StaleAfter: 30 * time.Minute,
	})
	staleFreezeJob.Start()
	defer staleFreezeJob.Stop()

	// Crash-recovery reconciler for the thumbnail publication state machine: re-drives attempts stuck in
	// 'publishing' (idempotent pointer CAS) and fails + sweeps attempts abandoned past their lease (their
	// staging/version objects go to the staging-cleanup queue below). DB-only.
	thumbnailRecoveryJob := jobs.NewThumbnailRecoveryJob(jobs.ThumbnailRecoveryConfig{
		DB:       db,
		Logger:   logger,
		Interval: 1 * time.Minute,
		// Re-drive completions whose ThumbnailUploaded confirmation was lost (verify -> promote -> publish
		// against the staged objects), so a one-shot VOD thumbnail isn't orphaned when a send/crash loses it.
		Complete: func(ctx context.Context, attemptID string) (bool, error) {
			return control.CompleteThumbnailAttemptForRecovery(ctx, attemptID, logger)
		},
		// Re-drive published-but-unprojected attempts (a crash between the publish CAS and the deterministic
		// copy): re-project the winning version objects to the served key and, on success, expose has_thumbnails.
		Reproject: func(ctx context.Context, attemptID string) (bool, error) {
			return control.ReprojectPublishedThumbnailAttempt(ctx, attemptID)
		},
		// Bounded eventual convergence: past the max-copy window, re-copy the current winner to the deterministic
		// key once to correct any straggler overwrite from an earlier loser, then clear the reassert clock.
		Reassert: func(ctx context.Context, attemptID string) (bool, error) {
			return control.ReassertThumbnailProjection(ctx, attemptID)
		},
	})
	thumbnailRecoveryJob.Start()
	defer thumbnailRecoveryJob.Stop()

	// Start the staging cleanup worker: drains foghorn.staging_cleanup_queue (superseded/abandoned freeze
	// staging objects, enqueued transactionally at completion/recovery), deleting each from S3 with a capped
	// backoff and removing the row on success. This is the ONLY collector for freeze staging objects; nil S3
	// makes it a no-op drain.
	if s3ForReconciler != nil {
		stagingCleanupJob := jobs.NewStagingCleanupJob(jobs.StagingCleanupConfig{
			DB:             db,
			S3:             s3ForReconciler,
			Logger:         logger,
			LocalBackendID: localBackendFingerprint(s3ForFederation),
		})
		stagingCleanupJob.Start()
		defer stagingCleanupJob.Stop()
	}

	// Start creation-command expiry job (terminalizes artifact-creation ledger rows
	// stranded 'accepted' after a handler crashed between the accept and its terminal
	// write — see GetArtifactCreationStatus). CAS-rejects only rows past the hard
	// deadline with no artifact, so a still-running or committed create is never
	// touched. This is the sole writer that terminalizes strands; the status read path
	// only reads.
	creationCommandExpiryJob := jobs.NewCreationCommandExpiryJob(jobs.CreationCommandExpiryConfig{
		DB:       db,
		Logger:   logger,
		Interval: 1 * time.Minute,
		Deadline: 15 * time.Minute,
	})
	creationCommandExpiryJob.Start()
	defer creationCommandExpiryJob.Stop()

	// Start completing-VOD recovery job (converges uploads stranded in 'completing' after an ambiguous
	// S3 completion error — see CompleteVodUpload). Needs S3 to probe object existence; skipped when
	// no S3 is configured. Uses s3ForFederation (a *storage.S3Client with Exists + BuildS3URL).
	if s3ForFederation != nil {
		completingVodRecoveryJob := jobs.NewCompletingVodRecoveryJob(jobs.CompletingVodRecoveryConfig{
			DB:             db,
			S3:             s3ForFederation,
			Logger:         logger,
			Interval:       2 * time.Minute,
			StaleAfter:     5 * time.Minute,
			FailAfter:      1 * time.Hour,
			LocalBackendID: localBackendFingerprint(s3ForFederation),
		})
		completingVodRecoveryJob.Start()
		defer completingVodRecoveryJob.Stop()

		// Start aborting-VOD recovery job (converges aborts stranded in 'aborting' after an interrupted
		// AbortVodUpload — see AbortVodUpload). Needs S3 to re-run the multipart abort idempotently;
		// skipped when no S3 is configured. Uses the same s3ForFederation *storage.S3Client.
		abortingVodRecoveryJob := jobs.NewAbortingVodRecoveryJob(jobs.AbortingVodRecoveryConfig{
			DB:             db,
			S3:             s3ForFederation,
			Logger:         logger,
			Interval:       2 * time.Minute,
			StaleAfter:     5 * time.Minute,
			LocalBackendID: localBackendFingerprint(s3ForFederation),
		})
		abortingVodRecoveryJob.Start()
		defer abortingVodRecoveryJob.Stop()
	}

	// Start DVR starting-recovery job (converges recordings stranded in 'starting' after a lost node
	// ack — see StartDVR). Reads the persisted dvr_start_dispatch descriptor to idempotently re-dispatch
	// the start (or drain a compensating stop / finalize failed) with no external routing dependency.
	dvrStartingRecoveryJob := jobs.NewDVRStartingRecoveryJob(jobs.DVRStartingRecoveryConfig{
		DB:         db,
		Logger:     logger,
		Interval:   1 * time.Minute,
		StaleAfter: 2 * time.Minute,
		FailAfter:  15 * time.Minute,
	})
	dvrStartingRecoveryJob.Start()
	defer dvrStartingRecoveryJob.Stop()

	// Replay the durable DVR start intent for record:true sessions whose async StartDVR was
	// lost to a crash (the intent is persisted on the ingest_sessions row before the push
	// is approved; this job creates the recording if no artifact exists yet).
	dvrIntentRecoveryJob := jobs.NewDVRIntentRecoveryJob(jobs.DVRIntentRecoveryConfig{
		Logger:  logger,
		Starter: foghornServer,
	})
	dvrIntentRecoveryJob.Start()
	defer dvrIntentRecoveryJob.Stop()

	// Retire ingest sessions whose node hard-disconnected (crash / SIGKILL): it sends neither
	// PUSH_INPUT_CLOSE nor STREAM_END, so the session would otherwise stay open forever and block a
	// cross-node republish of the same stream (the (tenant, stream) partial unique rejects it as a
	// duplicate). Fenced against the node's conn_owner, so a reconnect's newer session is never killed.
	ingestSessionReaperJob := jobs.NewIngestSessionReaperJob(jobs.IngestSessionReaperConfig{
		Logger: logger,
	})
	ingestSessionReaperJob.Start()
	defer ingestSessionReaperJob.Stop()

	// Apply durable source-offline transitions. The worker holds the stream advisory lock across the
	// authority recheck and idempotent effects, so reconnect admission cannot interleave with teardown.
	ingestOfflineEffectsJob := jobs.NewIngestOfflineEffectsJob(jobs.IngestOfflineEffectsConfig{
		Logger:         logger,
		Apply:          triggerProcessor.ApplyOfflineEffect,
		LeaderInstance: triggerProcessor.FederationLeaderInstance,
	})
	ingestOfflineEffectsJob.Start()
	defer ingestOfflineEffectsJob.Stop()

	// Apply durable admission effects (push-target activation, prior-owner drain, federation live
	// broadcast) — the once-only obligations persisted with each projection confirmation. Unlike
	// the offline worker, this worker splits lock-only DB phases around an unlocked I/O phase;
	// acknowledgements can land during dispatch. A crash on any replica is replayed by its siblings.
	ingestAdmissionEffectsJob := jobs.NewIngestAdmissionEffectsJob(jobs.IngestAdmissionEffectsConfig{
		Logger: logger,
		Apply:  triggerProcessor.ApplyAdmissionEffect,
	})
	ingestAdmissionEffectsJob.Start()
	defer ingestAdmissionEffectsJob.Stop()

	// Keep Commodore's record of where each stream is being published current
	// for ordinary push ingest. PUSH_REWRITE claims that placement once, under
	// a short lease; without this the claim lapses under a still-connected
	// publisher and endpoint resolution stops routing their reconnects here.
	// The cadence is derived from that lease inside the job, so it is not
	// restated (and cannot drift) here.
	placementCfg := jobs.ActiveIngestPlacementConfig{
		Logger: logger,
		Sources: func(ctx context.Context) ([]jobs.LiveIngest, error) {
			live, err := control.LocallyPublishedStreams(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]jobs.LiveIngest, 0, len(live))
			for _, s := range live {
				out = append(out, jobs.LiveIngest{
					TenantID:     s.TenantID,
					InternalName: s.InternalName,
					ClusterID:    s.ClusterID,
					ClaimToken:   s.ClaimToken,
				})
			}
			return out, nil
		},
		ClaimLost: func(ctx context.Context, refused *commodorepb.ActiveIngestStream) error {
			nodeID, retired, err := control.RetireIngestSessionByClaim(ctx, refused.GetTenantId(), refused.GetInternalName(), refused.GetClaimToken(), logger)
			if err != nil || !retired {
				return err
			}
			return control.SendDrainStream(ctx, nodeID, &ipcpb.DrainStreamRequest{
				RuntimeName: control.RuntimeNameForStream(control.StreamRegistryInstance, refused.GetInternalName()),
				Reason:      "placement_claim_lost",
			})
		},
	}
	// Assigned only when present: a nil *GRPCClient in the interface field
	// would read as non-nil and the job would start and then dereference it.
	if commodoreClient != nil {
		placementCfg.Syncer = commodoreClient
	}
	activeIngestPlacementJob := jobs.NewActiveIngestPlacementJob(placementCfg)
	activeIngestPlacementJob.Start()
	defer activeIngestPlacementJob.Stop()

	// Start orphan reconciliation job (retries failed deletions)
	orphanCleanupJob := jobs.NewOrphanCleanupJob(jobs.OrphanCleanupConfig{
		DB:       db,
		Logger:   logger,
		Interval: 5 * time.Minute,
		MaxAge:   30 * time.Minute,
	})
	orphanCleanupJob.Start()
	defer orphanCleanupJob.Stop()

	// Start purge job (hard-deletes old soft-deleted records, frees S3
	// bytes via the shared cleaner so cross-cluster deletes route
	// through the federation delegate). Reuses the SAME fully-populated
	// cleaner wired into the gRPC delete handlers above, so both paths
	// resolve the recorded backend identically (nil when no S3/delegate).
	purgeCleaner := artifactCleaner
	purgeDeletedJob := jobs.NewPurgeDeletedJob(jobs.PurgeDeletedConfig{
		DB:                      db,
		Logger:                  logger,
		Interval:                24 * time.Hour,
		RetentionAge:            30 * 24 * time.Hour, // 30 days
		Cleaner:                 purgeCleaner,
		AllowCrossClusterDelete: federationServer != nil,
	})
	purgeDeletedJob.Start()
	defer purgeDeletedJob.Stop()

	// Drains foghorn.stream_cleanup_obligation: sweeps a deleted LIVE stream's thumbnail bytes (no artifact row, so
	// purge/GC never reach it) and drops the control rows, retried from the durable tombstone until confirmed gone.
	// Reuses the purge cleaner so cross-cluster bytes route through the same federation delegate.
	streamCleanupJob := jobs.NewStreamCleanupJob(jobs.StreamCleanupConfig{
		DB:       db,
		Cleaner:  purgeCleaner,
		Logger:   logger,
		Interval: 1 * time.Minute,
	})
	streamCleanupJob.Start()
	defer streamCleanupJob.Stop()

	// Start artifact reconciler (retries failed syncs, advances pending, onboards orphaned)
	artifactReconciler := jobs.NewArtifactReconciler(jobs.ArtifactReconcilerConfig{
		DB:              db,
		S3Client:        s3ForReconciler,
		CommodoreClient: commodoreClient,
		SendFreeze:      control.SendFreezeRequest,
		Logger:          logger,
		Interval:        5 * time.Minute,
		ClusterID:       foghornCfg.ClusterID,
		OnNodeIndexed: func(ctx context.Context, artifactHash, nodeID string) {
			if err := control.RefreshNodeCopy(ctx, artifactHash, nodeID); err != nil {
				logger.WithError(err).WithField("artifact_hash", artifactHash).Warn("Failed to emit node-copy GAINED after reconciler index")
			}
		},
	})
	artifactReconciler.Start()
	defer artifactReconciler.Stop()
	control.SetOnArtifactMapUpdated(func(nodeID string) {
		logger.WithField("node_id", nodeID).Debug("Triggering immediate artifact reconciliation after artifact map update")
		artifactReconciler.Trigger()
	})
	control.SetOnCatalogDirty(func() {
		logger.Debug("Triggering immediate catalog projection after committed lifecycle mutation")
		artifactReconciler.Trigger()
	})

	// Node-copy telemetry reconciliation: emit GAINED for present copies that have never been
	// emitted (last_emitted_version=0). This is emission CORRECTNESS — rows on a fresh projection and
	// rows created by non-emitting writers (DVR-start / reconciler / segment inserts) — NOT loss
	// recovery: the durable, authoritative record of node copies is foghorn.artifact_nodes
	// itself (self-healing from ~10s node reports), and the ClickHouse projection is analytics-
	// only. Each row is emitted once (its last_emitted_version then becomes >0) under FOR UPDATE,
	// so this is idempotent, mints no fake history for stable rows, can't resurrect an evicted
	// copy, and is safe on every replica. Runs immediately on boot, then on a slow timer.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			if n, err := control.ReconcileNodeCopies(context.Background()); err != nil {
				logger.WithError(err).Warn("Node-copy reconciliation pass failed")
			} else if n > 0 {
				logger.WithField("emitted", n).Debug("Node-copy reconciliation seeded copies")
			}
			<-ticker.C
		}
	}()

	// Start processing job dispatcher (routes VOD processing jobs to edge nodes)
	processingDispatcher := jobs.NewProcessingDispatcher(jobs.ProcessingDispatcherConfig{
		DB:     db,
		Logger: logger,
	})
	processingDispatcher.SetProcessConfigCacher(triggerProcessor)
	processingDispatcher.SetGatewayResolver(triggerProcessor)

	// Initialize VOD processing pipeline. Completed/failed results are handled atomically in
	// control.processProcessingJobResult — there is no duplicate result callback.
	foghorngrpc.InitVodPipeline(db, logger, decklogClient)
	control.SetProcessConfigCacheUpdater(triggerProcessor.CacheProcessConfig)
	// Exhaustion (max retries) marks the artifact failed + emits lifecycle directly in the
	// dispatcher's recoverStale (failVODArtifact / failClipArtifact) — no separate callback that
	// would double-write.
	processingDispatcher.Start()
	defer processingDispatcher.Stop()

	publicRouter, internalRouter := configureFoghornHTTPRouters(logger, healthChecker, metricsCollector)

	publicConfig := server.DefaultConfig("foghorn", "18008")
	publicConfig.BindAddr = strings.TrimSpace(os.Getenv("FOGHORN_PUBLIC_HTTP_BIND_ADDR"))
	internalHTTPPort := strconv.Itoa(servicedefs.FoghornInternalHTTPPort)
	internalConfig := server.DefaultConfig("foghorn-internal", internalHTTPPort)
	internalConfig.Port = config.GetEnv("FOGHORN_INTERNAL_HTTP_PORT", internalHTTPPort)
	internalConfig.BindAddr = config.GetEnv("FOGHORN_INTERNAL_HTTP_BIND_ADDR", "127.0.0.1")

	refreshDone := make(chan struct{})
	defer close(refreshDone)
	go func() {
		ticker := time.NewTicker(control.BalancerCapabilityRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if refreshed := control.RefreshLocalBalancerCapabilities(); refreshed > 0 {
					logger.WithField("nodes", refreshed).Debug("Refreshed Mist balancer capabilities")
				}
			case <-refreshDone:
				return
			}
		}
	}()

	server.RegisterEnvFileReload("foghorn", logger)
	if err := server.StartAll([]server.Listener{
		{Config: publicConfig, Router: publicRouter},
		{Config: internalConfig, Router: internalRouter},
	}, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	control.CleanupLocalConnOwners(shutdownCtx)
	if err := triggerProcessor.Shutdown(shutdownCtx); err != nil {
		logger.WithError(err).Warn("Trigger processor shutdown did not finish cleanly; some client lifecycle batches may have been lost")
	}

	done := make(chan struct{})
	go func() {
		grpcServers.Internal.GracefulStop()
		grpcServers.External.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		grpcServers.Internal.Stop()
		grpcServers.External.Stop()
	}
	stopConfigSeedAckOutbox()
}

func foghornRelayAdvertiseAddr(internalBindAddr, fallbackAddr string) string {
	if addr := strings.TrimSpace(os.Getenv("FOGHORN_RELAY_ADVERTISE_ADDR")); addr != "" {
		return addr
	}
	relayHost := strings.TrimSpace(os.Getenv("FOGHORN_RELAY_ADVERTISE_HOST"))
	if relayHost == "" {
		relayHost = strings.TrimSpace(os.Getenv("FOGHORN_HOST"))
	}
	if relayHost == "" {
		host, _, err := net.SplitHostPort(strings.TrimSpace(fallbackAddr))
		if err == nil {
			relayHost = host
		}
	}
	if relayHost == "" {
		if config.IsProduction() {
			return ""
		}
		relayHost = "127.0.0.1"
	}
	relayPort := controlPortFromBindAddr(internalBindAddr, 18019)
	return net.JoinHostPort(relayHost, strconv.Itoa(relayPort))
}

func controlPortFromBindAddr(bindAddr string, fallback int) int {
	host, port, err := net.SplitHostPort(strings.TrimSpace(bindAddr))
	if err != nil {
		if trimmed, ok := strings.CutPrefix(bindAddr, ":"); ok {
			port = trimmed
		} else {
			return fallback
		}
	}
	_ = host
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func reconnectQuartermaster(
	grpcAddr string,
	serviceToken string,
	logger logging.Logger,
	clients *clientState,
	statusGauge *prometheus.GaugeVec,
	reconnects *prometheus.CounterVec,
	triggerProcessor *triggers.Processor,
) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:      grpcAddr,
			Timeout:       30 * time.Second,
			Logger:        logger,
			ServiceToken:  serviceToken,
			AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
			CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
			ServerName:    config.GetServiceGRPCTLSServerName("quartermaster"),
		})
		if err != nil {
			clients.setQuartermaster(false, err)
			statusGauge.WithLabelValues("quartermaster").Set(0)
			reconnects.WithLabelValues("quartermaster", "failure").Inc()
			logger.WithError(err).Warn("Quartermaster reconnect failed")
			continue
		}
		clients.setQuartermaster(true, nil)
		statusGauge.WithLabelValues("quartermaster").Set(1)
		reconnects.WithLabelValues("quartermaster", "success").Inc()
		control.SetQuartermasterClient(client)
		handlers.SetQuartermasterClient(client)
		startReleaseReconciler(client, logger)
		if triggerProcessor != nil {
			triggerProcessor.SetQuartermasterClient(client)
		}
		logger.Info("Quartermaster reconnected")
		return
	}
}

func startReleaseReconciler(qmClient *qmclient.GRPCClient, logger logging.Logger) {
	if qmClient == nil {
		return
	}
	releaseReconcilerClient.Store(qmClient)
	releaseReconcilerOnce.Do(func() {
		interval := time.Duration(config.GetEnvInt("EDGE_RELEASE_RECONCILE_INTERVAL_SECONDS", 60)) * time.Second
		orchestrator.StartReleaseReconciler(context.Background(), releaseReconcilerClient.Load, interval, logger)
	})
}

func reconnectCommodore(
	grpcAddr string,
	serviceToken string,
	controlCellID string,
	logger logging.Logger,
	commodoreCache *cache.Cache,
	clients *clientState,
	statusGauge *prometheus.GaugeVec,
	reconnects *prometheus.CounterVec,
	triggerProcessor *triggers.Processor,
	onConnected func(*commodore.GRPCClient),
) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		client, err := commodore.NewGRPCClient(commodore.GRPCConfig{
			GRPCAddr:      grpcAddr,
			Timeout:       30 * time.Second,
			Logger:        logger,
			Cache:         commodoreCache,
			ServiceToken:  serviceToken,
			AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
			CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
			ServerName:    config.GetServiceGRPCTLSServerName("commodore"),
		})
		if err != nil {
			clients.setCommodore(false, err)
			statusGauge.WithLabelValues("commodore").Set(0)
			reconnects.WithLabelValues("commodore", "failure").Inc()
			logger.WithError(err).Warn("Commodore reconnect failed")
			continue
		}
		clients.setCommodore(true, nil)
		statusGauge.WithLabelValues("commodore").Set(1)
		reconnects.WithLabelValues("commodore", "success").Inc()
		handlers.SetCommodoreClient(client)
		control.SetCommodoreClient(client)
		if triggerProcessor != nil {
			triggerProcessor.SetCommodoreClient(client)
		}
		if control.StreamRegistryInstance != nil {
			control.StreamRegistryInstance.SetCommodoreClient(client)
		}
		if onConnected != nil {
			onConnected(client)
		}
		requestMediaAuthorityReplay(client, controlCellID, logger)
		logger.Info("Commodore reconnected")
		return
	}
}

func requestMediaAuthorityReplay(client *commodore.GRPCClient, controlCellID string, logger logging.Logger) {
	if client == nil || strings.TrimSpace(controlCellID) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.RequestMediaAuthorityReplay(ctx, controlCellID)
	if err != nil {
		logger.WithError(err).WithField("control_cell_id", controlCellID).Warn("Failed to request media authority replay")
		return
	}
	logger.WithField("control_cell_id", controlCellID).WithField("requeued_count", resp.GetRequeuedCount()).Info("Requested current media authority replay")
}

const (
	edgeQMPublisherLeaseRole = "quartermaster_reporter"
	edgeQMPublisherLeaseTTL  = 15 * time.Second
)

func startEdgeQuartermasterPublisher(qm *qmclient.GRPCClient, store *state.RedisStateStore, instanceID string, log logging.Logger) {
	if store == nil {
		go startEdgeDNSDeltaCoalescer(qm, log)
		go startEdgeHealthSync(qm, log)
		return
	}
	go runEdgeQuartermasterPublisher(qm, store, instanceID, log)
}

func runEdgeQuartermasterPublisher(qm *qmclient.GRPCClient, store *state.RedisStateStore, instanceID string, log logging.Logger) {
	deltaTicker := time.NewTicker(1 * time.Second)
	defer deltaTicker.Stop()
	healthTicker := time.NewTicker(60 * time.Second)
	defer healthTicker.Stop()

	leaseHeld := false
	var lastLeaseErrLog time.Time
	ensureLease := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		ok, err := store.TryAcquireLease(ctx, edgeQMPublisherLeaseRole, instanceID, edgeQMPublisherLeaseTTL)
		cancel()
		if err != nil {
			now := time.Now()
			if lastLeaseErrLog.IsZero() || now.Sub(lastLeaseErrLog) >= 30*time.Second {
				log.WithError(err).Warn("edge Quartermaster publisher lease check failed")
				lastLeaseErrLog = now
			}
			return false
		}
		if ok && !leaseHeld {
			log.WithField("instance_id", instanceID).Info("Acquired edge Quartermaster publisher lease")
		}
		if !ok && leaseHeld {
			log.WithField("instance_id", instanceID).Warn("Lost edge Quartermaster publisher lease")
		}
		leaseHeld = ok
		return ok
	}

	for {
		select {
		case <-deltaTicker.C:
			if ensureLease() {
				publishEdgeDNSDeltas(qm, log)
			}
		case <-healthTicker.C:
			if ensureLease() {
				publishEdgeHealthSnapshot(qm, log)
			}
		}
	}
}

// startEdgeHealthSync re-publishes every known node — healthy AND unhealthy —
// to Quartermaster every 60s as a repair signal. The delta coalescer carries
// the fast path; this loop catches anything the coalescer missed (lost gRPC
// call, restart) including unhealthy tombstones that AliveNodeIDs would
// filter out.
func startEdgeHealthSync(qm *qmclient.GRPCClient, log logging.Logger) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		publishEdgeHealthSnapshot(qm, log)
	}
}

func publishEdgeHealthSnapshot(qm *qmclient.GRPCClient, log logging.Logger) {
	snaps := state.DefaultManager().AllReportedNodes(15 * time.Minute)
	if len(snaps) == 0 {
		return
	}
	nodes := make([]*quartermasterpb.NodeAliveness, 0, len(snaps))
	for _, s := range snaps {
		nodes = append(nodes, snapshotToProto(s))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := qm.ReportAliveNodes(ctx, nodes)
	cancel()
	if err != nil {
		log.WithError(err).WithField("count", len(nodes)).Warn("edge health sync failed")
	}
}

// startEdgeDNSDeltaCoalescer drains DNS-relevant deltas on a ~1s window and
// pushes them to Quartermaster. Coalescing protects against flap (caps
// flickering, stream rapid-reconnect) without sacrificing latency.
func startEdgeDNSDeltaCoalescer(qm *qmclient.GRPCClient, log logging.Logger) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		publishEdgeDNSDeltas(qm, log)
	}
}

func publishEdgeDNSDeltas(qm *qmclient.GRPCClient, log logging.Logger) {
	deltas := state.DefaultManager().ConsumeDNSRelevantDeltas()
	if len(deltas) == 0 {
		return
	}
	nodes := make([]*quartermasterpb.NodeAliveness, 0, len(deltas))
	for _, d := range deltas {
		nodes = append(nodes, snapshotToProto(d))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := qm.ReportAliveNodes(ctx, nodes)
	cancel()
	if err != nil {
		log.WithError(err).WithField("count", len(nodes)).Warn("edge DNS delta push failed")
	}
}

func snapshotToProto(s state.NodeDNSSnapshot) *quartermasterpb.NodeAliveness {
	return &quartermasterpb.NodeAliveness{
		NodeId:    s.NodeID,
		IsHealthy: s.IsHealthy,
		ClusterId: s.ClusterID,
		// ExternalIp is sent only when Foghorn could parse an IP literal out
		// of base_url; QM rejects malformed values rather than coerce.
		ExternalIp: s.ExternalIP,
		Capabilities: &quartermasterpb.EdgeCapabilities{
			Ingest:     s.CapIngest,
			Egress:     s.CapEdge,
			Storage:    s.CapStorage,
			Processing: s.CapProcessing,
		},
	}
}

func relayHealthResult(relayReady, haRequired bool) monitoring.CheckResult {
	if relayReady {
		return monitoring.CheckResult{Status: monitoring.StatusHealthy}
	}
	if haRequired {
		return monitoring.CheckResult{
			Status:  monitoring.StatusUnhealthy,
			Message: "HA relay required but not ready",
		}
	}
	return monitoring.CheckResult{
		Status:  monitoring.StatusDegraded,
		Message: "HA relay not ready",
	}
}

func startStorageSnapshotScheduler(p *triggers.Processor, logger logging.Logger) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		if err := p.GenerateAndSendStorageSnapshots(); err != nil {
			logger.WithError(err).Error("Failed to generate and enqueue storage snapshots")
		}
	}
}

func configureFoghornHTTPRouters(logger logging.Logger, healthChecker *monitoring.HealthChecker, metricsCollector *monitoring.MetricsCollector) (*gin.Engine, *gin.Engine) {
	publicRouter := server.SetupServiceRouter(logger, "foghorn", healthChecker, metricsCollector)
	internalRouter := server.SetupServiceRouter(logger, "foghorn-internal", healthChecker, metricsCollector)

	internalRead := internalRouter.Group("", handlers.RequireInternalRead())
	internalRead.GET("/nodes/overview", handlers.HandleNodesOverview)
	internalRead.GET("/nodes/:node_id/drain-status", handlers.HandleGetNodeDrainStatus)
	internalRead.GET("/dashboard", handlers.HandleRootPage)
	internalRead.GET("/debug/cache/stream-context", handlers.HandleStreamContextCache)
	internalRead.GET("/debug/stream-registry", handlers.HandleStreamRegistry)
	internalRead.GET("/debug/served-clusters", handlers.HandleServedClusters)
	internalRouter.PUT("/nodes/:node_id/mode", handlers.RequireInternalMutation(), handlers.HandleSetNodeMaintenanceMode)

	publicRouter.GET("/play/*path", handlers.HandleGenericViewerPlayback)
	publicRouter.GET("/resolve/*path", handlers.HandleGenericViewerPlayback)
	publicRouter.GET("/ingest/:streamKey", handlers.HandleIngestFrontDoor)
	publicRouter.POST("/ingest/:streamKey", handlers.HandleIngestFrontDoor)
	publicRouter.GET("/ingest/", handlers.HandleIngestFrontDoor)
	publicRouter.POST("/ingest/", handlers.HandleIngestFrontDoor)
	publicRouter.POST("/webhooks/livepeer/auth", handlers.HandleLivepeerAuth)

	publicRouter.NoRoute(handlers.AuthorizedMistServerCompatibilityHandler)
	internalRouter.NoRoute(handlers.RequireInternalCompatibility(), handlers.MistServerCompatibilityHandler)
	return publicRouter, internalRouter
}

// relayPoolAdapter wraps FoghornPool to satisfy control.CommandRelayPool.
type relayPoolAdapter struct {
	pool *foghornpool.FoghornPool
}

func (a *relayPoolAdapter) GetOrCreate(key, addr string) (control.CommandRelayClient, error) {
	client, err := a.pool.GetOrCreate(key, addr)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// chapterReclaimS3Adapter narrows *storage.S3Client to the small
// Delete-by-key surface the reclaim sweep needs. Lets the sweep stay
// in the jobs package without importing storage.
type chapterReclaimS3Adapter struct {
	client *storage.S3Client
}

func (a chapterReclaimS3Adapter) Delete(ctx context.Context, key string) error {
	if a.client == nil || key == "" {
		return nil
	}
	return a.client.Delete(ctx, key)
}
