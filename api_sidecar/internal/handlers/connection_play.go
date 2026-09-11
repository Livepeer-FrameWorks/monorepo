package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/gin-gonic/gin"
)

// HandleConnPlay forwards per-connection admission without issuing viewer
// sessions or recording source request URLs in analytics or the trigger WAL.
func HandleConnPlay(c *gin.Context) {
	incMistWebhook("CONN_PLAY", "received")
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10))
	if err != nil {
		incMistWebhook("CONN_PLAY", "read_error")
		c.String(http.StatusBadRequest, "invalid trigger payload")
		return
	}
	trigger, err := mist.ParseTriggerToProtobufWithHeaders(mist.TriggerConnPlay, body, c.Request.Header, getNodeID(), logger)
	if err != nil {
		incMistWebhook("CONN_PLAY", "parse_error")
		c.String(http.StatusBadRequest, "invalid trigger payload")
		return
	}
	// Viewer authorization remains USER_NEW's responsibility. CONN_PLAY also
	// fires for ordinary outputs, which must not acquire a second viewer slot.
	if !strings.EqualFold(trigger.GetConnectionPlay().GetConnector(), "DTSC") {
		c.String(http.StatusOK, "true")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	result, err := sendMistTrigger(ctx, trigger, logger)
	if err != nil || ctx.Err() != nil || result == nil {
		incMistWebhook("CONN_PLAY", "forward_error")
		c.String(http.StatusServiceUnavailable, "source admission unavailable")
		return
	}
	// Only an explicit boolean allow can admit a connection. A rewrite URL,
	// empty response, or KEEP action is not source-connection authorization.
	allowed := !result.Abort && result.ErrorCode == ipcpb.IngestErrorCode_INGEST_ERROR_NONE &&
		(result.Action == ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_UNSPECIFIED || result.Action == ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE) &&
		strings.TrimSpace(result.Response) == "true"
	if !allowed {
		incMistWebhook("CONN_PLAY", "denied")
		respondMistAction(c, http.StatusOK, ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_DENY, "source_admission_denied", "")
		return
	}
	incMistWebhook("CONN_PLAY", "allowed")
	c.String(http.StatusOK, "true")
}
