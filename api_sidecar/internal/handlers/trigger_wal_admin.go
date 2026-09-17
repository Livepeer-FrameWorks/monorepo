package handlers

import (
	"net/http"

	"frameworks/api_sidecar/internal/control"

	"github.com/gin-gonic/gin"
)

const triggerWALInspectionLimit = 100

// HandleTriggerWALStatus returns the durable trigger WAL state: how many
// entries are awaiting Foghorn's MistTriggerAck and what they look like.
// Used for incident response — see docs/architecture/trigger-durability.md.
//
// Response: {"pending_depth": N, "entries": [{...}, ...]}.
// Mounted on the internal listener so no auth is enforced beyond the
// usual network boundary that protects the rest of the sidecar's
// admin surface.
func HandleTriggerWALStatus(c *gin.Context) {
	depth, err := control.TriggerWALPendingDepth()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	pending, err := control.ListTriggerWALPending(triggerWALInspectionLimit)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	entries := make([]gin.H, 0, len(pending))
	for _, trigger := range pending {
		entry := gin.H{}
		for key, value := range control.TriggerSummaryFields(trigger, trigger.GetRequestId()) {
			entry[key] = value
		}
		entries = append(entries, entry)
	}
	c.JSON(http.StatusOK, gin.H{
		"pending_depth":     depth,
		"entries":           entries,
		"entries_returned":  len(entries),
		"entries_truncated": depth > len(entries),
	})
}

// HandleTriggerWALReplay nudges the forwarder to drain pending entries
// immediately instead of waiting for the next tick. Safe to call any
// time — the forwarder dedupes in-flight sends via its ack-pending map,
// so calling replay multiple times in a row has no negative effect.
func HandleTriggerWALReplay(c *gin.Context) {
	if err := control.KickTriggerForwarder(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "drain_kicked"})
}
