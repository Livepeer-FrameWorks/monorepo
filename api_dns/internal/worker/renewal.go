package worker

import (
	"context"
	"os"
	"strings"
	"time"

	"frameworks/api_dns/internal/store"
	"frameworks/pkg/logging"
)

// RenewalWorker handles background certificate renewal
type RenewalWorker struct {
	store       renewalStore
	certManager certIssuer
	logger      logging.Logger
	interval    time.Duration
	sleep       sleepFunc
}

type renewalStore interface {
	ListExpiringCertificates(ctx context.Context, threshold time.Duration) ([]store.Certificate, error)
}

type certIssuer interface {
	IssueCertificate(ctx context.Context, tenantID, domain, email string) (certPEM, keyPEM string, expiresAt time.Time, err error)
}

type sleepFunc func(ctx context.Context, duration time.Duration) error

func defaultSleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// NewRenewalWorker creates a new renewal worker
func NewRenewalWorker(s renewalStore, cm certIssuer, l logging.Logger) *RenewalWorker {
	return &RenewalWorker{
		store:       s,
		certManager: cm,
		logger:      l,
		interval:    24 * time.Hour, // Check daily
		sleep:       defaultSleep,
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
		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			_, _, _, err := w.certManager.IssueCertificate(ctx, tenantID, cert.Domain, email)
			if err == nil {
				lastErr = nil
				break
			}

			lastErr = err
			if !isRetryableACMEError(err) {
				break
			}

			backoff := time.Duration(30<<uint(attempt-1)) * time.Second
			if err := w.sleep(ctx, backoff); err != nil {
				log.WithError(err).Warn("Renewal interrupted")
				return
			}
		}

		if lastErr != nil {
			log.WithError(lastErr).Error("Failed to renew certificate")
			continue
		}
		log.Info("Certificate renewed successfully")
	}
}

func isRetryableACMEError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	retrySignals := []string{
		"timeout",
		"temporar",
		"rate limit",
		"429",
		"connection reset",
		"connection refused",
		"service unavailable",
		"server error",
	}
	for _, signal := range retrySignals {
		if strings.Contains(msg, signal) {
			return true
		}
	}
	return false
}
