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
	api := &coordinator.API{Store: &store.Store{DB: db, TenantID: cfg.TenantID}, Engine: engine, Secret: cfg.SharedSecret, ClusterID: cfg.ClusterID, MaxObservers: cfg.MaxObservers}
	for _, raw := range cfg.TrustedProxyCIDRs {
		prefix, prefixErr := netip.ParsePrefix(raw)
		if prefixErr != nil {
			logger.Fatal("Invalid trusted ingress CIDR")
		}
		api.TrustedProxies = append(api.TrustedProxies, prefix)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	consumer, err := kafka.NewConsumer(cfg.KafkaBrokers, "network-observer-discovery-"+cfg.Environment, cfg.KafkaClusterID, "network-observer", logger)
	if err != nil {
		logger.WithError(err).Fatal("Gateway discovery consumer unavailable")
	}
	defer consumer.Close()
	registerGatewaySubscriptions(consumer, api.Gateway, cfg.MirrorRegionPrefixes)
	go func() {
		if consumeErr := consumer.Start(ctx); consumeErr != nil && ctx.Err() == nil {
			logger.WithError(consumeErr).Error("Gateway discovery consumer stopped")
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
		ticker := time.NewTicker(time.Duration(cfg.CatalogIntervalSeconds) * time.Second)
		defer ticker.Stop()
		for {
			refreshCtx, stop := context.WithTimeout(ctx, 2*time.Minute)
			resources, refreshErr := catalog.List(refreshCtx)
			stop()
			if refreshErr != nil {
				logger.WithError(refreshErr).Warn("Catalog refresh failed; preserving inventory")
			} else if persistErr := api.Store.Update(ctx, func(s *store.State, _ *sql.Tx) error { engine.Catalog(s, resources, time.Now().UTC()); return nil }); persistErr != nil {
				logger.WithError(persistErr).Error("Catalog persistence failed")
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
