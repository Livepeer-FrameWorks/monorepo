package handlers

import (
	decklogclient "frameworks/pkg/clients/decklog"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

// Metrics holds Prometheus metrics for the handlers
type Metrics struct {
	WebhooksReceived     *prometheus.CounterVec
	EnrichmentCalls      *prometheus.CounterVec
	ChatwootAPICalls     *prometheus.CounterVec
	MessagesSent         *prometheus.CounterVec
	ConversationsCreated *prometheus.CounterVec
}

// Dependencies holds all external dependencies for handlers
type Dependencies struct {
	Logger          logging.Logger
	Metrics         *Metrics
	Quartermaster   *qmclient.GRPCClient
	Purser          *purserclient.GRPCClient
	Decklog         *decklogclient.BatchedClient
	Redis           *redis.Client
	ChatwootBaseURL string
	ChatwootToken   string
}

var deps Dependencies

// Init initializes the handlers with dependencies
func Init(d Dependencies) {
	deps = d
	deps.Logger.Info("Handlers initialized")
}
