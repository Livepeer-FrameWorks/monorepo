package handlers

import (
	"database/sql"

	"github.com/prometheus/client_golang/prometheus"

	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
)

var (
	db           *sql.DB
	logger       logging.Logger
	emailService *EmailService
	metrics      *PurserMetrics
	qmClient     *qmclient.GRPCClient
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
func Init(database *sql.DB, log logging.Logger, purserMetrics *PurserMetrics, quartermasterClient *qmclient.GRPCClient) {
	db = database
	logger = log
	emailService = NewEmailService(log)
	metrics = purserMetrics
	qmClient = quartermasterClient
}
