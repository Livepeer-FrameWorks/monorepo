package handlers

import (
	"database/sql"

	"github.com/prometheus/client_golang/prometheus"

	"frameworks/api_billing/internal/mollie"
	decklogclient "frameworks/pkg/clients/decklog"
	periscopeclient "frameworks/pkg/clients/periscope"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
)

var (
	db              *sql.DB
	logger          logging.Logger
	emailService    *EmailService
	qmClient        *qmclient.GRPCClient
	mollieClient    *mollie.Client
	decklogClient   *decklogclient.BatchedClient
	periscopeClient *periscopeclient.GRPCClient
)

// PurserMetrics holds all Prometheus metrics for Purser
type PurserMetrics struct {
	BillingCalculations *prometheus.CounterVec
	UsageRecords        *prometheus.CounterVec
	InvoiceOperations   *prometheus.CounterVec
	DBQueries           *prometheus.CounterVec
	DBDuration          *prometheus.HistogramVec
	DBConnections       *prometheus.GaugeVec
}

// Init initializes the handlers with database, logger, and service clients
func Init(database *sql.DB, log logging.Logger, purserMetrics *PurserMetrics, quartermasterClient *qmclient.GRPCClient, mollieSvc *mollie.Client, decklogSvc *decklogclient.BatchedClient, periscopeSvc *periscopeclient.GRPCClient) {
	db = database
	logger = log
	emailService = NewEmailService(log)
	qmClient = quartermasterClient
	mollieClient = mollieSvc
	decklogClient = decklogSvc
	periscopeClient = periscopeSvc
}
