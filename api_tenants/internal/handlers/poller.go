package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

var pollerInFlight int32

// HealthPollerConfig configures StartHealthPoller. Every value except TLS is
// read once when the poller starts.
type HealthPollerConfig struct {
	// PollIntervalSeconds must be positive.
	PollIntervalSeconds int
	TimeoutMS           int
	// MaxConcurrency and BatchSize fall back to 8 and 200 when not positive.
	MaxConcurrency int
	BatchSize      int
	// MinAgeSeconds is how old a health result must be before the instance is
	// polled again. Negative uses the poll interval.
	MinAgeSeconds int

	GRPCWatch bool
	// WatchRefreshSeconds must be positive when GRPCWatch is set.
	WatchRefreshSeconds int
	WatchBackoffSeconds int
	WatchDialTimeoutMS  int
	// WatchMaxConcurrency falls back to MaxConcurrency when not positive.
	WatchMaxConcurrency int

	// TLS returns the TLS settings for a gRPC health probe or watch of the
	// given service type. It is called for every dial, so a configuration
	// reload applies to dials made afterwards. Nil dials with no TLS overrides.
	TLS func(serviceID string) HealthWatchTLS
}

// HealthWatchTLS is the TLS material for one gRPC health probe or watch dial.
type HealthWatchTLS struct {
	CAPath string
	// ServerName overrides the certificate authority name. Empty falls back to
	// <serviceID>.internal when CAPath is set.
	ServerName    string
	AllowInsecure bool
}

// StartHealthPoller launches a background goroutine that polls HTTP/gRPC health endpoints
// for all registered service instances and updates their health status in the database.
func StartHealthPoller(cfg HealthPollerConfig) {
	interval := time.Duration(cfg.PollIntervalSeconds) * time.Second
	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	maxConc := cfg.MaxConcurrency
	if maxConc <= 0 {
		maxConc = 8
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 200
	}
	minAgeSeconds := cfg.MinAgeSeconds
	if minAgeSeconds < 0 {
		minAgeSeconds = int(interval.Seconds())
	}

	client := &http.Client{Timeout: timeout}
	sem := make(chan struct{}, maxConc)
	minAge := time.Duration(minAgeSeconds) * time.Second

	tls := cfg.TLS
	if tls == nil {
		tls = func(string) HealthWatchTLS { return HealthWatchTLS{} }
	}

	if cfg.GRPCWatch {
		watchRefresh := time.Duration(cfg.WatchRefreshSeconds) * time.Second
		watchBackoff := time.Duration(cfg.WatchBackoffSeconds) * time.Second
		watchDialTimeout := time.Duration(cfg.WatchDialTimeoutMS) * time.Millisecond
		watchMaxConc := cfg.WatchMaxConcurrency
		if watchMaxConc <= 0 {
			watchMaxConc = maxConc
		}
		watchSem := make(chan struct{}, watchMaxConc)
		go startGrpcHealthWatchers(watchRefresh, watchDialTimeout, watchBackoff, watchSem, tls)
	}

	go func() {
		// Add ±25% jitter to prevent thundering herd on restart
		jitterRange := int64(interval / 4)
		for {
			if !atomic.CompareAndSwapInt32(&pollerInFlight, 0, 1) {
				time.Sleep(interval)
				continue
			}
			if err := pollOnce(client, sem, batchSize, minAge, tls); err != nil {
				logger.WithError(err).Warn("health poller iteration failed")
			}
			atomic.StoreInt32(&pollerInFlight, 0)
			// Sleep with jitter: interval ± 25%
			jitter := time.Duration(rand.Int63n(jitterRange*2) - jitterRange)
			time.Sleep(interval + jitter)
		}
	}()
}

type serviceInstance struct {
	id, serviceID, proto, defaultProto, host, path string
	assignedClusterID, assignedBaseURL             string
	port                                           int
}

type serviceHealthCounts struct {
	Checked   int `json:"checked"`
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
	Skipped   int `json:"skipped"`
}

type serviceHealthSummary struct {
	mu       sync.Mutex
	services map[string]*serviceHealthCounts
}

func newServiceHealthSummary() *serviceHealthSummary {
	return &serviceHealthSummary{
		services: map[string]*serviceHealthCounts{},
	}
}

func (s *serviceHealthSummary) recordResult(serviceID, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	counts := s.countsFor(serviceID)
	counts.Checked++
	switch status {
	case "healthy":
		counts.Healthy++
	default:
		counts.Unhealthy++
	}
}

func (s *serviceHealthSummary) recordSkipped(serviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.countsFor(serviceID).Skipped++
}

func (s *serviceHealthSummary) countsFor(serviceID string) *serviceHealthCounts {
	serviceID = strings.TrimSpace(serviceID)
	if serviceID == "" {
		serviceID = "unknown"
	}
	counts := s.services[serviceID]
	if counts == nil {
		counts = &serviceHealthCounts{}
		s.services[serviceID] = counts
	}
	return counts
}

func (s *serviceHealthSummary) snapshot() (map[string]serviceHealthCounts, []string, []string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	byService := make(map[string]serviceHealthCounts, len(s.services))
	var healthyServices, unhealthyServices, skippedServices []string
	for serviceID, counts := range s.services {
		copied := *counts
		byService[serviceID] = copied
		if counts.Healthy > 0 {
			healthyServices = append(healthyServices, serviceID)
		}
		if counts.Unhealthy > 0 {
			unhealthyServices = append(unhealthyServices, serviceID)
		}
		if counts.Skipped > 0 {
			skippedServices = append(skippedServices, serviceID)
		}
	}
	sort.Strings(healthyServices)
	sort.Strings(unhealthyServices)
	sort.Strings(skippedServices)
	return byService, healthyServices, unhealthyServices, skippedServices
}

func pollOnce(client *http.Client, sem chan struct{}, batchSize int, minAge time.Duration, tls func(serviceID string) HealthWatchTLS) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cutoff := time.Now().Add(-minAge)
	var list []serviceInstance
	if err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		rows, err := quartermasterdb.New(db).ListHealthPollCandidates(ctx, quartermasterdb.ListHealthPollCandidatesParams{
			Cutoff: cutoff, BatchSize: int32(batchSize),
		})
		if err != nil {
			return err
		}

		nextList := make([]serviceInstance, 0, len(rows))
		for _, row := range rows {
			port := 0
			if row.Port.Valid {
				port = int(row.Port.Int32)
			}
			nextList = append(nextList, serviceInstance{
				id: row.InstanceID, serviceID: row.ServiceID, proto: row.Protocol,
				host: row.AdvertiseHost, port: port, path: row.Path, defaultProto: row.DefaultProtocol,
				assignedClusterID: row.AssignedClusterID, assignedBaseURL: row.AssignedBaseUrl,
			})
		}
		list = nextList
		return nil
	}); err != nil {
		return err
	}

	logger.WithField("count", len(list)).Debug("Health poller checking instances")

	var wg sync.WaitGroup
	var checked, healthy, unhealthy, skipped int32
	serviceSummary := newServiceHealthSummary()
	for _, it := range list {
		applyServiceDefinitionFallback(&it)
		if it.host == "" || it.port == 0 {
			logger.WithField("instance_id", it.id).WithField("service", it.serviceID).Warn("Skipping health check: missing host or port")
			atomic.AddInt32(&skipped, 1)
			serviceSummary.recordSkipped(it.serviceID)
			recordSkippedHealthCheck(it.id)
			continue
		}
		proto := strings.ToLower(strings.TrimSpace(it.proto))
		if proto == "" {
			proto = strings.ToLower(strings.TrimSpace(it.defaultProto))
		}
		if proto == "" {
			proto = "http"
		}
		// HTTP health
		if proto == "http" {
			// path required for http; skip if not known
			if it.path == "" {
				logger.WithField("instance_id", it.id).WithField("service", it.serviceID).Warn("Skipping HTTP health check: no path configured")
				atomic.AddInt32(&skipped, 1)
				serviceSummary.recordSkipped(it.serviceID)
				recordSkippedHealthCheck(it.id)
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(ii serviceInstance) {
				defer wg.Done()
				defer func() { <-sem }()
				probeURL, urlErr := httpHealthURL(ii)
				status := "healthy"
				atomic.AddInt32(&checked, 1)
				probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if urlErr != nil {
					status = "unhealthy"
					atomic.AddInt32(&unhealthy, 1)
					serviceSummary.recordResult(ii.serviceID, status)
					logger.WithError(urlErr).WithField("service", ii.serviceID).WithField("health_endpoint", ii.path).Debug("HTTP health check endpoint invalid")
					if dbErr := persistHealthStatus(context.Background(), ii.id, status); dbErr != nil {
						logger.WithError(dbErr).WithField("instance_id", ii.id).Warn("Failed to persist health status")
					}
					return
				}
				req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, probeURL, nil)
				if err != nil {
					status = "unhealthy"
					atomic.AddInt32(&unhealthy, 1)
					serviceSummary.recordResult(ii.serviceID, status)
					logger.WithError(err).WithField("service", ii.serviceID).WithField("url", probeURL).Debug("HTTP health check request failed")
					if dbErr := persistHealthStatus(context.Background(), ii.id, status); dbErr != nil {
						logger.WithError(dbErr).WithField("instance_id", ii.id).Warn("Failed to persist health status")
					}
					return
				}
				resp, err := client.Do(req)
				if err != nil {
					status = "unhealthy"
					atomic.AddInt32(&unhealthy, 1)
					logger.WithError(err).WithField("service", ii.serviceID).WithField("url", probeURL).Debug("HTTP health check failed")
				} else if resp.StatusCode != 200 {
					status = "unhealthy"
					atomic.AddInt32(&unhealthy, 1)
					logger.WithField("service", ii.serviceID).WithField("url", probeURL).WithField("status_code", resp.StatusCode).Debug("HTTP health check returned non-200")
				} else {
					atomic.AddInt32(&healthy, 1)
					logger.WithField("service", ii.serviceID).WithField("url", probeURL).Debug("HTTP health check passed")
				}
				serviceSummary.recordResult(ii.serviceID, status)
				if resp != nil {
					_ = resp.Body.Close()
				}
				if persistErr := persistHealthStatus(context.Background(), ii.id, status); persistErr != nil {
					logger.WithError(persistErr).WithField("service", ii.serviceID).Debug("persist health status failed")
				}
			}(it)
			continue
		}
		// gRPC health
		if proto == "grpc" {
			wg.Add(1)
			sem <- struct{}{}
			go func(ii serviceInstance) {
				defer wg.Done()
				defer func() { <-sem }()
				addr := fmt.Sprintf("%s:%d", ii.host, ii.port)
				status := "healthy"
				atomic.AddInt32(&checked, 1)
				probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				transport, err := grpcHealthDialOption(ii, tls(ii.serviceID))
				if err != nil {
					status = "unhealthy"
					atomic.AddInt32(&unhealthy, 1)
					serviceSummary.recordResult(ii.serviceID, status)
					logger.WithError(err).WithField("service", ii.serviceID).WithField("addr", addr).Debug("gRPC health check TLS config failed")
					if dbErr := persistHealthStatus(context.Background(), ii.id, status); dbErr != nil {
						logger.WithError(dbErr).WithField("instance_id", ii.id).Warn("Failed to persist health status")
					}
					return
				}
				conn, err := grpc.NewClient(
					addr,
					transport,
					grpc.WithConnectParams(grpc.ConnectParams{MinConnectTimeout: 2 * time.Second}),
				)
				if err != nil {
					status = "unhealthy"
					atomic.AddInt32(&unhealthy, 1)
					serviceSummary.recordResult(ii.serviceID, status)
					logger.WithError(err).WithField("service", ii.serviceID).WithField("addr", addr).Debug("gRPC health check dial failed")
					if dbErr := persistHealthStatus(context.Background(), ii.id, status); dbErr != nil {
						logger.WithError(dbErr).WithField("instance_id", ii.id).Warn("Failed to persist health status")
					}
					return
				}
				defer func() { _ = conn.Close() }()
				hc := healthpb.NewHealthClient(conn)
				if _, err := hc.Check(probeCtx, &healthpb.HealthCheckRequest{}); err != nil {
					status = "unhealthy"
					atomic.AddInt32(&unhealthy, 1)
					logger.WithError(err).WithField("service", ii.serviceID).WithField("addr", addr).Debug("gRPC health check failed")
				} else {
					atomic.AddInt32(&healthy, 1)
					logger.WithField("service", ii.serviceID).WithField("addr", addr).Debug("gRPC health check passed")
				}
				serviceSummary.recordResult(ii.serviceID, status)
				if persistErr := persistHealthStatus(context.Background(), ii.id, status); persistErr != nil {
					logger.WithError(persistErr).WithField("service", ii.serviceID).Debug("persist health status failed")
				}
			}(it)
			continue
		}
		if proto == "tcp" {
			wg.Add(1)
			sem <- struct{}{}
			go func(ii serviceInstance) {
				defer wg.Done()
				defer func() { <-sem }()
				addr := fmt.Sprintf("%s:%d", ii.host, ii.port)
				status := "healthy"
				atomic.AddInt32(&checked, 1)
				probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				var d net.Dialer
				conn, err := d.DialContext(probeCtx, "tcp", addr)
				if err != nil {
					status = "unhealthy"
					atomic.AddInt32(&unhealthy, 1)
					logger.WithError(err).WithField("service", ii.serviceID).WithField("addr", addr).Debug("TCP health check failed")
				} else {
					atomic.AddInt32(&healthy, 1)
					_ = conn.Close()
					logger.WithField("service", ii.serviceID).WithField("addr", addr).Debug("TCP health check passed")
				}
				serviceSummary.recordResult(ii.serviceID, status)
				if persistErr := persistHealthStatus(context.Background(), ii.id, status); persistErr != nil {
					logger.WithError(persistErr).WithField("service", ii.serviceID).Debug("persist health status failed")
				}
			}(it)
			continue
		}
		logger.WithField("instance_id", it.id).WithField("service", it.serviceID).WithField("protocol", proto).Warn("Skipping health check: unsupported protocol")
		atomic.AddInt32(&skipped, 1)
		serviceSummary.recordSkipped(it.serviceID)
		recordSkippedHealthCheck(it.id)
	}
	wg.Wait()
	serviceHealth, healthyServices, unhealthyServices, skippedServices := serviceSummary.snapshot()
	summary := logger.
		WithField("queued", len(list)).
		WithField("checked", atomic.LoadInt32(&checked)).
		WithField("healthy", atomic.LoadInt32(&healthy)).
		WithField("unhealthy", atomic.LoadInt32(&unhealthy)).
		WithField("skipped", atomic.LoadInt32(&skipped)).
		WithField("service_health", serviceHealth).
		WithField("healthy_services", healthyServices).
		WithField("unhealthy_services", unhealthyServices).
		WithField("skipped_services", skippedServices)
	if atomic.LoadInt32(&unhealthy) > 0 || atomic.LoadInt32(&skipped) > 0 {
		summary.Warn("Health poller completed with unhealthy or skipped instances")
	} else {
		summary.Debug("Health poller completed")
	}
	return nil
}

func httpHealthURL(inst serviceInstance) (string, error) {
	endpoint := strings.TrimSpace(inst.path)
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if parsed.IsAbs() {
		if parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return "", fmt.Errorf("absolute health endpoint must use http or https")
		}
		return parsed.String(), nil
	}
	if !strings.HasPrefix(endpoint, "/") {
		return "", fmt.Errorf("relative health endpoint must begin with /")
	}
	return fmt.Sprintf("http://%s:%d%s", inst.host, inst.port, endpoint), nil
}

func applyServiceDefinitionFallback(i *serviceInstance) {
	def, ok := servicedefs.Lookup(i.serviceID)
	if !ok {
		return
	}
	if strings.TrimSpace(i.defaultProto) == "" {
		i.defaultProto = def.HealthProtocol
	}
	if strings.TrimSpace(i.proto) == "" && strings.TrimSpace(i.defaultProto) == "" {
		i.proto = def.HealthProtocol
	}
	// An instance registered without a health path is probed on the fleet-wide readiness path. The poller cannot see
	// which release the instance runs, so this stays on liveness while servicedefs.ReadySince is set.
	if strings.TrimSpace(i.path) == "" {
		i.path = def.ReadinessPath()
	}
	if i.port == 0 {
		i.port = def.DefaultPort
	}
}

// poolDNSWake, when set before StartHealthPoller, is invoked whenever a
// pool-assigned/physical service instance crosses its health state, so Navigator
// refreshes that instance's DNS — the pooled record of every media cluster it
// serves (livepeer.<cluster>, …) and its node-keyed infra record — immediately
// instead of waiting for the periodic reconcile. It is passed the INSTANCE name so
// the wake can resolve served clusters (pooled DNS is keyed by the served cluster,
// not the physical host cluster). Set once at startup before the poll goroutines
// launch, so the plain package var is race-free.
var poolDNSWake func(instanceID, serviceType string)

// SetPoolDNSWake registers the Navigator wake hook. Must be called before
// StartHealthPoller.
func SetPoolDNSWake(fn func(instanceID, serviceType string)) {
	poolDNSWake = fn
}

func persistHealthStatus(ctx context.Context, instanceID, status string) error {
	var oldStatus, serviceType string
	var scanErr error
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		// One statement: always bump last_health_check (the freshness gate
		// ListServiceInstancesByType depends on) AND return the prior status, so a
		// health transition can wake DNS without an extra read.
		row, queryErr := quartermasterdb.New(db).PersistServiceHealthStatus(ctx, quartermasterdb.PersistServiceHealthStatusParams{
			Status: status, InstanceID: instanceID,
		})
		scanErr = queryErr
		if queryErr == nil {
			oldStatus, serviceType = row.OldStatus, row.ServiceID
		}
		if errors.Is(scanErr, sql.ErrNoRows) {
			// Instance vanished between poll and write; nothing to persist or wake.
			return nil
		}
		return scanErr
	})
	if err != nil {
		return err
	}
	if errors.Is(scanErr, sql.ErrNoRows) {
		return nil
	}
	// service_id is the service type (ensureServiceExists sets them equal). Wake only
	// on an actual transition so an unchanged poll doesn't spam Navigator.
	if oldStatus != status && poolDNSWake != nil &&
		(dns.IsPhysicalEndpointServiceType(serviceType) || dns.IsPoolAssignedServiceType(serviceType)) {
		poolDNSWake(instanceID, serviceType)
	}
	return nil
}

func recordSkippedHealthCheck(instanceID string) {
	if instanceID == "" {
		return
	}
	if err := persistHealthStatus(context.Background(), instanceID, "skipped"); err != nil {
		logger.WithError(err).WithField("instance_id", instanceID).Warn("Failed to persist skipped health status")
	}
}

type grpcWatchManager struct {
	mu      sync.Mutex
	active  map[string]context.CancelFunc
	backoff map[string]time.Time
	tls     func(serviceID string) HealthWatchTLS
}

func startGrpcHealthWatchers(refreshInterval, dialTimeout, backoff time.Duration, sem chan struct{}, tls func(serviceID string) HealthWatchTLS) {
	manager := &grpcWatchManager{
		active:  make(map[string]context.CancelFunc),
		backoff: make(map[string]time.Time),
		tls:     tls,
	}

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		if err := manager.refreshGrpcWatches(dialTimeout, backoff, sem); err != nil {
			logger.WithError(err).Warn("grpc health watcher refresh failed")
		}
		<-ticker.C
	}
}

func (m *grpcWatchManager) refreshGrpcWatches(dialTimeout, backoff time.Duration, sem chan struct{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := quartermasterdb.New(db).ListGRPCHealthWatchCandidates(ctx)
	if err != nil {
		return err
	}

	desired := make(map[string]serviceInstance)
	now := time.Now()

	for _, row := range rows {
		port := 0
		if row.Port.Valid {
			port = int(row.Port.Int32)
		}
		i := serviceInstance{
			id: row.InstanceID, serviceID: row.ServiceID, host: row.AdvertiseHost,
			port: port, proto: row.Protocol, defaultProto: row.DefaultProtocol,
			assignedClusterID: row.AssignedClusterID, assignedBaseURL: row.AssignedBaseUrl,
		}
		finalProto := strings.ToLower(strings.TrimSpace(i.proto))
		if finalProto == "" {
			finalProto = strings.ToLower(strings.TrimSpace(i.defaultProto))
		}
		if finalProto != "grpc" || i.host == "" || i.port == 0 {
			continue
		}
		desired[i.id] = i
	}

	m.mu.Lock()
	for id, cancel := range m.active {
		if _, ok := desired[id]; !ok {
			cancel()
			delete(m.active, id)
		}
	}
	m.mu.Unlock()

	for id, inst := range desired {
		m.mu.Lock()
		if _, ok := m.active[id]; ok {
			m.mu.Unlock()
			continue
		}
		if until, ok := m.backoff[id]; ok && until.After(now) {
			m.mu.Unlock()
			continue
		}
		// mark active to prevent duplicate starts
		ctxWatch, cancel := context.WithCancel(context.Background())
		m.active[id] = cancel
		m.mu.Unlock()

		sem <- struct{}{}
		go func(ii serviceInstance, instID string, watchCtx context.Context) {
			defer func() { <-sem }()
			defer m.clearWatch(instID)
			m.watchGrpcInstance(watchCtx, ii, dialTimeout, backoff)
		}(inst, id, ctxWatch)
	}

	return nil
}

func (m *grpcWatchManager) clearWatch(instanceID string) {
	m.mu.Lock()
	if cancel, ok := m.active[instanceID]; ok {
		cancel()
		delete(m.active, instanceID)
	}
	m.mu.Unlock()
}

func (m *grpcWatchManager) watchGrpcInstance(ctx context.Context, inst serviceInstance, dialTimeout, backoff time.Duration) {
	addr := fmt.Sprintf("%s:%d", inst.host, inst.port)
	transport, err := grpcHealthDialOption(inst, m.tls(inst.serviceID))
	if err != nil {
		logger.WithError(err).WithField("service", inst.serviceID).WithField("addr", addr).Debug("gRPC watch TLS config failed")
		m.setBackoff(inst.id, backoff)
		return
	}
	conn, err := grpc.NewClient(
		addr,
		transport,
		grpc.WithConnectParams(grpc.ConnectParams{MinConnectTimeout: dialTimeout}),
	)
	if err != nil {
		logger.WithError(err).WithField("service", inst.serviceID).WithField("addr", addr).Debug("gRPC watch dial failed")
		m.setBackoff(inst.id, backoff)
		return
	}
	defer func() { _ = conn.Close() }()

	client := healthpb.NewHealthClient(conn)
	stream, err := client.Watch(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			m.setBackoff(inst.id, backoff)
			return
		}
		logger.WithError(err).WithField("service", inst.serviceID).WithField("addr", addr).Debug("gRPC watch start failed")
		return
	}

	for {
		resp, err := stream.Recv()
		if err != nil {
			logger.WithError(err).WithField("service", inst.serviceID).WithField("addr", addr).Debug("gRPC watch ended")
			return
		}
		statusStr := mapGrpcHealthStatus(resp.GetStatus())
		// Route through persistHealthStatus so a gRPC-watch health transition also
		// wakes physical-endpoint DNS, same as the HTTP poll path.
		if dbErr := persistHealthStatus(context.Background(), inst.id, statusStr); dbErr != nil {
			logger.WithError(dbErr).WithField("instance_id", inst.id).Warn("Failed to persist health status")
		}
	}
}

func grpcHealthDialOption(inst serviceInstance, tls HealthWatchTLS) (grpc.DialOption, error) {
	serverName, caPath := grpcHealthTLSConfig(inst, strings.TrimSpace(tls.CAPath), tls.ServerName)
	return grpcutil.ClientTLS(grpcutil.ClientTLSConfig{
		CACertFile:    caPath,
		ServerName:    serverName,
		AllowInsecure: tls.AllowInsecure,
	}, logger)
}

func grpcHealthTLSConfig(inst serviceInstance, caPath, configured string) (serverName, caFile string) {
	configuredName := strings.TrimSpace(configured)
	if configuredName != "" {
		return configuredName, caPath
	}
	if strings.TrimSpace(caPath) == "" {
		return "", ""
	}
	serviceID := strings.TrimSpace(inst.serviceID)
	if serviceID == "" {
		return "", caPath
	}
	return serviceID + ".internal", caPath
}

func grpcHealthServerName(inst serviceInstance, caPath, configured string) string {
	serverName, _ := grpcHealthTLSConfig(inst, caPath, configured)
	return serverName
}

func (m *grpcWatchManager) setBackoff(instanceID string, backoff time.Duration) {
	m.mu.Lock()
	m.backoff[instanceID] = time.Now().Add(backoff)
	m.mu.Unlock()
}

func mapGrpcHealthStatus(status healthpb.HealthCheckResponse_ServingStatus) string {
	switch status {
	case healthpb.HealthCheckResponse_SERVING:
		return "healthy"
	case healthpb.HealthCheckResponse_NOT_SERVING:
		return "unhealthy"
	default:
		return "unknown"
	}
}
