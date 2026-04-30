package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"frameworks/pkg/grpcutil"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// StartHealthPoller launches a background goroutine that polls HTTP/gRPC health endpoints
// for all registered service instances and updates their health status in the database.
var pollerInFlight int32

func StartHealthPoller() {
	interval := time.Duration(getenvInt("QM_HEALTH_POLL_INTERVAL_SECONDS", 30)) * time.Second
	timeout := time.Duration(getenvInt("QM_HEALTH_TIMEOUT_MS", 2000)) * time.Millisecond
	maxConc := getenvInt("QM_HEALTH_MAX_CONCURRENCY", 8)
	if maxConc <= 0 {
		maxConc = 8
	}
	batchSize := getenvInt("QM_HEALTH_BATCH_SIZE", 200)
	if batchSize <= 0 {
		batchSize = 200
	}
	minAgeSeconds := getenvInt("QM_HEALTH_MIN_AGE_SECONDS", int(interval.Seconds()))
	if minAgeSeconds < 0 {
		minAgeSeconds = int(interval.Seconds())
	}

	client := &http.Client{Timeout: timeout}
	sem := make(chan struct{}, maxConc)
	minAge := time.Duration(minAgeSeconds) * time.Second

	watchEnabled := getenvBool("QM_HEALTH_GRPC_WATCH", true)
	if watchEnabled {
		watchRefresh := time.Duration(getenvInt("QM_HEALTH_WATCH_REFRESH_SECONDS", 60)) * time.Second
		watchBackoff := time.Duration(getenvInt("QM_HEALTH_WATCH_BACKOFF_SECONDS", 300)) * time.Second
		watchDialTimeout := time.Duration(getenvInt("QM_HEALTH_WATCH_DIAL_TIMEOUT_MS", 2000)) * time.Millisecond
		watchMaxConc := getenvInt("QM_HEALTH_WATCH_MAX_CONCURRENCY", maxConc)
		if watchMaxConc <= 0 {
			watchMaxConc = maxConc
		}
		watchSem := make(chan struct{}, watchMaxConc)
		go startGrpcHealthWatchers(watchRefresh, watchDialTimeout, watchBackoff, watchSem)
	}

	go func() {
		// Add ±25% jitter to prevent thundering herd on restart
		jitterRange := int64(interval / 4)
		for {
			if !atomic.CompareAndSwapInt32(&pollerInFlight, 0, 1) {
				time.Sleep(interval)
				continue
			}
			if err := pollOnce(client, sem, batchSize, minAge); err != nil {
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

func pollOnce(client *http.Client, sem chan struct{}, batchSize int, minAge time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cutoff := time.Now().Add(-minAge)
	rows, err := db.QueryContext(ctx, `
        SELECT si.instance_id, si.service_id, si.cluster_id, si.protocol, si.advertise_host, si.port,
               COALESCE(si.health_endpoint_override, s.health_check_path) AS path,
               si.last_health_check, s.protocol AS default_protocol
        FROM quartermaster.service_instances si
        JOIN quartermaster.services s ON si.service_id = s.service_id
        WHERE si.status IN ('running','starting')
          AND si.service_id NOT LIKE 'edge-%'
          AND (si.last_health_check IS NULL OR si.last_health_check < $1)
        ORDER BY COALESCE(si.last_health_check, si.created_at) ASC
        LIMIT $2
    `, cutoff, batchSize)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	var list []serviceInstance
	for rows.Next() {
		var i serviceInstance
		var proto, defaultProto sql.NullString
		var host sql.NullString
		var path sql.NullString
		if err := rows.Scan(&i.id, &i.serviceID, new(string), &proto, &host, &i.port, &path, new(sql.NullTime), &defaultProto); err == nil {
			if proto.Valid {
				i.proto = proto.String
			}
			if defaultProto.Valid {
				i.defaultProto = defaultProto.String
			}
			if host.Valid {
				i.host = host.String
			}
			if path.Valid {
				i.path = path.String
			}
			list = append(list, i)
		}
	}

	logger.WithField("count", len(list)).Debug("Health poller checking instances")

	var wg sync.WaitGroup
	var checked, healthy, unhealthy, skipped int32
	serviceSummary := newServiceHealthSummary()
	for _, it := range list {
		if it.host == "" || it.port == 0 {
			logger.WithField("instance_id", it.id).WithField("service", it.serviceID).Warn("Skipping health check: missing host or port")
			atomic.AddInt32(&skipped, 1)
			serviceSummary.recordSkipped(it.serviceID)
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
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(ii serviceInstance) {
				defer wg.Done()
				defer func() { <-sem }()
				url := fmt.Sprintf("http://%s:%d%s", ii.host, ii.port, ii.path)
				status := "healthy"
				atomic.AddInt32(&checked, 1)
				probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
				if err != nil {
					status = "unhealthy"
					atomic.AddInt32(&unhealthy, 1)
					serviceSummary.recordResult(ii.serviceID, status)
					logger.WithError(err).WithField("service", ii.serviceID).WithField("url", url).Debug("HTTP health check request failed")
					if _, dbErr := db.ExecContext(context.Background(), `UPDATE quartermaster.service_instances SET health_status=$1, last_health_check=NOW(), updated_at=NOW() WHERE instance_id=$2`, status, ii.id); dbErr != nil {
						logger.WithError(dbErr).WithField("instance_id", ii.id).Warn("Failed to persist health status")
					}
					return
				}
				resp, err := client.Do(req)
				if err != nil {
					status = "unhealthy"
					atomic.AddInt32(&unhealthy, 1)
					logger.WithError(err).WithField("service", ii.serviceID).WithField("url", url).Debug("HTTP health check failed")
				} else if resp.StatusCode != 200 {
					status = "unhealthy"
					atomic.AddInt32(&unhealthy, 1)
					logger.WithField("service", ii.serviceID).WithField("url", url).WithField("status_code", resp.StatusCode).Debug("HTTP health check returned non-200")
				} else {
					atomic.AddInt32(&healthy, 1)
					logger.WithField("service", ii.serviceID).WithField("url", url).Debug("HTTP health check passed")
				}
				serviceSummary.recordResult(ii.serviceID, status)
				if resp != nil {
					_ = resp.Body.Close()
				}
				_, _ = db.ExecContext(context.Background(), `UPDATE quartermaster.service_instances SET health_status=$1, last_health_check=NOW(), updated_at=NOW() WHERE instance_id=$2`, status, ii.id)
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
				transport, err := grpcHealthDialOption()
				if err != nil {
					status = "unhealthy"
					atomic.AddInt32(&unhealthy, 1)
					serviceSummary.recordResult(ii.serviceID, status)
					logger.WithError(err).WithField("service", ii.serviceID).WithField("addr", addr).Debug("gRPC health check TLS config failed")
					if _, dbErr := db.ExecContext(context.Background(), `UPDATE quartermaster.service_instances SET health_status=$1, last_health_check=NOW(), updated_at=NOW() WHERE instance_id=$2`, status, ii.id); dbErr != nil {
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
					if _, dbErr := db.ExecContext(context.Background(), `UPDATE quartermaster.service_instances SET health_status=$1, last_health_check=NOW(), updated_at=NOW() WHERE instance_id=$2`, status, ii.id); dbErr != nil {
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
				_, _ = db.ExecContext(context.Background(), `UPDATE quartermaster.service_instances SET health_status=$1, last_health_check=NOW(), updated_at=NOW() WHERE instance_id=$2`, status, ii.id)
			}(it)
			continue
		}
		logger.WithField("instance_id", it.id).WithField("service", it.serviceID).WithField("protocol", proto).Warn("Skipping health check: unsupported protocol")
		atomic.AddInt32(&skipped, 1)
		serviceSummary.recordSkipped(it.serviceID)
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

func getenvInt(key string, def int) int {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return def
	}
	return n
}

func getenvBool(key string, def bool) bool {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	switch strings.ToLower(val) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}

type grpcWatchManager struct {
	mu      sync.Mutex
	active  map[string]context.CancelFunc
	backoff map[string]time.Time
}

func startGrpcHealthWatchers(refreshInterval, dialTimeout, backoff time.Duration, sem chan struct{}) {
	manager := &grpcWatchManager{
		active:  make(map[string]context.CancelFunc),
		backoff: make(map[string]time.Time),
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

	rows, err := db.QueryContext(ctx, `
		SELECT si.instance_id, si.service_id, si.advertise_host, si.port, si.protocol, s.protocol AS default_protocol
		FROM quartermaster.service_instances si
		JOIN quartermaster.services s ON si.service_id = s.service_id
		WHERE si.status IN ('running','starting')
	`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	desired := make(map[string]serviceInstance)
	now := time.Now()

	for rows.Next() {
		var i serviceInstance
		var host, proto, defaultProto sql.NullString
		if err := rows.Scan(&i.id, &i.serviceID, &host, &i.port, &proto, &defaultProto); err != nil {
			continue
		}
		if host.Valid {
			i.host = host.String
		}
		if proto.Valid {
			i.proto = proto.String
		}
		if defaultProto.Valid {
			i.defaultProto = defaultProto.String
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
	transport, err := grpcHealthDialOption()
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
		if _, dbErr := db.ExecContext(context.Background(), `UPDATE quartermaster.service_instances SET health_status=$1, last_health_check=NOW(), updated_at=NOW() WHERE instance_id=$2`, statusStr, inst.id); dbErr != nil {
			logger.WithError(dbErr).WithField("instance_id", inst.id).Warn("Failed to persist health status")
		}
	}
}

func grpcHealthDialOption() (grpc.DialOption, error) {
	return grpcutil.ClientTLS(grpcutil.ClientTLSConfig{
		CACertFile:    strings.TrimSpace(os.Getenv("GRPC_TLS_CA_PATH")),
		ServerName:    strings.TrimSpace(os.Getenv("GRPC_TLS_SERVER_NAME")),
		AllowInsecure: getenvBool("GRPC_ALLOW_INSECURE", false),
	}, logger)
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
