// Package httpapi serves Lookout's HTTP ingestion endpoint.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"frameworks/api_incidents/internal/incidents"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/gin-gonic/gin"
)

// maxWebhookBytes bounds one Alertmanager notification; large groups carry
// every alert with its labels and annotations.
const maxWebhookBytes = 8 << 20

// Ingester applies an Alertmanager notification.
type Ingester interface {
	IngestAlertmanager(ctx context.Context, hook incidents.AlertmanagerWebhook) (incidents.IngestResult, error)
}

// AlertmanagerHandler handles POST /v1/alertmanager. The token is read per
// request so a reloaded secret takes effect immediately; an empty token
// rejects every request. Storage failures return 5xx so Alertmanager retries;
// malformed notifications return 4xx, which Alertmanager does not retry.
func AlertmanagerHandler(ingester Ingester, token func() string, logger logging.Logger, metrics *incidents.Metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		expected := token()
		presented, hasBearer := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if expected == "" || !hasBearer || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(presented)), []byte(expected)) != 1 {
			metrics.ObserveWebhook("unauthorized")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		var hook incidents.AlertmanagerWebhook
		if err := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBytes)).Decode(&hook); err != nil {
			metrics.ObserveWebhook("invalid")
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid alertmanager payload"})
			return
		}

		result, err := ingester.IngestAlertmanager(c.Request.Context(), hook)
		if errors.Is(err, incidents.ErrInvalidWebhook) {
			metrics.ObserveWebhook("invalid")
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err != nil {
			metrics.ObserveWebhook("error")
			if logger != nil {
				logger.WithError(err).WithField("group_key", hook.GroupKey).Error("Failed to ingest Alertmanager notification")
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "ingestion failed"})
			return
		}
		metrics.ObserveWebhook(result.Outcome)
		c.JSON(http.StatusOK, gin.H{"outcome": result.Outcome, "incident_id": result.IncidentID})
	}
}
