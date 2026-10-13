package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"frameworks/api_network_observer/internal/appconfig"
	"frameworks/api_network_observer/internal/coordinator"
	"frameworks/api_network_observer/internal/providers/livepeer"
	"frameworks/api_network_observer/internal/store"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
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
	httpServer := &http.Server{Addr: ":" + cfg.HTTPPort, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if shutdownErr := httpServer.Shutdown(stopCtx); shutdownErr != nil {
			logger.WithError(shutdownErr).Warn("Observer shutdown failed")
		}
	}()
	if err = httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.WithError(err).Fatal("Observer HTTP server failed")
	}
}
