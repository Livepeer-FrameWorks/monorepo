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

	"golang.org/x/sync/singleflight"
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
	currentHealthProbe.Store(&healthProbe{client: client, tls: tls})

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
	probe := healthProbe{client: client, tls: tls}
	for _, it := range list {
		applyServiceDefinitionFallback(&it)
		proto, skipReason := healthProbeProtocol(it)
		if skipReason != "" {
			logger.WithField("instance_id", it.id).WithField("service", it.serviceID).WithField("protocol", proto).Warn("Skipping health check: " + skipReason)
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
			atomic.AddInt32(&checked, 1)
			status := probe.checkAndPersist(ii, proto)
			if status == "healthy" {
				atomic.AddInt32(&healthy, 1)
			} else {
				atomic.AddInt32(&unhealthy, 1)
			}
			serviceSummary.recordResult(ii.serviceID, status)
		}(it)
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

// healthProbeTimeout bounds one health check of one instance.
const healthProbeTimeout = 2 * time.Second

// healthProbe checks one service instance over its health protocol.
type healthProbe struct {
	client *http.Client
	tls    func(serviceID string) HealthWatchTLS
}

// healthProbeProtocol resolves the protocol an instance is checked over, or
// the reason it cannot be checked.
func healthProbeProtocol(inst serviceInstance) (proto, skipReason string) {
	proto = strings.ToLower(strings.TrimSpace(inst.proto))
	if proto == "" {
		proto = strings.ToLower(strings.TrimSpace(inst.defaultProto))
	}
	if proto == "" {
		proto = "http"
	}
	switch {
	case inst.host == "" || inst.port == 0:
		return proto, "missing host or port"
	case proto == "http" && inst.path == "":
		return proto, "no HTTP health path configured"
	case proto != "http" && proto != "grpc" && proto != "tcp":
		return proto, "unsupported protocol"
	}
	return proto, ""
}

// check returns nil when the instance answers healthy, else why it did not.
func (p healthProbe) check(inst serviceInstance, proto string) error {
	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	defer cancel()
	addr := fmt.Sprintf("%s:%d", inst.host, inst.port)
	switch proto {
	case "http":
		probeURL, err := httpHealthURL(inst)
		if err != nil {
			return fmt.Errorf("health endpoint %q: %w", inst.path, err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
		if err != nil {
			return fmt.Errorf("build request for %s: %w", probeURL, err)
		}
		resp, err := p.client.Do(req)
		if err != nil {
			return fmt.Errorf("GET %s: %w", probeURL, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s returned %d", probeURL, resp.StatusCode)
		}
		return nil
	case "grpc":
		tls := HealthWatchTLS{}
		if p.tls != nil {
			tls = p.tls(inst.serviceID)
		}
		transport, err := grpcHealthDialOption(inst, tls)
		if err != nil {
			return fmt.Errorf("gRPC TLS config for %s: %w", addr, err)
		}
		conn, err := grpc.NewClient(addr, transport, grpc.WithConnectParams(grpc.ConnectParams{MinConnectTimeout: healthProbeTimeout}))
		if err != nil {
			return fmt.Errorf("gRPC dial %s: %w", addr, err)
		}
		defer func() { _ = conn.Close() }()
		if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
			return fmt.Errorf("gRPC health %s: %w", addr, err)
		}
		return nil
	case "tcp":
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("TCP dial %s: %w", addr, err)
		}
		_ = conn.Close()
		return nil
	}
	return fmt.Errorf("unsupported protocol %q", proto)
}

// checkAndPersist checks the instance and records the result.
func (p healthProbe) checkAndPersist(inst serviceInstance, proto string) string {
	status := "healthy"
	if err := p.check(inst, proto); err != nil {
		status = "unhealthy"
		logger.WithError(err).WithField("service", inst.serviceID).WithField("instance_id", inst.id).Debug("Health check failed")
	}
	if err := persistHealthStatus(context.Background(), inst.id, status); err != nil {
		logger.WithError(err).WithField("service", inst.serviceID).WithField("instance_id", inst.id).Warn("Failed to persist health status")
	}
	return status
}

var (
	// currentHealthProbe carries the running poller's HTTP client and TLS
	// settings to on-demand checks.
	currentHealthProbe atomic.Pointer[healthProbe]
	// onDemandProbes lets concurrent on-demand checks of one instance share a
	// single probe.
	onDemandProbes singleflight.Group
)

// ProbeDiscoveredInstances checks the given instances now, the way the poller
// does, and persists each result. A discovery that finds a running instance
// marked unhealthy calls it, so an instance that came back is usable without
// waiting for the next poll. It returns the new status per instance ID;
// instances that cannot be checked are left out.
func ProbeDiscoveredInstances(ctx context.Context, rows []quartermasterdb.ServiceDiscoveryRow) map[string]string {
	probe := healthProbe{client: &http.Client{Timeout: healthProbeTimeout}}
	if current := currentHealthProbe.Load(); current != nil {
		probe = *current
	}
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results = make(map[string]string, len(rows))
	)
	for _, row := range rows {
		inst := serviceInstance{
			id: row.InstanceID, serviceID: row.ServiceID, proto: row.Protocol,
			host: row.AdvertiseHost.String, port: int(row.Port.Int32), path: row.HealthEndpoint.String,
		}
		applyServiceDefinitionFallback(&inst)
		proto, skipReason := healthProbeProtocol(inst)
		if skipReason != "" {
			logger.WithField("instance_id", inst.id).WithField("service", inst.serviceID).Debug("On-demand health check skipped: " + skipReason)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := onDemandProbes.DoChan(inst.id, func() (interface{}, error) {
				return probe.checkAndPersist(inst, proto), nil
			})
			select {
			case res := <-ch:
				if status, ok := res.Val.(string); ok {
					mu.Lock()
					results[inst.id] = status
					mu.Unlock()
				}
			case <-ctx.Done():
			}
		}()
	}
	wg.Wait()
	return results
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
