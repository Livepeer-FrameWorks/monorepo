package worker

import (
	"context"
	"errors"
	"strings"
	"time"

	"frameworks/api_dns/internal/store"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// RenewalWorker handles background certificate renewal
type RenewalWorker struct {
	store       renewalStore
	certManager certIssuer
	logger      logging.Logger
	interval    time.Duration
	sleep       sleepFunc
	acmeEmail   string
	rootDomain  string
}

type renewalStore interface {
	ListExpiringCertificates(ctx context.Context, threshold time.Duration) ([]store.Certificate, error)
	ListExpiringTLSBundles(ctx context.Context, threshold time.Duration) ([]store.TLSBundle, error)
	GetTenantAlias(ctx context.Context, tenantID string) (*store.TenantAlias, error)
	GetTenantCustomDomain(ctx context.Context, tenantID, domain string) (*store.TenantCustomDomain, error)
	DeleteExpiredCertificateIssuanceLeases(ctx context.Context) (int64, error)
}

type certIssuer interface {
	RenewCertificate(ctx context.Context, tenantID, domain, email string) (certPEM, keyPEM string, expiresAt time.Time, err error)
	EnsureTLSBundle(ctx context.Context, bundleID string, domains []string, email string) (*store.TLSBundle, error)
	EnsureTenantWildcardCertificate(ctx context.Context, tenantID, subdomain, tenantZone, rootDomain, email string) (*store.TLSBundle, error)
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
func NewRenewalWorker(s renewalStore, cm certIssuer, l logging.Logger, rootDomain, acmeEmail string) *RenewalWorker {
	return &RenewalWorker{
		store:       s,
		certManager: cm,
		logger:      l,
		interval:    24 * time.Hour, // Check daily
		sleep:       defaultSleep,
		acmeEmail:   strings.TrimSpace(acmeEmail),
		rootDomain:  strings.Trim(strings.ToLower(strings.TrimSpace(rootDomain)), "."),
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
	if _, err := w.store.DeleteExpiredCertificateIssuanceLeases(ctx); err != nil {
		w.logger.WithError(err).Warn("Failed to clean expired certificate issuance leases")
	}
	// Renew certs expiring in the next 30 days
	threshold := 30 * 24 * time.Hour
	certs, err := w.store.ListExpiringCertificates(ctx, threshold)
	if err != nil {
		w.logger.WithError(err).Error("Failed to list expiring certificates")
		return
	}

	if len(certs) == 0 {
		w.logger.Debug("No certificates need renewal")
	} else {
		w.logger.WithField("count", len(certs)).Info("Found certificates expiring soon")
	}

	for _, cert := range certs {
		// Extract tenant context from the certificate
		tenantID := ""
		if cert.TenantID.Valid {
			tenantID = cert.TenantID.String
		}

		log := w.logger.WithField("domain", cert.Domain)
		if tenantID != "" {
			log = log.WithField("tenant_id", tenantID)
			authorized, authorityErr := w.tenantCertificateAuthorized(ctx, tenantID, cert.Domain)
			if authorityErr != nil {
				log.WithError(authorityErr).Warn("Skipping certificate renewal because tenant authority could not be read")
				continue
			}
			if !authorized {
				log.Warn("Skipping certificate renewal without active tenant authority")
				continue
			}
		}
		log.Info("Renewing certificate")

		email := w.acmeEmail

		// Attempt renewal with tenant context
		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			_, _, _, issueErr := w.certManager.RenewCertificate(ctx, tenantID, cert.Domain, email)
			if issueErr == nil {
				lastErr = nil
				break
			}

			lastErr = issueErr
			if !isRetryableACMEError(issueErr) {
				break
			}

			backoff := time.Duration(30<<uint(attempt-1)) * time.Second
			if sleepErr := w.sleep(ctx, backoff); sleepErr != nil {
				log.WithError(sleepErr).Warn("Renewal interrupted")
				return
			}
		}

		if lastErr != nil {
			if errors.Is(lastErr, store.ErrIssuanceInProgress) {
				log.Info("Skipped certificate renewal because another Navigator owns the issuance lease")
				continue
			}
			if errors.Is(lastErr, store.ErrAuthorityLost) {
				log.Info("Skipped certificate renewal because tenant authority changed")
				continue
			}
			log.WithError(lastErr).Error("Failed to renew certificate")
			continue
		}
		log.Info("Certificate renewed successfully")
	}

	bundles, err := w.store.ListExpiringTLSBundles(ctx, threshold)
	if err != nil {
		w.logger.WithError(err).Error("Failed to list expiring tls bundles")
		return
	}

	if len(bundles) == 0 {
		return
	}

	w.logger.WithField("count", len(bundles)).Info("Found tls bundles expiring soon")
	for _, bundle := range bundles {
		log := w.logger.WithField("bundle_id", bundle.BundleID).WithField("domains", bundle.Domains)
		var tenantAlias *store.TenantAlias
		if tenantID, tenantBundle := strings.CutPrefix(bundle.BundleID, "tenant:"); tenantBundle {
			alias, aliasErr := w.store.GetTenantAlias(ctx, tenantID)
			if aliasErr != nil {
				if !errors.Is(aliasErr, store.ErrNotFound) {
					log.WithError(aliasErr).Warn("Skipping TLS bundle renewal because tenant authority could not be read")
				} else {
					log.Warn("Skipping TLS bundle renewal without tenant alias authority")
				}
				continue
			}
			if alias.Status != "cert_issued" {
				log.WithField("alias_status", alias.Status).Warn("Skipping TLS bundle renewal without issued tenant alias authority")
				continue
			}
			tenantAlias = alias
		}
		email := w.acmeEmail

		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			var err error
			if tenantAlias != nil {
				_, err = w.certManager.EnsureTenantWildcardCertificate(
					ctx, tenantAlias.TenantID, tenantAlias.Subdomain, pkgdns.TenantAliasZoneLabel, w.rootDomain, email,
				)
			} else {
				_, err = w.certManager.EnsureTLSBundle(ctx, bundle.BundleID, bundle.Domains, email)
			}
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
				log.WithError(err).Warn("TLS bundle renewal interrupted")
				return
			}
		}
		if lastErr != nil {
			if errors.Is(lastErr, store.ErrIssuanceInProgress) {
				log.Info("Skipped TLS bundle renewal because another Navigator owns the issuance lease")
				continue
			}
			if errors.Is(lastErr, store.ErrAuthorityLost) {
				log.Info("Skipped TLS bundle renewal because tenant authority changed")
				continue
			}
			log.WithError(lastErr).Error("Failed to renew tls bundle")
			continue
		}
		log.Info("TLS bundle renewed successfully")
	}
}

func (w *RenewalWorker) tenantCertificateAuthorized(ctx context.Context, tenantID, domain string) (bool, error) {
	customDomain, customDomainErr := w.store.GetTenantCustomDomain(ctx, tenantID, domain)
	if customDomainErr != nil {
		if errors.Is(customDomainErr, store.ErrNotFound) {
			return false, nil
		}
		return false, customDomainErr
	}
	return store.CustomDomainHasCertificateAuthority(customDomain.Status), nil
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
