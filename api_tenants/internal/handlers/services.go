package handlers

import (
	"database/sql"
	"encoding/json"
	"frameworks/pkg/models"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

// ============================================================================
// SERVICE CATALOG MANAGEMENT
// ============================================================================

// GetServices returns all services in the catalog
func GetServices(c *gin.Context) {
	rows, err := db.Query(`
		SELECT id, service_id, name, plane, description, default_port,
		       health_check_path, docker_image, version, dependencies,
		       tags, is_active, created_at, updated_at
		FROM services
		WHERE is_active = true
		ORDER BY plane, name
	`)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch services")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch services"})
		return
	}
	defer rows.Close()

	var services []models.Service
	for rows.Next() {
		var service models.Service
		var tagsJSON sql.NullString

		err := rows.Scan(
			&service.ID, &service.ServiceID, &service.Name, &service.Plane,
			&service.Description, &service.DefaultPort, &service.HealthCheckPath,
			&service.DockerImage, &service.Version, pq.Array(&service.Dependencies),
			&tagsJSON, &service.IsActive, &service.CreatedAt, &service.UpdatedAt,
		)
		if err != nil {
			logger.WithError(err).Error("Failed to scan service row")
			continue
		}

		// Parse tags JSON if present
		if tagsJSON.Valid {
			if err := json.Unmarshal([]byte(tagsJSON.String), &service.Tags); err != nil {
				logger.WithError(err).Warn("Failed to parse service tags JSON")
				service.Tags = make(map[string]interface{})
			}
		} else {
			service.Tags = make(map[string]interface{})
		}

		services = append(services, service)
	}

	c.JSON(http.StatusOK, gin.H{
		"services": services,
		"count":    len(services),
	})
}

// GetService returns a specific service by ID
func GetService(c *gin.Context) {
	serviceID := c.Param("service_id")

	var service models.Service
	var tagsJSON sql.NullString

	err := db.QueryRow(`
		SELECT id, service_id, name, plane, description, default_port,
		       health_check_path, docker_image, version, dependencies,
		       tags, is_active, created_at, updated_at
		FROM services
		WHERE service_id = $1 AND is_active = true
	`, serviceID).Scan(
		&service.ID, &service.ServiceID, &service.Name, &service.Plane,
		&service.Description, &service.DefaultPort, &service.HealthCheckPath,
		&service.DockerImage, &service.Version, pq.Array(&service.Dependencies),
		&tagsJSON, &service.IsActive, &service.CreatedAt, &service.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Service not found"})
		return
	}

	if err != nil {
		logger.WithError(err).Error("Failed to fetch service")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch service"})
		return
	}

	// Parse tags JSON if present
	if tagsJSON.Valid {
		if err := json.Unmarshal([]byte(tagsJSON.String), &service.Tags); err != nil {
			logger.WithError(err).Warn("Failed to parse service tags JSON")
			service.Tags = make(map[string]interface{})
		}
	} else {
		service.Tags = make(map[string]interface{})
	}

	c.JSON(http.StatusOK, service)
}

// ============================================================================
// CLUSTER SERVICE MANAGEMENT
// ============================================================================

// GetClusterServices returns all services assigned to a cluster
func GetClusterServices(c *gin.Context) {
	clusterID := c.Param("cluster_id")

	rows, err := db.Query(`
		SELECT cs.id, cs.cluster_id, cs.service_id, cs.desired_state,
		       cs.desired_replicas, cs.current_replicas, cs.config_blob,
		       cs.environment_vars, cs.cpu_limit, cs.memory_limit_mb,
		       cs.health_status, cs.last_deployed, cs.created_at, cs.updated_at,
		       s.name, s.plane
		FROM cluster_services cs
		JOIN services s ON cs.service_id = s.service_id
		WHERE cs.cluster_id = $1
		ORDER BY s.plane, s.name
	`, clusterID)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch cluster services")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch cluster services"})
		return
	}
	defer rows.Close()

	var clusterServices []models.ClusterService
	for rows.Next() {
		var cs models.ClusterService
		var configJSON, envJSON sql.NullString

		err := rows.Scan(
			&cs.ID, &cs.ClusterID, &cs.ServiceID, &cs.DesiredState,
			&cs.DesiredReplicas, &cs.CurrentReplicas, &configJSON, &envJSON,
			&cs.CPULimit, &cs.MemoryLimitMB, &cs.HealthStatus, &cs.LastDeployed,
			&cs.CreatedAt, &cs.UpdatedAt, &cs.ServiceName, &cs.ServicePlane,
		)
		if err != nil {
			logger.WithError(err).Error("Failed to scan cluster service row")
			continue
		}

		// Parse JSON fields
		if configJSON.Valid {
			if err := json.Unmarshal([]byte(configJSON.String), &cs.ConfigBlob); err != nil {
				logger.WithError(err).Warn("Failed to parse config JSON")
				cs.ConfigBlob = make(map[string]interface{})
			}
		} else {
			cs.ConfigBlob = make(map[string]interface{})
		}

		if envJSON.Valid {
			if err := json.Unmarshal([]byte(envJSON.String), &cs.EnvironmentVars); err != nil {
				logger.WithError(err).Warn("Failed to parse environment vars JSON")
				cs.EnvironmentVars = make(map[string]interface{})
			}
		} else {
			cs.EnvironmentVars = make(map[string]interface{})
		}

		clusterServices = append(clusterServices, cs)
	}

	c.JSON(http.StatusOK, gin.H{
		"cluster_id": clusterID,
		"services":   clusterServices,
		"count":      len(clusterServices),
	})
}

// UpdateClusterServiceState updates the desired state of a service on a cluster
func UpdateClusterServiceState(c *gin.Context) {
	clusterID := c.Param("cluster_id")
	serviceID := c.Param("service_id")

	var req struct {
		DesiredState    string                 `json:"desired_state"`
		DesiredReplicas *int                   `json:"desired_replicas,omitempty"`
		ConfigBlob      map[string]interface{} `json:"config_blob,omitempty"`
		EnvironmentVars map[string]interface{} `json:"environment_vars,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Build dynamic update query
	query := "UPDATE cluster_services SET desired_state = $1, updated_at = NOW()"
	args := []interface{}{req.DesiredState}
	argCount := 1

	if req.DesiredReplicas != nil {
		argCount++
		query += ", desired_replicas = $" + strconv.Itoa(argCount)
		args = append(args, *req.DesiredReplicas)
	}

	if req.ConfigBlob != nil {
		argCount++
		configJSON, _ := json.Marshal(req.ConfigBlob)
		query += ", config_blob = $" + strconv.Itoa(argCount)
		args = append(args, string(configJSON))
	}

	if req.EnvironmentVars != nil {
		argCount++
		envJSON, _ := json.Marshal(req.EnvironmentVars)
		query += ", environment_vars = $" + strconv.Itoa(argCount)
		args = append(args, string(envJSON))
	}

	argCount++
	query += " WHERE cluster_id = $" + strconv.Itoa(argCount)
	args = append(args, clusterID)

	argCount++
	query += " AND service_id = $" + strconv.Itoa(argCount)
	args = append(args, serviceID)

	result, err := db.Exec(query, args...)
	if err != nil {
		logger.WithError(err).Error("Failed to update cluster service")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update service"})
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Service assignment not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    "Service state updated successfully",
		"cluster_id": clusterID,
		"service_id": serviceID,
	})
}

// GetServiceInstances returns all running instances of services
func GetServiceInstances(c *gin.Context) {
	clusterID := c.Query("cluster_id")
	serviceID := c.Query("service_id")

	query := `
		SELECT si.id, si.instance_id, si.cluster_id, si.node_id, si.service_id,
		       si.version, si.port, si.process_id, si.container_id, si.status,
		       si.health_status, si.started_at, si.stopped_at, si.last_health_check,
		       si.cpu_usage_percent, si.memory_usage_mb, si.created_at, si.updated_at,
		       s.name as service_name, s.plane as service_plane
		FROM service_instances si
		JOIN services s ON si.service_id = s.service_id
		WHERE 1=1
	`
	args := []interface{}{}
	argCount := 0

	if clusterID != "" {
		argCount++
		query += " AND si.cluster_id = $" + strconv.Itoa(argCount)
		args = append(args, clusterID)
	}

	if serviceID != "" {
		argCount++
		query += " AND si.service_id = $" + strconv.Itoa(argCount)
		args = append(args, serviceID)
	}

	query += " ORDER BY si.cluster_id, s.name, si.instance_id"

	rows, err := db.Query(query, args...)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch service instances")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch service instances"})
		return
	}
	defer rows.Close()

	var instances []models.ServiceInstance
	for rows.Next() {
		var instance models.ServiceInstance
		var serviceName, servicePlane string

		err := rows.Scan(
			&instance.ID, &instance.InstanceID, &instance.ClusterID, &instance.NodeID,
			&instance.ServiceID, &instance.Version, &instance.Port, &instance.ProcessID,
			&instance.ContainerID, &instance.Status, &instance.HealthStatus,
			&instance.StartedAt, &instance.StoppedAt, &instance.LastHealthCheck,
			&instance.CPUUsagePercent, &instance.MemoryUsageMB, &instance.CreatedAt,
			&instance.UpdatedAt, &serviceName, &servicePlane,
		)
		if err != nil {
			logger.WithError(err).Error("Failed to scan service instance row")
			continue
		}

		instances = append(instances, instance)
	}

	c.JSON(http.StatusOK, gin.H{
		"instances": instances,
		"count":     len(instances),
		"filters": gin.H{
			"cluster_id": clusterID,
			"service_id": serviceID,
		},
	})
}
