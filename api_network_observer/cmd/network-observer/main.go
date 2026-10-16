package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"frameworks/api_network_observer/internal/appconfig"
	"frameworks/api_network_observer/internal/coordinator"
	"frameworks/api_network_observer/internal/providers/livepeer"
	"frameworks/api_network_observer/internal/store"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
	"github.com/twmb/franz-go/pkg/kadm"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if version.HandleCLI() {
		return
	}
	logger := logging.NewLoggerWithService("network-observer")
	config.LoadEnv(logger)
	cfg, err := config.Load[appconfig.Observer](config.Options{Service: "network-observer", Logger: logger})
	if err != nil {
		logger.WithError(err).Fatal("Invalid observer configuration")
	}
	cfg.ApplyLogLevel(logger)
	if cfg.MaxObservers < 1 || cfg.CatalogIntervalSeconds < 60 {
		logger.Fatal("Invalid observer limits")
	}
	dc := database.DefaultConfig()
	dc.ServiceName = "network-observer"
	dc.URL = cfg.DatabaseURL
	db := database.MustConnect(dc, logger)
	defer db.Close()
	catalog, chain, err := livepeer.DialCatalog(context.Background(), cfg.EthRPCURL, cfg.Controller, cfg.NetworkID, cfg.Environment)
	if err != nil {
		logger.WithError(err).Fatal("Invalid catalog configuration")
	}
	defer chain()
	geo, err := geoip.NewReader(cfg.GeoIPPath)
	if err != nil {
		logger.WithError(err).Fatal("Invalid GeoIP database")
	}
	if geo != nil {
		defer geo.Close()
	}
	engine := coordinator.Engine{Network: cfg.NetworkID, Environment: cfg.Environment, Locate: func(ip string) obs.Location {
		if geo != nil {
			if d := geo.Lookup(ip); d != nil && (d.Latitude != 0 || d.Longitude != 0) {
				return obs.Location{Known: true, Latitude: d.Latitude, Longitude: d.Longitude, Country: d.CountryCode, City: d.City, Source: "mmdb"}
			}
		}
		return obs.Location{Source: "unknown"}
	}}
	catalogInterval := time.Duration(cfg.CatalogIntervalSeconds) * time.Second
	readiness := coordinator.NewReadiness(catalogInterval, time.Now().UTC())
	api := &coordinator.API{Store: &store.Store{DB: db, TenantID: cfg.TenantID}, Engine: engine, Secret: cfg.SharedSecret, ClusterID: cfg.ClusterID, MaxObservers: cfg.MaxObservers, Readiness: readiness}
	for _, raw := range cfg.TrustedProxyCIDRs {
		prefix, prefixErr := netip.ParsePrefix(raw)
		if prefixErr != nil {
			logger.Fatal("Invalid trusted ingress CIDR")
		}
		api.TrustedProxies = append(api.TrustedProxies, prefix)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	consumerGroup := "network-observer-discovery-" + cfg.Environment
	consumer, err := kafka.NewConsumer(cfg.KafkaBrokers, consumerGroup, cfg.KafkaClusterID, "network-observer", logger)
	if err != nil {
		logger.WithError(err).Fatal("Gateway discovery consumer unavailable")
	}
	defer consumer.Close()
	registerGatewaySubscriptions(consumer, api.Gateway, cfg.MirrorRegionPrefixes)
	go func() {
		consumeErr := consumer.Start(ctx)
		if ctx.Err() == nil {
			readiness.KafkaStopped(consumeErr)
			logger.WithError(consumeErr).Error("Gateway discovery consumer stopped")
		}
	}()
	go func() {
		admin := kadm.NewClient(consumer.GetClient())
		for {
			sampleCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			memberID, _ := consumer.GetClient().GroupMetadata()
			sample := coordinator.SampleKafkaGroup(sampleCtx, admin, consumerGroup, memberID)
			stop()
			if ctx.Err() != nil {
				return
			}
			readiness.KafkaSampled(time.Now().UTC(), sample)
			// Joining the group takes seconds; sample quickly until then.
			wait := coordinator.KafkaSampleInterval
			if !sample.Joined || sample.Partitions == 0 {
				wait = 5 * time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
	var definitions []obs.Observer
	if err = json.Unmarshal([]byte(cfg.ObserverDefinitions), &definitions); err != nil {
		logger.WithError(err).Fatal("Invalid observer definitions")
	}
	if err = api.Store.Update(ctx, func(s *store.State, _ *sql.Tx) error {
		for _, o := range definitions {
			if old, ok := s.Observers[o.ID]; ok {
				o.LastSeen = old.LastSeen
			}
			s.Observers[o.ID] = o
		}
		return nil
	}); err != nil {
		logger.WithError(err).Fatal("Observer schema unavailable")
	}
	sender, err := decklog.NewBatchedClient(decklog.BatchedClientConfig{Target: cfg.DecklogGRPCAddr, ServiceToken: cfg.ServiceToken, Source: "network-observer", ClusterID: cfg.ClusterID, AllowInsecure: cfg.AllowInsecure, CACertFile: cfg.CAPath}, logger)
	if err != nil {
		logger.WithError(err).Fatal("Decklog connection configuration invalid")
	}
	defer sender.Close()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for i := 0; i < 100; i++ {
					drainErr := api.Drain(ctx, sender)
					if errors.Is(drainErr, sql.ErrNoRows) {
						break
					}
					if drainErr != nil {
						logger.WithError(drainErr).Warn("Telemetry outbox delivery failed")
						break
					}
				}
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(catalogInterval)
		defer ticker.Stop()
		for {
			refreshCtx, stop := context.WithTimeout(ctx, coordinator.CatalogRefreshTimeout)
			resources, refreshErr := catalog.List(refreshCtx)
			stop()
			if refreshErr != nil {
				logger.WithError(refreshErr).Warn("Catalog refresh failed; preserving inventory")
			} else if refreshErr = api.Store.Update(ctx, func(s *store.State, _ *sql.Tx) error { engine.Catalog(s, resources, time.Now().UTC()); return nil }); refreshErr != nil {
				logger.WithError(refreshErr).Error("Catalog persistence failed")
			}
			if ctx.Err() == nil {
				readiness.CatalogRefreshed(time.Now().UTC(), refreshErr)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if scheduleErr := api.Store.Update(ctx, func(s *store.State, tx *sql.Tx) error {
					engine.Schedule(s, now.UTC())
					_, pruneErr := tx.ExecContext(ctx, "DELETE FROM network_observer.ingest_receipts WHERE tenant_id=$1 AND received_at<now()-interval '30 days'", cfg.TenantID)
					return pruneErr
				}); scheduleErr != nil {
					logger.WithError(scheduleErr).Warn("Assignment scheduling failed")
				}
			}
		}
	}()
	handler := api.Handler()
	plainListener, err := server.ListenAddrs(cfg.BindAddrs, cfg.HTTPPort)
	if err != nil {
		logger.WithError(err).Fatal("Observer HTTP listener failed")
	}
	httpServer := newHTTPServer(handler)
	internalTLS := grpcutil.ServerTLSConfig{CertFile: cfg.CertPath, KeyFile: cfg.KeyPath}
	var internalServer *http.Server
	if cfg.CertPath != "" && cfg.KeyPath != "" {
		internalServer = newHTTPServer(handler)
		httpServer.Handler = probesOnly(handler)
		go serveInternalTLS(ctx, internalServer, cfg.BindAddrs, cfg.InternalPort, internalTLS, logger)
	}
	go registerWithQuartermaster(ctx, cfg, logger)
	go refreshGatewayLocations(ctx, cfg, api.Store, catalogInterval, logger)
	go func() {
		<-ctx.Done()
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		for _, srv := range []*http.Server{internalServer, httpServer} {
			if srv == nil {
				continue
			}
			if shutdownErr := srv.Shutdown(stopCtx); shutdownErr != nil {
				logger.WithError(shutdownErr).Warn("Observer shutdown failed")
			}
		}
	}()
	if err = httpServer.Serve(plainListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.WithError(err).Fatal("Observer HTTP server failed")
	}
}

// probesOnly limits the plain listener to health and readiness once the
// internal TLS listener carries the API, so the bearer secret never crosses the
// mesh in plaintext.
func probesOnly(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/health" || r.URL.Path == "/ready") {
			handler.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
}

// serveInternalTLS serves the API to mesh callers with the internal-CA leaf.
// Privateer may write the leaf after the process starts, so the listener opens
// once the files exist; the plain listener keeps health probes answering
// meanwhile. The certificate is reloaded when Privateer renews it.
func serveInternalTLS(ctx context.Context, srv *http.Server, bindAddrs []string, port string, files grpcutil.ServerTLSConfig, logger logging.Logger) {
	if err := grpcutil.WaitForServerTLSFiles(ctx, files, logger); err != nil {
		if ctx.Err() == nil {
			logger.WithError(err).Fatal("Observer internal TLS files unavailable")
		}
		return
	}
	tlsConfig, err := grpcutil.HTTPServerTLSConfig(files)
	if err != nil {
		logger.WithError(err).Fatal("Observer internal TLS configuration invalid")
	}
	srv.TLSConfig = tlsConfig
	listener, err := server.ListenAddrs(bindAddrs, port)
	if err != nil {
		logger.WithError(err).Fatal("Observer internal HTTPS listener failed")
	}
	if err := srv.ServeTLS(listener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.WithError(err).Fatal("Observer internal HTTPS server failed")
	}
}

// nodeLister is the Quartermaster node listing refreshGatewayLocations reads.
type nodeLister interface {
	ListNodes(ctx context.Context, clusterID, nodeType, region string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error)
}

// refreshGatewayLocations positions gateway observers from Quartermaster's
// node coordinates once per catalog interval. Gateway telemetry carries no
// address of the gateway host, so the node row is the only location source.
func refreshGatewayLocations(ctx context.Context, cfg *appconfig.Observer, st *store.Store, interval time.Duration, logger logging.Logger) {
	if cfg.QuartermasterGRPCAddr == "" {
		logger.Info("Quartermaster address unset; gateway observer locations stay unknown")
		return
	}
	qmClient, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      cfg.QuartermasterGRPCAddr,
		Timeout:       30 * time.Second,
		Logger:        logger,
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.QuartermasterTLSName,
	})
	if err != nil {
		logger.WithError(err).Warn("Quartermaster client unavailable; gateway observer locations stay unknown")
		return
	}
	defer func() { _ = qmClient.Close() }()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		listCtx, stop := context.WithTimeout(ctx, time.Minute)
		nodes, listErr := listNodeLocations(listCtx, qmClient)
		stop()
		if listErr != nil {
			logger.WithError(listErr).Warn("Gateway observer location refresh failed")
		} else if updateErr := st.Update(ctx, func(s *store.State, _ *sql.Tx) error {
			coordinator.LocateGateways(s, nodes)
			return nil
		}); updateErr != nil {
			logger.WithError(updateErr).Warn("Gateway observer location persistence failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// listNodeLocations reads every Quartermaster node, following pagination to
// the end so no gateway loses its position to a truncated page.
func listNodeLocations(ctx context.Context, qm nodeLister) ([]coordinator.NodeLocation, error) {
	const pageSize = 500
	var out []coordinator.NodeLocation
	after := ""
	for {
		page := &commonpb.CursorPaginationRequest{First: pageSize}
		if after != "" {
			cursor := after
			page.After = &cursor
		}
		resp, err := qm.ListNodes(ctx, "", "", "", page)
		if err != nil {
			return nil, err
		}
		for _, n := range resp.GetNodes() {
			out = append(out, coordinator.NodeLocation{
				NodeID:    n.GetNodeId(),
				ClusterID: n.GetClusterId(),
				Latitude:  n.GetLatitude(),
				Longitude: n.GetLongitude(),
				Known:     n.Latitude != nil && n.Longitude != nil,
			})
		}
		p := resp.GetPagination()
		if p == nil || !p.GetHasNextPage() {
			return out, nil
		}
		end := p.GetEndCursor()
		if end == "" || end == after {
			return nil, fmt.Errorf("list nodes: pagination cursor %q did not advance", end)
		}
		after = end
	}
}

// registerWithQuartermaster records this instance in the service registry,
// which is what resolves network-observer.<region>.internal for Bridge on other
// nodes. The registered port is the plain listener that serves the readiness
// probe.
func registerWithQuartermaster(ctx context.Context, cfg *appconfig.Observer, logger logging.Logger) {
	if cfg.QuartermasterGRPCAddr == "" {
		logger.Info("Quartermaster address unset; observer not registered for mesh DNS")
		return
	}
	qmClient, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      cfg.QuartermasterGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.QuartermasterTLSName,
	})
	if err != nil {
		logger.WithError(err).Warn("Quartermaster client unavailable; observer not registered")
		return
	}
	defer func() { _ = qmClient.Close() }()
	req, err := qmbootstrap.NewServiceRequest(qmbootstrap.ServiceRegistration{
		ServiceType:   "network-observer",
		Port:          cfg.HTTPPort,
		AdvertiseHost: cfg.AdvertiseHost,
		ClusterID:     cfg.Registration.ClusterID,
		NodeID:        cfg.NodeID,
	})
	if err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap skipped")
		return
	}
	if _, err := qmbootstrap.BootstrapServiceWithRetry(ctx, qmClient, req, logger, qmbootstrap.DefaultRetryConfig("network-observer")); err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap (network-observer) failed")
		return
	}
	logger.Info("Quartermaster bootstrap (network-observer) ok")
}
