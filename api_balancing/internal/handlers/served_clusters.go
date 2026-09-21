package handlers

import (
	"net/http"

	"frameworks/api_balancing/internal/appconfig"
	"frameworks/api_balancing/internal/control"

	"github.com/gin-gonic/gin"
)

// HandleServedClusters exposes the set of cluster IDs this Foghorn instance serves.
// Route: GET /debug/served-clusters
func HandleServedClusters(c *gin.Context) {
	instanceID := appconfig.Current().InstanceID
	clusters := control.ServedClustersSnapshot()

	c.JSON(http.StatusOK, gin.H{
		"instance_id":      instanceID,
		"local_cluster_id": control.GetLocalClusterID(),
		"served_clusters":  clusters,
		"count":            len(clusters),
	})
}
