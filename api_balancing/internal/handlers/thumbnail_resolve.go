package handlers

import (
	"net/http"
	"strings"

	"frameworks/api_balancing/internal/control"

	"github.com/gin-gonic/gin"
)

// HandleResolveActiveThumbnailVersion is the in-cell thumbnail resolver an authenticated, co-located Chandler
// calls on a cold cache miss: asset_key -> serving decision. Thumbnail reads are public and asset_key is globally
// unique (stream_id / artifact_hash), so this is keyed by asset_key alone. The active pointer lives in the cell's
// shared Postgres, so ANY Foghorn instance in the HA pool answers consistently. It returns a TRI-STATE:
//   - {"state":"active","activeVersion":"<v>"} — serve the versioned object.
//   - {"state":"legacy","activeVersion":""}    — no version yet; serve the legacy un-versioned object.
//   - {"state":"gone"}                          — the parent artifact is terminal; the asset is GONE, Chandler
//     must evict any cached mapping and 404 rather than serve a surviving legacy object.
func HandleResolveActiveThumbnailVersion(c *gin.Context) {
	assetKey := strings.TrimSpace(c.Query("assetKey"))
	if assetKey == "" || strings.Contains(assetKey, "/") || strings.Contains(assetKey, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid assetKey"})
		return
	}
	version, state, err := control.ResolveThumbnailForServing(c.Request.Context(), db, assetKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "resolve failed"})
		return
	}
	switch state {
	case control.ThumbnailGone:
		c.JSON(http.StatusOK, gin.H{"state": "gone"})
	case control.ThumbnailActive:
		c.JSON(http.StatusOK, gin.H{"state": "active", "activeVersion": version})
	default:
		c.JSON(http.StatusOK, gin.H{"state": "legacy", "activeVersion": ""})
	}
}
