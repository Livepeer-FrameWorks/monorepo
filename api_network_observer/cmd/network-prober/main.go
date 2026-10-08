package main

import (
	"context"
	"errors"
	"frameworks/api_network_observer/internal/appconfig"
	"frameworks/api_network_observer/internal/prober"
	"frameworks/api_network_observer/internal/providers/livepeer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetryspool"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if version.HandleCLI() {
		return
	}
	logger := logging.NewLoggerWithService("network-prober")
	config.LoadEnv(logger)
	cfg, err := config.Load[appconfig.Prober](config.Options{Service: "network-prober", Logger: logger})
	if err != nil {
		logger.WithError(err).Fatal("Invalid prober configuration")
	}
	cfg.ApplyLogLevel(logger)
	u, err := url.Parse(cfg.CoordinatorURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (!cfg.AllowInsecureCoordinator || u.Scheme != "http")) {
		logger.Fatal("Coordinator requires verified HTTPS; local HTTP needs explicit opt-in")
	}
	probe, err := livepeer.NewProbe(cfg.BroadcasterAddress, cfg.BroadcasterSignature, cfg.SkipLivepeerTLSVerify, time.Duration(cfg.TimeoutSeconds)*time.Second)
	if err != nil {
		logger.WithError(err).Fatal("Invalid discovery authentication")
	}
	queue, err := telemetryspool.Open(telemetryspool.Config{Directory: cfg.SpoolDirectory, MaxBytes: int64(cfg.SpoolMaxBytes)})
	if err != nil {
		logger.WithError(err).Fatal("Could not open durable spool")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	p := &prober.Prober{URL: strings.TrimRight(cfg.CoordinatorURL, "/"), Secret: cfg.SharedSecret, ID: cfg.ObserverID, Region: cfg.Region, Client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Queue: queue, Probe: probe, Concurrency: cfg.Concurrency, Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second}
	health := &http.Server{Addr: ":" + cfg.HealthPort, Handler: p.Health(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.WithError(serveErr).Error("Prober health listener failed")
			cancel()
		}
	}()
	defer func() {
		stop, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if shutdownErr := health.Shutdown(stop); shutdownErr != nil {
			logger.WithError(shutdownErr).Warn("Prober shutdown failed")
		}
	}()
	if err = p.Run(ctx, func(err error) {
		logger.WithError(err).Warn("Observation operation failed; pending batches remain on disk")
	}); err != nil && ctx.Err() == nil {
		logger.WithError(err).Fatal("Prober stopped")
	}
}
