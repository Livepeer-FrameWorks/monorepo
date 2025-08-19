package handlers

import (
	"database/sql"
	"net/http"

	"frameworks/pkg/logging"
	"frameworks/pkg/models"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

// GetClusters returns all clusters
func GetClusters(c *gin.Context) {
	rows, err := db.Query(`
		SELECT id, cluster_id, cluster_name, cluster_type, base_url, 
		       max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
		       current_stream_count, current_viewer_count, current_bandwidth_mbps,
		       is_active, health_status, created_at, updated_at
		FROM infrastructure_clusters
		ORDER BY cluster_name
	`)
	if err != nil {
		logger.WithError(err).Error("Failed to get clusters")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get clusters"})
		return
	}
	defer rows.Close()

	var clusters []models.InfrastructureCluster
	for rows.Next() {
		var cluster models.InfrastructureCluster
		err := rows.Scan(
			&cluster.ID, &cluster.ClusterID, &cluster.ClusterName, &cluster.ClusterType,
			&cluster.BaseURL, &cluster.MaxConcurrentStreams, &cluster.MaxConcurrentViewers,
			&cluster.MaxBandwidthMbps, &cluster.CurrentStreamCount, &cluster.CurrentViewerCount,
			&cluster.CurrentBandwidthMbps, &cluster.IsActive, &cluster.HealthStatus,
			&cluster.CreatedAt, &cluster.UpdatedAt,
		)
		if err != nil {
			logger.WithError(err).Error("Failed to scan cluster")
			continue
		}
		clusters = append(clusters, cluster)
	}

	c.JSON(http.StatusOK, gin.H{"clusters": clusters})
}

// GetCluster returns a specific cluster
func GetCluster(c *gin.Context) {
	clusterID := c.Param("id")

	var cluster models.InfrastructureCluster
	query := `
		SELECT id, cluster_id, cluster_name, cluster_type, base_url,
		       max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
		       current_stream_count, current_viewer_count, current_bandwidth_mbps,
		       is_active, health_status, created_at, updated_at
		FROM infrastructure_clusters
		WHERE cluster_id = $1
	`

	err := db.QueryRow(query, clusterID).Scan(
		&cluster.ID, &cluster.ClusterID, &cluster.ClusterName, &cluster.ClusterType,
		&cluster.BaseURL, &cluster.MaxConcurrentStreams, &cluster.MaxConcurrentViewers,
		&cluster.MaxBandwidthMbps, &cluster.CurrentStreamCount, &cluster.CurrentViewerCount,
		&cluster.CurrentBandwidthMbps, &cluster.IsActive, &cluster.HealthStatus,
		&cluster.CreatedAt, &cluster.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Cluster not found"})
		return
	} else if err != nil {
		logger.WithError(err).Error("Failed to get cluster")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get cluster"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"cluster": cluster})
}

// CreateCluster creates a new infrastructure cluster
func CreateCluster(c *gin.Context) {
	var req struct {
		ClusterID            string   `json:"cluster_id" binding:"required"`
		ClusterName          string   `json:"cluster_name" binding:"required"`
		ClusterType          string   `json:"cluster_type" binding:"required"`
		BaseURL              string   `json:"base_url" binding:"required"`
		DatabaseURL          *string  `json:"database_url,omitempty"`
		PeriscopeURL         *string  `json:"periscope_url,omitempty"`
		KafkaBrokers         []string `json:"kafka_brokers,omitempty"`
		MaxConcurrentStreams int      `json:"max_concurrent_streams"`
		MaxConcurrentViewers int      `json:"max_concurrent_viewers"`
		MaxBandwidthMbps     int      `json:"max_bandwidth_mbps"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		logger.WithError(err).Warn("Invalid create cluster request")
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Set defaults
	if req.MaxConcurrentStreams == 0 {
		req.MaxConcurrentStreams = 1000
	}
	if req.MaxConcurrentViewers == 0 {
		req.MaxConcurrentViewers = 100000
	}
	if req.MaxBandwidthMbps == 0 {
		req.MaxBandwidthMbps = 10000
	}

	query := `
		INSERT INTO infrastructure_clusters 
		(cluster_id, cluster_name, cluster_type, base_url, database_url, periscope_url, 
		 kafka_brokers, max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps, 
		 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
		RETURNING id, created_at, updated_at
	`

	var cluster models.InfrastructureCluster
	cluster.ClusterID = req.ClusterID
	cluster.ClusterName = req.ClusterName
	cluster.ClusterType = req.ClusterType
	cluster.BaseURL = req.BaseURL
	cluster.DatabaseURL = req.DatabaseURL
	cluster.PeriscopeURL = req.PeriscopeURL
	cluster.KafkaBrokers = req.KafkaBrokers
	cluster.MaxConcurrentStreams = req.MaxConcurrentStreams
	cluster.MaxConcurrentViewers = req.MaxConcurrentViewers
	cluster.MaxBandwidthMbps = req.MaxBandwidthMbps

	err := db.QueryRow(query, req.ClusterID, req.ClusterName, req.ClusterType, req.BaseURL,
		req.DatabaseURL, req.PeriscopeURL, pq.Array(req.KafkaBrokers),
		req.MaxConcurrentStreams, req.MaxConcurrentViewers, req.MaxBandwidthMbps).Scan(
		&cluster.ID, &cluster.CreatedAt, &cluster.UpdatedAt)

	if err != nil {
		logger.WithError(err).WithField("cluster_id", req.ClusterID).Error("Failed to create cluster")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create cluster"})
		return
	}

	logger.WithFields(logging.Fields{
		"cluster_id":   cluster.ClusterID,
		"cluster_name": req.ClusterName,
		"cluster_type": req.ClusterType,
	}).Info("Created cluster successfully")

	c.JSON(http.StatusCreated, cluster)
}

// UpdateCluster updates an existing infrastructure cluster
func UpdateCluster(c *gin.Context) {
	clusterID := c.Param("id")

	var req struct {
		ClusterName          *string  `json:"cluster_name,omitempty"`
		BaseURL              *string  `json:"base_url,omitempty"`
		DatabaseURL          *string  `json:"database_url,omitempty"`
		PeriscopeURL         *string  `json:"periscope_url,omitempty"`
		KafkaBrokers         []string `json:"kafka_brokers,omitempty"`
		MaxConcurrentStreams *int     `json:"max_concurrent_streams,omitempty"`
		MaxConcurrentViewers *int     `json:"max_concurrent_viewers,omitempty"`
		MaxBandwidthMbps     *int     `json:"max_bandwidth_mbps,omitempty"`
		CurrentStreamCount   *int     `json:"current_stream_count,omitempty"`
		CurrentViewerCount   *int     `json:"current_viewer_count,omitempty"`
		CurrentBandwidthMbps *int     `json:"current_bandwidth_mbps,omitempty"`
		HealthStatus         *string  `json:"health_status,omitempty"`
		IsActive             *bool    `json:"is_active,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		logger.WithError(err).Warn("Invalid update cluster request")
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Build dynamic update query
	setParts := []string{"updated_at = NOW()"}
	args := []interface{}{}
	argIndex := 1

	if req.ClusterName != nil {
		setParts = append(setParts, "cluster_name = $"+string(rune(argIndex+'0')))
		args = append(args, *req.ClusterName)
		argIndex++
	}

	if req.BaseURL != nil {
		setParts = append(setParts, "base_url = $"+string(rune(argIndex+'0')))
		args = append(args, *req.BaseURL)
		argIndex++
	}

	if req.DatabaseURL != nil {
		setParts = append(setParts, "database_url = $"+string(rune(argIndex+'0')))
		args = append(args, *req.DatabaseURL)
		argIndex++
	}

	if req.PeriscopeURL != nil {
		setParts = append(setParts, "periscope_url = $"+string(rune(argIndex+'0')))
		args = append(args, *req.PeriscopeURL)
		argIndex++
	}

	if len(req.KafkaBrokers) > 0 {
		setParts = append(setParts, "kafka_brokers = $"+string(rune(argIndex+'0')))
		args = append(args, pq.Array(req.KafkaBrokers))
		argIndex++
	}

	if req.MaxConcurrentStreams != nil {
		setParts = append(setParts, "max_concurrent_streams = $"+string(rune(argIndex+'0')))
		args = append(args, *req.MaxConcurrentStreams)
		argIndex++
	}

	if req.MaxConcurrentViewers != nil {
		setParts = append(setParts, "max_concurrent_viewers = $"+string(rune(argIndex+'0')))
		args = append(args, *req.MaxConcurrentViewers)
		argIndex++
	}

	if req.MaxBandwidthMbps != nil {
		setParts = append(setParts, "max_bandwidth_mbps = $"+string(rune(argIndex+'0')))
		args = append(args, *req.MaxBandwidthMbps)
		argIndex++
	}

	if req.CurrentStreamCount != nil {
		setParts = append(setParts, "current_stream_count = $"+string(rune(argIndex+'0')))
		args = append(args, *req.CurrentStreamCount)
		argIndex++
	}

	if req.CurrentViewerCount != nil {
		setParts = append(setParts, "current_viewer_count = $"+string(rune(argIndex+'0')))
		args = append(args, *req.CurrentViewerCount)
		argIndex++
	}

	if req.CurrentBandwidthMbps != nil {
		setParts = append(setParts, "current_bandwidth_mbps = $"+string(rune(argIndex+'0')))
		args = append(args, *req.CurrentBandwidthMbps)
		argIndex++
	}

	if req.HealthStatus != nil {
		setParts = append(setParts, "health_status = $"+string(rune(argIndex+'0')), "last_seen = NOW()")
		args = append(args, *req.HealthStatus)
		argIndex++
	}

	if req.IsActive != nil {
		setParts = append(setParts, "is_active = $"+string(rune(argIndex+'0')))
		args = append(args, *req.IsActive)
		argIndex++
	}

	// Add cluster ID as the last parameter
	args = append(args, clusterID)

	if len(setParts) == 1 { // Only updated_at
		c.JSON(http.StatusBadRequest, gin.H{"error": "No fields to update"})
		return
	}

	query := "UPDATE infrastructure_clusters SET " + setParts[0]
	for i := 1; i < len(setParts); i++ {
		query += ", " + setParts[i]
	}
	query += " WHERE cluster_id = $" + string(rune(argIndex+'0')) + " AND is_active = TRUE"

	result, err := db.Exec(query, args...)
	if err != nil {
		logger.WithError(err).WithField("cluster_id", clusterID).Error("Failed to update cluster")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update cluster"})
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		logger.WithField("cluster_id", clusterID).Warn("Cluster not found for update")
		c.JSON(http.StatusNotFound, gin.H{"error": "Cluster not found"})
		return
	}

	logger.WithField("cluster_id", clusterID).Info("Updated cluster successfully")
	c.JSON(http.StatusOK, gin.H{"message": "Cluster updated successfully"})
}
