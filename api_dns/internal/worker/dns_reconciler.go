package worker

import (
	"context"
	"time"

	"frameworks/api_dns/internal/logic"
	"frameworks/pkg/logging"
)

type DNSReconciler struct {
	dnsManager   *logic.DNSManager
	logger       logging.Logger
	interval     time.Duration
	serviceTypes []string
}

func NewDNSReconciler(dnsManager *logic.DNSManager, logger logging.Logger, interval time.Duration, serviceTypes []string) *DNSReconciler {
	return &DNSReconciler{
		dnsManager:   dnsManager,
		logger:       logger,
		interval:     interval,
		serviceTypes: serviceTypes,
	}
}

func (r *DNSReconciler) Start(ctx context.Context) {
	r.logger.Info("Starting DNS reconciliation worker")
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.reconcile(ctx)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("Stopping DNS reconciliation worker")
			return
		case <-ticker.C:
			r.reconcile(ctx)
		}
	}
}

func (r *DNSReconciler) reconcile(ctx context.Context) {
	for _, serviceType := range r.serviceTypes {
		partialErrors, err := r.dnsManager.SyncService(ctx, serviceType, "")
		if err != nil {
			r.logger.WithError(err).WithField("service_type", serviceType).Error("DNS reconciliation failed")
		}
		if len(partialErrors) > 0 {
			r.logger.WithField("service_type", serviceType).WithField("partial_errors", partialErrors).Warn("DNS reconciliation completed with partial errors")
		}
	}
}
