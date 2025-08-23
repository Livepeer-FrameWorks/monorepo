package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	qmapi "frameworks/pkg/api/quartermaster"
	"frameworks/pkg/models"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// INFRASTRUCTURE NODE MANAGEMENT
// ============================================================================

// GetNodes returns all nodes, optionally filtered by cluster
func GetNodes(c *gin.Context) {
	clusterID := c.Query("cluster_id")
	nodeType := c.Query("node_type")
	region := c.Query("region")

	query := `
		SELECT id, node_id, cluster_id, node_name, node_type, internal_ip,
		       external_ip, wireguard_ip, wireguard_public_key, region,
		       availability_zone, latitude, longitude, cpu_cores, memory_gb,
		       disk_gb, status, health_score, last_heartbeat, tags, metadata,
		       created_at, updated_at
		FROM infrastructure_nodes
		WHERE 1=1
	`
	args := []interface{}{}
	argCount := 0

	if clusterID != "" {
		argCount++
		query += " AND cluster_id = $" + strconv.Itoa(argCount)
		args = append(args, clusterID)
	}

	if nodeType != "" {
		argCount++
		query += " AND node_type = $" + strconv.Itoa(argCount)
		args = append(args, nodeType)
	}

	if region != "" {
		argCount++
		query += " AND region = $" + strconv.Itoa(argCount)
		args = append(args, region)
	}

	query += " ORDER BY cluster_id, node_name"

	rows, err := db.Query(query, args...)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch nodes")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch nodes"})
		return
	}
	defer rows.Close()

	var nodes []models.InfrastructureNode
	for rows.Next() {
		var node models.InfrastructureNode
		var tagsJSON, metadataJSON sql.NullString

		err := rows.Scan(
			&node.ID, &node.NodeID, &node.ClusterID, &node.NodeName, &node.NodeType,
			&node.InternalIP, &node.ExternalIP, &node.WireguardIP, &node.WireguardPublicKey,
			&node.Region, &node.AvailabilityZone, &node.Latitude, &node.Longitude,
			&node.CPUCores, &node.MemoryGB, &node.DiskGB, &node.Status, &node.HealthScore,
			&node.LastHeartbeat, &tagsJSON, &metadataJSON, &node.CreatedAt, &node.UpdatedAt,
		)
		if err != nil {
			logger.WithError(err).Error("Failed to scan node row")
			continue
		}

		// Parse JSON fields
		if tagsJSON.Valid {
			if err := json.Unmarshal([]byte(tagsJSON.String), &node.Tags); err != nil {
				logger.WithError(err).Warn("Failed to parse node tags JSON")
				node.Tags = make(map[string]interface{})
			}
		} else {
			node.Tags = make(map[string]interface{})
		}

		if metadataJSON.Valid {
			if err := json.Unmarshal([]byte(metadataJSON.String), &node.Metadata); err != nil {
				logger.WithError(err).Warn("Failed to parse node metadata JSON")
				node.Metadata = make(map[string]interface{})
			}
		} else {
			node.Metadata = make(map[string]interface{})
		}

		nodes = append(nodes, node)
	}

	c.JSON(http.StatusOK, qmapi.NodesResponse{
		Nodes: nodes,
		Count: len(nodes),
		Filters: qmapi.NodeFilters{
			ClusterID: clusterID,
			NodeType:  nodeType,
			Region:    region,
		},
	})
}

// GetNode returns a specific node by ID
func GetNode(c *gin.Context) {
	nodeID := c.Param("node_id")

	var node models.InfrastructureNode
	var tagsJSON, metadataJSON sql.NullString

	err := db.QueryRow(`
		SELECT id, node_id, cluster_id, node_name, node_type, internal_ip,
		       external_ip, wireguard_ip, wireguard_public_key, region,
		       availability_zone, latitude, longitude, cpu_cores, memory_gb,
		       disk_gb, status, health_score, last_heartbeat, tags, metadata,
		       created_at, updated_at
		FROM infrastructure_nodes
		WHERE node_id = $1
	`, nodeID).Scan(
		&node.ID, &node.NodeID, &node.ClusterID, &node.NodeName, &node.NodeType,
		&node.InternalIP, &node.ExternalIP, &node.WireguardIP, &node.WireguardPublicKey,
		&node.Region, &node.AvailabilityZone, &node.Latitude, &node.Longitude,
		&node.CPUCores, &node.MemoryGB, &node.DiskGB, &node.Status, &node.HealthScore,
		&node.LastHeartbeat, &tagsJSON, &metadataJSON, &node.CreatedAt, &node.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	if err != nil {
		logger.WithError(err).Error("Failed to fetch node")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch node"})
		return
	}

	// Parse JSON fields
	if tagsJSON.Valid {
		if err := json.Unmarshal([]byte(tagsJSON.String), &node.Tags); err != nil {
			logger.WithError(err).Warn("Failed to parse node tags JSON")
			node.Tags = make(map[string]interface{})
		}
	} else {
		node.Tags = make(map[string]interface{})
	}

	if metadataJSON.Valid {
		if err := json.Unmarshal([]byte(metadataJSON.String), &node.Metadata); err != nil {
			logger.WithError(err).Warn("Failed to parse node metadata JSON")
			node.Metadata = make(map[string]interface{})
		}
	} else {
		node.Metadata = make(map[string]interface{})
	}

	c.JSON(http.StatusOK, qmapi.NodeResponse{Node: node})
}

// CreateNode creates a new infrastructure node
func CreateNode(c *gin.Context) {
	var req struct {
		NodeID             string                 `json:"node_id" binding:"required"`
		ClusterID          string                 `json:"cluster_id" binding:"required"`
		NodeName           string                 `json:"node_name" binding:"required"`
		NodeType           string                 `json:"node_type" binding:"required"`
		InternalIP         *string                `json:"internal_ip,omitempty"`
		ExternalIP         *string                `json:"external_ip,omitempty"`
		WireguardIP        *string                `json:"wireguard_ip,omitempty"`
		WireguardPublicKey *string                `json:"wireguard_public_key,omitempty"`
		Region             *string                `json:"region,omitempty"`
		AvailabilityZone   *string                `json:"availability_zone,omitempty"`
		Latitude           *float64               `json:"latitude,omitempty"`
		Longitude          *float64               `json:"longitude,omitempty"`
		CPUCores           *int                   `json:"cpu_cores,omitempty"`
		MemoryGB           *int                   `json:"memory_gb,omitempty"`
		DiskGB             *int                   `json:"disk_gb,omitempty"`
		Tags               map[string]interface{} `json:"tags,omitempty"`
		Metadata           map[string]interface{} `json:"metadata,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Verify cluster exists
	var clusterExists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM infrastructure_clusters WHERE cluster_id = $1)", req.ClusterID).Scan(&clusterExists)
	if err != nil {
		logger.WithError(err).Error("Failed to check cluster existence")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to validate cluster"})
		return
	}

	if !clusterExists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster not found"})
		return
	}

	// Serialize JSON fields
	tagsJSON, _ := json.Marshal(req.Tags)
	metadataJSON, _ := json.Marshal(req.Metadata)

	var node models.InfrastructureNode
	err = db.QueryRow(`
		INSERT INTO infrastructure_nodes 
		(node_id, cluster_id, node_name, node_type, internal_ip, external_ip,
		 wireguard_ip, wireguard_public_key, region, availability_zone,
		 latitude, longitude, cpu_cores, memory_gb, disk_gb, tags, metadata,
		 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, NOW(), NOW())
		RETURNING id, created_at, updated_at
	`, req.NodeID, req.ClusterID, req.NodeName, req.NodeType, req.InternalIP, req.ExternalIP,
		req.WireguardIP, req.WireguardPublicKey, req.Region, req.AvailabilityZone,
		req.Latitude, req.Longitude, req.CPUCores, req.MemoryGB, req.DiskGB,
		string(tagsJSON), string(metadataJSON)).Scan(&node.ID, &node.CreatedAt, &node.UpdatedAt)

	if err != nil {
		logger.WithError(err).Error("Failed to create node")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create node"})
		return
	}

	// Populate response
	node.NodeID = req.NodeID
	node.ClusterID = req.ClusterID
	node.NodeName = req.NodeName
	node.NodeType = req.NodeType
	node.InternalIP = req.InternalIP
	node.ExternalIP = req.ExternalIP
	node.WireguardIP = req.WireguardIP
	node.WireguardPublicKey = req.WireguardPublicKey
	node.Region = req.Region
	node.AvailabilityZone = req.AvailabilityZone
	node.Latitude = req.Latitude
	node.Longitude = req.Longitude
	node.CPUCores = req.CPUCores
	node.MemoryGB = req.MemoryGB
	node.DiskGB = req.DiskGB
	node.Status = "active"
	node.HealthScore = 1.0
	node.Tags = req.Tags
	node.Metadata = req.Metadata

	if node.Tags == nil {
		node.Tags = make(map[string]interface{})
	}
	if node.Metadata == nil {
		node.Metadata = make(map[string]interface{})
	}

	logger.WithField("node_id", req.NodeID).Info("Created node successfully")
	c.JSON(http.StatusCreated, qmapi.NodeResponse{Node: node})
}

// UpdateNodeHealth updates the health status and metrics of a node
func UpdateNodeHealth(c *gin.Context) {
	nodeID := c.Param("node_id")

	var req struct {
		HealthScore *float64               `json:"health_score,omitempty"`
		Status      *string                `json:"status,omitempty"`
		CPUUsage    *float64               `json:"cpu_usage,omitempty"`
		MemoryUsage *float64               `json:"memory_usage,omitempty"`
		DiskUsage   *float64               `json:"disk_usage,omitempty"`
		Metadata    map[string]interface{} `json:"metadata,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Build dynamic update query
	query := "UPDATE infrastructure_nodes SET last_heartbeat = NOW(), updated_at = NOW()"
	args := []interface{}{}
	argCount := 0

	if req.HealthScore != nil {
		argCount++
		query += ", health_score = $" + strconv.Itoa(argCount)
		args = append(args, *req.HealthScore)
	}

	if req.Status != nil {
		argCount++
		query += ", status = $" + strconv.Itoa(argCount)
		args = append(args, *req.Status)
	}

	if req.Metadata != nil {
		argCount++
		metadataJSON, _ := json.Marshal(req.Metadata)
		query += ", metadata = $" + strconv.Itoa(argCount)
		args = append(args, string(metadataJSON))
	}

	argCount++
	query += " WHERE node_id = $" + strconv.Itoa(argCount)
	args = append(args, nodeID)

	result, err := db.Exec(query, args...)
	if err != nil {
		logger.WithError(err).Error("Failed to update node health")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update node health"})
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	c.JSON(http.StatusOK, qmapi.NodeHealthUpdateResponse{
		Message: "Node health updated successfully",
		NodeID:  nodeID,
	})
}
