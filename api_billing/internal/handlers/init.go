package handlers

import (
	"database/sql"

	"github.com/prometheus/client_golang/prometheus"

	"frameworks/api_billing/internal/mollie"
	billingstripe "frameworks/api_billing/internal/stripe"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// PurserMetrics holds all Prometheus metrics for Purser. DB connection-pool
// stats are registered separately via RegisterDBStats. invoice_operations_total
// was dropped because the invoice UPSERT branches on a status column rather
// than going through distinct create/finalize/void/reissue entry points.
type PurserMetrics struct {
	BillingCalculations      *prometheus.CounterVec
	UsageRecords             *prometheus.CounterVec
	UsageQuarantine          *prometheus.CounterVec // labels: usage_type, reason
	WebhookSignatureFailures *prometheus.CounterVec
}

// Service owns the dependencies for the Stripe/Mollie webhook and checkout
// flows. It replaces the package-level globals so the webhook handlers can be
// constructed and tested in isolation (no global mutation, parallel-safe).
type Service struct {
	db            *sql.DB
	logger        logging.Logger
	metrics       *PurserMetrics
	emailService  *EmailService
	qmClient      *qmclient.GRPCClient
	mollieClient  *mollie.Client
	stripeClient  *billingstripe.Client
	decklogClient *decklogclient.BatchedClient
}

// NewService builds the webhook/checkout handler service from its
// dependencies. emailService is derived from the logger, mirroring the prior
// Init() behavior.
func NewService(database *sql.DB, log logging.Logger, purserMetrics *PurserMetrics, quartermasterClient *qmclient.GRPCClient, mollieSvc *mollie.Client, stripeSvc *billingstripe.Client, decklogSvc *decklogclient.BatchedClient) *Service {
	return &Service{
		db:            database,
		logger:        log,
		metrics:       purserMetrics,
		emailService:  NewEmailService(log),
		qmClient:      quartermasterClient,
		mollieClient:  mollieSvc,
		stripeClient:  stripeSvc,
		decklogClient: decklogSvc,
	}
}
