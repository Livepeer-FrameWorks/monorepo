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
		// Extract tenant context from the certificate
		tenantID := ""
		if cert.TenantID.Valid {
			tenantID = cert.TenantID.String
		}

		log := w.logger.WithField("domain", cert.Domain)
		if tenantID != "" {
			log = log.WithField("tenant_id", tenantID)
		}
		log.Info("Renewing certificate")

		// Use contact email for ACME registration
		// For tenant-specific certificates, we could look up tenant contact email from Quartermaster
		// For now, use the platform default
		email := os.Getenv("BRAND_CONTACT_EMAIL")
		if email == "" {
			email = "info@frameworks.network"
		}

		// Attempt renewal with tenant context
		_, _, _, err := w.certManager.IssueCertificate(ctx, tenantID, cert.Domain, email)
		if err != nil {
			log.WithError(err).Error("Failed to renew certificate")
			continue
		}
		log.Info("Certificate renewed successfully")
	}
}
