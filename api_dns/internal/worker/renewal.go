package worker

import (
	"context"
	"os"
	"time"

	"frameworks/api_dns/internal/logic"
	"frameworks/api_dns/internal/store"
	"frameworks/pkg/logging"
)

// RenewalWorker handles background certificate renewal
type RenewalWorker struct {
	store       *store.Store
	certManager *logic.CertManager
	logger      logging.Logger
	interval    time.Duration
}

// NewRenewalWorker creates a new renewal worker
func NewRenewalWorker(s *store.Store, cm *logic.CertManager, l logging.Logger) *RenewalWorker {
	return &RenewalWorker{
		store:       s,
		certManager: cm,
		logger:      l,
		interval:    24 * time.Hour, // Check daily
	}
}

// Start starts the renewal loop
func (w *RenewalWorker) Start(ctx context.Context) {
	w.logger.Info("Starting certificate renewal worker")
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	// Run immediately on start
	w.renewCertificates(ctx)

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("Stopping renewal worker")
			return
		case <-ticker.C:
			w.renewCertificates(ctx)
		}
	}
}

func (w *RenewalWorker) renewCertificates(ctx context.Context) {
	// Renew certs expiring in the next 30 days
	threshold := 30 * 24 * time.Hour
	certs, err := w.store.ListExpiringCertificates(ctx, threshold)
	if err != nil {
		w.logger.WithError(err).Error("Failed to list expiring certificates")
		return
	}

	if len(certs) == 0 {
		w.logger.Debug("No certificates need renewal")
		return
	}

	w.logger.WithField("count", len(certs)).Info("Found certificates expiring soon")

	for _, cert := range certs {
		log := w.logger.WithField("domain", cert.Domain)
		log.Info("Renewing certificate")

		// Use contact email for ACME registration
		email := os.Getenv("BRAND_CONTACT_EMAIL")
		if email == "" {
			email = "info@frameworks.network"
		}

		// Attempt renewal (IssueCertificate handles cache checking and ACME logic)
		_, _, err := w.certManager.IssueCertificate(ctx, cert.Domain, email)
		if err != nil {
			log.WithError(err).Error("Failed to renew certificate")
			continue
		}
		log.Info("Certificate renewed successfully")
	}
}
