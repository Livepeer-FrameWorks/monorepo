package handlers

import (
	"net/http"
	"net/url"
	"strings"

	"frameworks/api_balancing/internal/state"

	"github.com/gin-gonic/gin"
)

// livepeerAuthRequest is the body sent by go-livepeer's auth webhook.
// go-livepeer POSTs {"url": "<incomingRequestURL>"} on the first segment of a new session.
type livepeerAuthRequest struct {
	URL string `json:"url"`
}

// livepeerAuthResponse is what go-livepeer expects back.
// ManifestID is required — an empty value or non-200 status rejects the stream.
type livepeerAuthResponse struct {
	ManifestID string `json:"manifestID"`
}

// HandleLivepeerAuth handles the auth webhook from go-livepeer gateways.
// It validates that the manifestID in the push URL corresponds to an active stream
// in Foghorn's session registry.
//
// URL format: http://gateway:8935/live/<manifestID>/<segNum>.ts
func HandleLivepeerAuth(c *gin.Context) {
	var req livepeerAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.WithError(err).Warn("livepeer auth: invalid request body")
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	manifestID := extractManifestID(req.URL)
	if manifestID == "" {
		logger.WithField("url", req.URL).Warn("livepeer auth: could not extract manifestID from URL")
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid stream URL"})
		return
	}

	sm := state.DefaultManager()
	streamState := sm.GetStreamState(manifestID)
	if streamState == nil {
		logger.WithField("manifest_id", manifestID).Warn("livepeer auth: unknown stream rejected")
		c.JSON(http.StatusForbidden, gin.H{"error": "unknown stream"})
		return
	}

	logger.WithField("manifest_id", manifestID).Debug("livepeer auth: stream authorized")
	c.JSON(http.StatusOK, livepeerAuthResponse{ManifestID: manifestID})
}

// extractManifestID parses the manifestID from a go-livepeer push URL.
// Expected path: /live/<manifestID>/<segNum>.ts (or just /live/<manifestID>/...)
func extractManifestID(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	// Path: /live/<manifestID>/0.ts
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "live" {
		return ""
	}
	return parts[1]
}
