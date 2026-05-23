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

var (
	db            *sql.DB
	logger        logging.Logger
	emailService  *EmailService
	qmClient      *qmclient.GRPCClient
	mollieClient  *mollie.Client
	stripeClient  *billingstripe.Client
	decklogClient *decklogclient.BatchedClient
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

var metrics *PurserMetrics

// Init initializes the handlers with database, logger, and service clients
func Init(database *sql.DB, log logging.Logger, purserMetrics *PurserMetrics, quartermasterClient *qmclient.GRPCClient, mollieSvc *mollie.Client, stripeSvc *billingstripe.Client, decklogSvc *decklogclient.BatchedClient) {
	db = database
	logger = log
	metrics = purserMetrics
	emailService = NewEmailService(log)
	qmClient = quartermasterClient
	mollieClient = mollieSvc
	stripeClient = stripeSvc
	decklogClient = decklogSvc
}
