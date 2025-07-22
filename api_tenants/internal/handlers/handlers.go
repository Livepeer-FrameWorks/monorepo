package handlers

import (
	"database/sql"
	"net/http"

	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/models"

	"github.com/lib/pq"
)

var (
	db     *sql.DB
	logger logging.Logger
)

// Init initializes the handlers with database and logger
func Init(database *sql.DB, log logging.Logger) {
	db = database
	logger = log
}

// ValidateTenant validates a tenant and returns its features/limits
func ValidateTenant(c middleware.Context) {
	var req models.ValidateTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, middleware.H{"error": err.Error()})
		return
	}

	var resp models.ValidateTenantResponse
	query := `
		SELECT name, is_active
		FROM tenants 
		WHERE id = $1
	`

	err := db.QueryRow(query, req.TenantID).Scan(
		&resp.Name, &resp.IsActive,
	)

	if err == sql.ErrNoRows {
		resp.Valid = false
		resp.Error = "Tenant not found"
		c.JSON(http.StatusOK, resp)
		return
	}

	if err != nil {
		logger.WithError(err).WithField("tenant_id", req.TenantID).Error("Failed to validate tenant")
		resp.Valid = false
		resp.Error = "Internal server error"
		c.JSON(http.StatusInternalServerError, resp)
		return
	}

	// Basic validation - tenant exists and is active
	resp.Valid = resp.IsActive

	c.JSON(http.StatusOK, resp)
}

// ResolveTenant resolves a tenant ID from subdomain/domain
func ResolveTenant(c middleware.Context) {
	var req models.ResolveTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, middleware.H{"error": err.Error()})
		return
	}

	var resp models.ResolveTenantResponse
	var query string
	var param string

	if req.Subdomain != "" {
		query = `SELECT id, name FROM tenants WHERE subdomain = $1 AND is_active = true`
		param = req.Subdomain
	} else if req.Domain != "" {
		query = `SELECT id, name FROM tenants WHERE custom_domain = $1 AND is_active = true`
		param = req.Domain
	} else {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Either subdomain or domain required"})
		return
	}

	err := db.QueryRow(query, param).Scan(&resp.TenantID, &resp.Name)
	if err == sql.ErrNoRows {
		resp.Error = "Tenant not found"
		c.JSON(http.StatusOK, resp)
		return
	}

	if err != nil {
		logger.WithError(err).Error("Failed to resolve tenant")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// GetClusterRouting returns the best cluster for a tenant's stream
func GetClusterRouting(c middleware.Context) {
	var req models.GetClusterRoutingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, middleware.H{"error": err.Error()})
		return
	}

	// Get tenant's primary cluster and deployment tier
	var primaryClusterID, deploymentTier string
	err := db.QueryRow(`
		SELECT primary_cluster_id, deployment_tier 
		FROM tenants 
		WHERE id = $1 AND is_active = true
	`, req.TenantID).Scan(&primaryClusterID, &deploymentTier)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, middleware.H{"error": "Tenant not found"})
		return
	}

	if err != nil {
		logger.WithError(err).Error("Failed to get tenant cluster info")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Internal server error"})
		return
	}

	// Get cluster info with capacity checks
	query := `
		SELECT 
			c.cluster_id,
			c.cluster_type,
			c.base_url,
			c.kafka_brokers,
			COALESCE(tca.kafka_topic_prefix, t.kafka_topic_prefix) as topic_prefix,
			c.max_streams,
			c.current_stream_count,
			c.health_status
		FROM infrastructure_clusters c
		JOIN tenants t ON t.id = $1
		LEFT JOIN tenant_cluster_assignments tca ON tca.tenant_id = t.id AND tca.cluster_id = c.cluster_id
		WHERE c.cluster_id = $2
		  AND c.is_active = true
		  AND (
			c.max_streams = 0 OR 
			c.current_stream_count < c.max_streams
		  )
		  AND (
			$3 = 0 OR 
			c.max_bandwidth_mbps = 0 OR 
			c.current_bandwidth_mbps + $3 <= c.max_bandwidth_mbps
		  )
	`

	var routing models.ClusterRouting
	err = db.QueryRow(query, req.TenantID, primaryClusterID, req.EstimatedMbps).Scan(
		&routing.ClusterID,
		&routing.ClusterType,
		&routing.BaseURL,
		pq.Array(&routing.KafkaBrokers),
		&routing.TopicPrefix,
		&routing.MaxStreams,
		&routing.CurrentStreams,
		&routing.HealthStatus,
	)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, middleware.H{"error": "No suitable cluster found"})
		return
	}

	if err != nil {
		logger.WithError(err).Error("Failed to get cluster routing")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusOK, middleware.H{"routing": routing})
}

// GetTenant retrieves a tenant by ID
func GetTenant(c middleware.Context) {
	tenantID := c.Param("id")

	var tenant models.Tenant
	query := `
		SELECT id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
		       deployment_tier, deployment_model, primary_deployment_tier, allowed_deployment_tiers,
		       primary_cluster_id, kafka_topic_prefix, kafka_brokers, database_url,
		       is_active, created_at, updated_at
		FROM tenants
		WHERE id = $1 AND is_active = TRUE
	`

	err := db.QueryRow(query, tenantID).Scan(
		&tenant.ID, &tenant.Name, &tenant.Subdomain, &tenant.CustomDomain,
		&tenant.LogoURL, &tenant.PrimaryColor, &tenant.SecondaryColor,
		&tenant.DeploymentTier, &tenant.DeploymentModel, &tenant.PrimaryDeploymentTier,
		pq.Array(&tenant.AllowedDeploymentTiers), &tenant.PrimaryClusterID,
		&tenant.KafkaTopicPrefix, pq.Array(&tenant.KafkaBrokers), &tenant.DatabaseURL,
		&tenant.IsActive, &tenant.CreatedAt, &tenant.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		logger.WithField("tenant_id", tenantID).Warn("Tenant not found")
		c.JSON(http.StatusNotFound, middleware.H{"error": "Tenant not found"})
		return
	}

	if err != nil {
		logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to get tenant")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Internal server error"})
		return
	}

	logger.WithField("tenant_id", tenantID).Debug("Retrieved tenant successfully")
	c.JSON(http.StatusOK, tenant)
}

// CreateTenant creates a new tenant
func CreateTenant(c middleware.Context) {
	var req models.CreateTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.WithError(err).Warn("Invalid create tenant request")
		c.JSON(http.StatusBadRequest, middleware.H{"error": err.Error()})
		return
	}

	// Set defaults
	if req.DeploymentModel == "" {
		req.DeploymentModel = "shared"
	}
	if req.PrimaryDeploymentTier == "" {
		req.PrimaryDeploymentTier = "global"
	}
	if len(req.AllowedDeploymentTiers) == 0 {
		req.AllowedDeploymentTiers = []string{"global"}
	}

	query := `
		INSERT INTO tenants (name, subdomain, custom_domain, deployment_tier, deployment_model, 
		                    primary_deployment_tier, allowed_deployment_tiers, primary_color, secondary_color, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
		RETURNING id, created_at, updated_at
	`

	var tenant models.Tenant
	tenant.Name = req.Name
	tenant.Subdomain = req.Subdomain
	tenant.CustomDomain = req.CustomDomain
	tenant.DeploymentTier = req.DeploymentTier
	tenant.DeploymentModel = req.DeploymentModel
	tenant.PrimaryDeploymentTier = req.PrimaryDeploymentTier
	tenant.AllowedDeploymentTiers = req.AllowedDeploymentTiers
	tenant.PrimaryColor = req.PrimaryColor
	tenant.SecondaryColor = req.SecondaryColor
	tenant.IsActive = true

	err := db.QueryRow(query, tenant.Name, tenant.Subdomain, tenant.CustomDomain,
		tenant.DeploymentTier, tenant.DeploymentModel, tenant.PrimaryDeploymentTier,
		pq.Array(tenant.AllowedDeploymentTiers), tenant.PrimaryColor, tenant.SecondaryColor).Scan(
		&tenant.ID, &tenant.CreatedAt, &tenant.UpdatedAt,
	)

	if err != nil {
		logger.WithError(err).WithField("tenant_name", req.Name).Error("Failed to create tenant")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to create tenant"})
		return
	}

	logger.WithFields(logging.Fields{
		"tenant_id":   tenant.ID,
		"tenant_name": req.Name,
	}).Info("Created tenant successfully")

	c.JSON(http.StatusCreated, tenant)
}

// UpdateTenant updates an existing tenant
func UpdateTenant(c middleware.Context) {
	tenantID := c.Param("id")

	var req models.UpdateTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.WithError(err).Warn("Invalid update tenant request")
		c.JSON(http.StatusBadRequest, middleware.H{"error": err.Error()})
		return
	}

	// Build dynamic update query
	setParts := []string{"updated_at = NOW()"}
	args := []interface{}{}
	argIndex := 1

	if req.Name != nil {
		setParts = append(setParts, "name = $"+string(rune(argIndex+'0')))
		args = append(args, *req.Name)
		argIndex++
	}

	if req.Subdomain != nil {
		setParts = append(setParts, "subdomain = $"+string(rune(argIndex+'0')))
		args = append(args, *req.Subdomain)
		argIndex++
	}

	if req.CustomDomain != nil {
		setParts = append(setParts, "custom_domain = $"+string(rune(argIndex+'0')))
		args = append(args, *req.CustomDomain)
		argIndex++
	}

	if req.LogoURL != nil {
		setParts = append(setParts, "logo_url = $"+string(rune(argIndex+'0')))
		args = append(args, *req.LogoURL)
		argIndex++
	}

	if req.PrimaryColor != nil {
		setParts = append(setParts, "primary_color = $"+string(rune(argIndex+'0')))
		args = append(args, *req.PrimaryColor)
		argIndex++
	}

	if req.SecondaryColor != nil {
		setParts = append(setParts, "secondary_color = $"+string(rune(argIndex+'0')))
		args = append(args, *req.SecondaryColor)
		argIndex++
	}

	if req.DeploymentModel != nil {
		setParts = append(setParts, "deployment_model = $"+string(rune(argIndex+'0')))
		args = append(args, *req.DeploymentModel)
		argIndex++
	}

	if req.PrimaryDeploymentTier != nil {
		setParts = append(setParts, "primary_deployment_tier = $"+string(rune(argIndex+'0')))
		args = append(args, *req.PrimaryDeploymentTier)
		argIndex++
	}

	if req.AllowedDeploymentTiers != nil {
		setParts = append(setParts, "allowed_deployment_tiers = $"+string(rune(argIndex+'0')))
		args = append(args, pq.Array(req.AllowedDeploymentTiers))
		argIndex++
	}

	if req.PrimaryClusterID != nil {
		setParts = append(setParts, "primary_cluster_id = $"+string(rune(argIndex+'0')))
		args = append(args, *req.PrimaryClusterID)
		argIndex++
	}

	if req.IsActive != nil {
		setParts = append(setParts, "is_active = $"+string(rune(argIndex+'0')))
		args = append(args, *req.IsActive)
		argIndex++
	}

	// Add tenant ID as the last parameter
	args = append(args, tenantID)

	if len(setParts) == 1 { // Only updated_at
		c.JSON(http.StatusBadRequest, middleware.H{"error": "No fields to update"})
		return
	}

	query := "UPDATE tenants SET " + setParts[0]
	for i := 1; i < len(setParts); i++ {
		query += ", " + setParts[i]
	}
	query += " WHERE id = $" + string(rune(argIndex+'0')) + " AND is_active = TRUE"

	result, err := db.Exec(query, args...)
	if err != nil {
		logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to update tenant")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to update tenant"})
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		logger.WithField("tenant_id", tenantID).Warn("Tenant not found for update")
		c.JSON(http.StatusNotFound, middleware.H{"error": "Tenant not found"})
		return
	}

	logger.WithField("tenant_id", tenantID).Info("Updated tenant successfully")
	c.JSON(http.StatusOK, middleware.H{"message": "Tenant updated successfully"})
}

// DeleteTenant soft deletes a tenant
func DeleteTenant(c middleware.Context) {
	tenantID := c.Param("id")

	query := `UPDATE tenants SET is_active = FALSE, updated_at = NOW() WHERE id = $1 AND is_active = TRUE`

	result, err := db.Exec(query, tenantID)
	if err != nil {
		logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to delete tenant")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to delete tenant"})
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		logger.WithField("tenant_id", tenantID).Warn("Tenant not found for deletion")
		c.JSON(http.StatusNotFound, middleware.H{"error": "Tenant not found"})
		return
	}

	logger.WithField("tenant_id", tenantID).Info("Deleted tenant successfully")
	c.JSON(http.StatusOK, middleware.H{"message": "Tenant deleted successfully"})
}

// GetTenantCluster retrieves cluster information for a tenant
func GetTenantCluster(c middleware.Context) {
	tenantID := c.Param("id")

	var tenant models.Tenant
	query := `
		SELECT id, deployment_tier, deployment_model, primary_deployment_tier, 
		       allowed_deployment_tiers, primary_cluster_id, kafka_topic_prefix, 
		       kafka_brokers, database_url
		FROM tenants 
		WHERE id = $1 AND is_active = TRUE
	`

	err := db.QueryRow(query, tenantID).Scan(
		&tenant.ID, &tenant.DeploymentTier, &tenant.DeploymentModel,
		&tenant.PrimaryDeploymentTier, pq.Array(&tenant.AllowedDeploymentTiers),
		&tenant.PrimaryClusterID, &tenant.KafkaTopicPrefix,
		pq.Array(&tenant.KafkaBrokers), &tenant.DatabaseURL,
	)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, middleware.H{"error": "Tenant not found"})
		return
	}

	if err != nil {
		logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to get tenant cluster")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusOK, tenant)
}

// UpdateTenantCluster updates the cluster routing information for a tenant
func UpdateTenantCluster(c middleware.Context) {
	tenantID := c.Param("id")

	var req struct {
		PrimaryClusterID       *string  `json:"primary_cluster_id,omitempty"`
		DeploymentModel        *string  `json:"deployment_model,omitempty"`
		PrimaryDeploymentTier  *string  `json:"primary_deployment_tier,omitempty"`
		AllowedDeploymentTiers []string `json:"allowed_deployment_tiers,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, middleware.H{"error": err.Error()})
		return
	}

	query := `
		UPDATE tenants 
		SET primary_cluster_id = COALESCE($2, primary_cluster_id),
		    deployment_model = COALESCE($3, deployment_model),
		    primary_deployment_tier = COALESCE($4, primary_deployment_tier),
		    allowed_deployment_tiers = COALESCE($5, allowed_deployment_tiers),
		    updated_at = NOW()
		WHERE id = $1 AND is_active = TRUE
	`

	result, err := db.Exec(query, tenantID, req.PrimaryClusterID, req.DeploymentModel,
		req.PrimaryDeploymentTier, pq.Array(req.AllowedDeploymentTiers))

	if err != nil {
		logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to update tenant cluster")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to update tenant cluster"})
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		c.JSON(http.StatusNotFound, middleware.H{"error": "Tenant not found"})
		return
	}

	logger.WithField("tenant_id", tenantID).Info("Updated tenant cluster successfully")
	c.JSON(http.StatusOK, middleware.H{"message": "Tenant cluster updated successfully"})
}

// GetTenantsBatch retrieves multiple tenants by IDs
func GetTenantsBatch(c middleware.Context) {
	var req struct {
		TenantIDs []string `json:"tenant_ids" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, middleware.H{"error": err.Error()})
		return
	}

	if len(req.TenantIDs) == 0 {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "No tenant IDs provided"})
		return
	}

	query := `
		SELECT id, name, deployment_tier, deployment_model, primary_deployment_tier,
		       allowed_deployment_tiers, primary_cluster_id, kafka_topic_prefix,
		       kafka_brokers, database_url, is_active
		FROM tenants 
		WHERE id = ANY($1) AND is_active = TRUE
	`

	rows, err := db.Query(query, pq.Array(req.TenantIDs))
	if err != nil {
		logger.WithError(err).Error("Failed to get tenants batch")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var tenants []models.Tenant
	for rows.Next() {
		var tenant models.Tenant
		err := rows.Scan(
			&tenant.ID, &tenant.Name, &tenant.DeploymentTier, &tenant.DeploymentModel,
			&tenant.PrimaryDeploymentTier, pq.Array(&tenant.AllowedDeploymentTiers),
			&tenant.PrimaryClusterID, &tenant.KafkaTopicPrefix,
			pq.Array(&tenant.KafkaBrokers), &tenant.DatabaseURL, &tenant.IsActive,
		)
		if err != nil {
			logger.WithError(err).Error("Failed to scan tenant in batch")
			continue
		}
		tenants = append(tenants, tenant)
	}

	c.JSON(http.StatusOK, middleware.H{"tenants": tenants})
}

// GetTenantsByCluster retrieves all tenants assigned to a specific cluster
func GetTenantsByCluster(c middleware.Context) {
	clusterID := c.Param("cluster_id")

	query := `
		SELECT t.id, t.name, t.deployment_tier, t.deployment_model, 
		       t.primary_deployment_tier, t.allowed_deployment_tiers,
		       t.primary_cluster_id, t.is_active
		FROM tenants t
		LEFT JOIN tenant_cluster_assignments tca ON t.id = tca.tenant_id
		WHERE (t.primary_cluster_id = $1 OR tca.cluster_id = $1) AND t.is_active = TRUE
	`

	rows, err := db.Query(query, clusterID)
	if err != nil {
		logger.WithError(err).WithField("cluster_id", clusterID).Error("Failed to get tenants by cluster")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var tenants []models.Tenant
	for rows.Next() {
		var tenant models.Tenant
		err := rows.Scan(
			&tenant.ID, &tenant.Name, &tenant.DeploymentTier, &tenant.DeploymentModel,
			&tenant.PrimaryDeploymentTier, pq.Array(&tenant.AllowedDeploymentTiers),
			&tenant.PrimaryClusterID, &tenant.IsActive,
		)
		if err != nil {
			logger.WithError(err).Error("Failed to scan tenant by cluster")
			continue
		}
		tenants = append(tenants, tenant)
	}

	c.JSON(http.StatusOK, middleware.H{"tenants": tenants, "cluster_id": clusterID})
}
