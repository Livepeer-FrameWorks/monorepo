package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	qmapi "frameworks/pkg/api/quartermaster"
	"frameworks/pkg/models"
	"github.com/gin-gonic/gin"
)

// getAuthContext extracts tenant, role and provider flag from request context
func getAuthContext(c *gin.Context) (tenantID string, role string, isProvider bool) {
	tVal, _ := c.Get("tenant_id")
	rVal, _ := c.Get("role")
	if tVal != nil {
		if s, ok := tVal.(string); ok {
			tenantID = s
		}
	}
	if rVal != nil {
		if s, ok := rVal.(string); ok {
			role = s
		}
	}
	if tenantID != "" {
		_ = db.QueryRow(`SELECT COALESCE(is_provider,false) FROM quartermaster.tenants WHERE id = $1`, tenantID).Scan(&isProvider)
	}
	return
}

// POST /bootstrap/edge-node
func BootstrapEdgeNode(c *gin.Context) {
	// Only internal services or provider can bootstrap nodes
	_, role, isProvider := getAuthContext(c)
	if role != "service" && !isProvider {
		c.JSON(http.StatusForbidden, qmapi.ErrorResponse{Error: "forbidden"})
		return
	}
	var req qmapi.BootstrapEdgeNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, qmapi.ErrorResponse{Error: "invalid request"})
		return
	}

	// Validate token
	var kind, tenantID, clusterID string
	var expiresAt time.Time
	err := db.QueryRow(`
        SELECT kind, COALESCE(tenant_id::text,''), COALESCE(cluster_id,''), expires_at
        FROM quartermaster.bootstrap_tokens
        WHERE token = $1 AND used_at IS NULL
    `, req.Token).Scan(&kind, &tenantID, &clusterID, &expiresAt)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusUnauthorized, qmapi.ErrorResponse{Error: "invalid or used token"})
		return
	}
	if err != nil {
		logger.WithError(err).Error("bootstrap token lookup failed")
		c.JSON(http.StatusInternalServerError, qmapi.ErrorResponse{Error: "internal error"})
		return
	}
	if time.Now().After(expiresAt) || kind != "edge_node" || tenantID == "" {
		c.JSON(http.StatusUnauthorized, qmapi.ErrorResponse{Error: "token expired or invalid"})
		return
	}

	// Create node_id
	nodeID := "edge-" + strings.ToLower(time.Now().Format("060102150405"))
	nodeName := req.Hostname
	if nodeName == "" {
		nodeName = nodeID
	}

	// For now, assign to a default shared cluster if clusterID empty
	if clusterID == "" {
		_ = db.QueryRow(`SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE is_active = true ORDER BY cluster_name LIMIT 1`).Scan(&clusterID)
		if clusterID == "" {
			c.JSON(http.StatusServiceUnavailable, qmapi.ErrorResponse{Error: "no active cluster available"})
			return
		}
	}

	// Insert node
	_, err = db.Exec(`
        INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type, tags, metadata)
        VALUES ($1,$2,$3,'edge','{}','{}')
    `, nodeID, clusterID, nodeName)
	if err != nil {
		logger.WithError(err).WithField("node_id", nodeID).Error("failed to insert edge node")
		c.JSON(http.StatusInternalServerError, qmapi.ErrorResponse{Error: "failed to create node"})
		return
	}

	// Bind fingerprint if provided (at enrollment time)
	// Note: seen_ips initialized from request IP list
	if req.MachineIDSHA256 != nil || req.MacsSHA256 != nil || len(req.IPs) > 0 || len(req.Labels) > 0 || len(req.IPs) > 0 {
		var attrsJSON string
		if len(req.Labels) > 0 {
			if b, _ := json.Marshal(req.Labels); b != nil {
				attrsJSON = string(b)
			}
		} else {
			attrsJSON = "{}"
		}
		// Build array literal for seen_ips
		if len(req.IPs) == 0 {
			_, _ = db.Exec(`
                INSERT INTO quartermaster.node_fingerprints (tenant_id, node_id, fingerprint_machine_sha256, fingerprint_macs_sha256, seen_ips, attrs)
                VALUES ($1,$2,$3,$4,'{}',$5)
                ON CONFLICT (node_id) DO UPDATE SET last_seen = NOW()
            `, tenantID, nodeID, nullString(req.MachineIDSHA256), nullString(req.MacsSHA256), attrsJSON)
		} else {
			// Convert []string to '{ip1,ip2}' literal for inet[]
			ips := "{" + strings.Join(req.IPs, ",") + "}"
			_, _ = db.Exec(`
                INSERT INTO quartermaster.node_fingerprints (tenant_id, node_id, fingerprint_machine_sha256, fingerprint_macs_sha256, seen_ips, attrs)
                VALUES ($1,$2,$3,$4,$5::inet[],$6)
                ON CONFLICT (node_id) DO UPDATE SET last_seen = NOW(), seen_ips = quartermaster.node_fingerprints.seen_ips || EXCLUDED.seen_ips
            `, tenantID, nodeID, nullString(req.MachineIDSHA256), nullString(req.MacsSHA256), ips, attrsJSON)
		}
	}

	// Mark token used
	_, _ = db.Exec(`UPDATE quartermaster.bootstrap_tokens SET used_at = NOW() WHERE token = $1`, req.Token)

	c.JSON(http.StatusOK, qmapi.BootstrapEdgeNodeResponse{
		NodeID:    nodeID,
		TenantID:  tenantID,
		ClusterID: clusterID,
	})
}

func nullString(p *string) interface{} {
	if p == nil {
		return nil
	}
	if *p == "" {
		return nil
	}
	return *p
}

// POST /bootstrap/service
func BootstrapService(c *gin.Context) {
	// Only internal services or provider can bootstrap services
	_, role, isProvider := getAuthContext(c)
	if role != "service" && !isProvider {
		c.JSON(http.StatusForbidden, qmapi.ErrorResponse{Error: "forbidden"})
		return
	}
	var req qmapi.BootstrapServiceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, qmapi.ErrorResponse{Error: "invalid request"})
		return
	}

	// Validate token (either bootstrap token or provider service token via middleware already)
	var clusterID string
	if req.Token != nil && *req.Token != "" {
		var kind string
		var expiresAt time.Time
		err := db.QueryRow(`SELECT kind, COALESCE(cluster_id,''), expires_at FROM quartermaster.bootstrap_tokens WHERE token=$1 AND used_at IS NULL`, *req.Token).Scan(&kind, &clusterID, &expiresAt)
		if err == sql.ErrNoRows || kind != "service" || time.Now().After(expiresAt) {
			c.JSON(http.StatusUnauthorized, qmapi.ErrorResponse{Error: "invalid bootstrap token"})
			return
		}
		_, _ = db.Exec(`UPDATE quartermaster.bootstrap_tokens SET used_at = NOW() WHERE token=$1`, *req.Token)
	} else {
		// Fallback: requires service token auth; choose any active provider cluster
		_ = db.QueryRow(`SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE is_active = true ORDER BY cluster_name LIMIT 1`).Scan(&clusterID)
		if clusterID == "" {
			c.JSON(http.StatusServiceUnavailable, qmapi.ErrorResponse{Error: "no active cluster available"})
			return
		}
	}

	// Ensure service exists (catalog)
	var serviceID string
	err := db.QueryRow(`SELECT service_id FROM quartermaster.services WHERE name = $1`, req.Type).Scan(&serviceID)
	if err == sql.ErrNoRows {
		serviceID = req.Type
		_, err = db.Exec(`INSERT INTO quartermaster.services (service_id, name, plane, is_active) VALUES ($1,$2,'control',true)`, serviceID, req.Type)
	}
	if err != nil {
		logger.WithError(err).Error("failed to ensure service catalog entry")
		c.JSON(http.StatusInternalServerError, qmapi.ErrorResponse{Error: "failed to register service"})
		return
	}

	instanceID := "inst-" + strings.ToLower(time.Now().Format("060102150405"))
	// Derive protocol and advertise host
	proto := strings.ToLower(strings.TrimSpace(req.Protocol))
	if proto == "" {
		proto = "http"
	}
	advHost := ""
	if req.AdvertiseHost != nil && *req.AdvertiseHost != "" {
		advHost = *req.AdvertiseHost
	} else {
		advHost = c.ClientIP()
	}
	var healthOverride *string
	if req.HealthEndpoint != nil && *req.HealthEndpoint != "" {
		healthOverride = req.HealthEndpoint
	}

	// Idempotent registration: reuse existing instance row for same (service_id, cluster_id, protocol, port) when present.
	var existingID string
	var existingInstanceID string
	_ = db.QueryRow(`
        SELECT id::text, instance_id FROM quartermaster.service_instances
        WHERE service_id = $1 AND cluster_id = $2 AND protocol = $3 AND port = $4
        ORDER BY updated_at DESC NULLS LAST, started_at DESC NULLS LAST LIMIT 1
    `, serviceID, clusterID, proto, req.Port).Scan(&existingID, &existingInstanceID)

	if existingID != "" {
		// Update existing row
		_, err = db.Exec(`
            UPDATE quartermaster.service_instances
            SET advertise_host=$1,
                health_endpoint_override=$2,
                version=$3,
                status='running',
                health_status='unknown',
                started_at = COALESCE(started_at, NOW()),
                last_health_check = NULL,
                updated_at=NOW()
            WHERE id = $4::uuid
        `, advHost, healthOverride, req.Version, existingID)
		if err != nil {
			logger.WithError(err).Error("failed to update service instance")
			c.JSON(http.StatusInternalServerError, qmapi.ErrorResponse{Error: "failed to update service instance"})
			return
		}
		instanceID = existingInstanceID
	} else {
		// Insert a new row
		_, err = db.Exec(`
            INSERT INTO quartermaster.service_instances (instance_id, cluster_id, service_id, protocol, advertise_host, health_endpoint_override, version, port, status, health_status)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'running','unknown')
        `, instanceID, clusterID, serviceID, proto, advHost, healthOverride, req.Version, req.Port)
		if err != nil {
			logger.WithError(err).Error("failed to insert service instance")
			c.JSON(http.StatusInternalServerError, qmapi.ErrorResponse{Error: "failed to create service instance"})
			return
		}
	}

	// Cleanup: stop obviously stale or duplicate rows for same service/cluster.
	// - Stale: last_health_check older than 10 minutes or NULL
	// - Exact duplicates on same host+port+protocol
	_, _ = db.Exec(`
        UPDATE quartermaster.service_instances
        SET status='stopped', updated_at=NOW()
        WHERE service_id = $1 AND cluster_id = $2 AND instance_id <> $3
          AND (
                last_health_check IS NULL OR last_health_check < NOW() - INTERVAL '10 minutes' OR
                (COALESCE(advertise_host,'') = $4 AND COALESCE(protocol,'') = $5 AND COALESCE(port,0) = $6)
              )
    `, serviceID, clusterID, instanceID, advHost, proto, req.Port)

	c.JSON(http.StatusOK, qmapi.BootstrapServiceResponse{
		ServiceID:  serviceID,
		InstanceID: instanceID,
		ClusterID:  clusterID,
	})
}

// GET /service-discovery
func ServiceDiscovery(c *gin.Context) {
	typ := c.Query("type")
	cluster := c.Query("cluster_id")
	if typ == "" {
		c.JSON(http.StatusBadRequest, qmapi.ErrorResponse{Error: "type required"})
		return
	}
	// Resolve service_id by name
	var serviceID string
	_ = db.QueryRow(`SELECT service_id FROM quartermaster.services WHERE name=$1`, typ).Scan(&serviceID)
	if serviceID == "" {
		c.JSON(http.StatusOK, qmapi.ServiceDiscoveryResponse{Instances: []models.ServiceInstance{}, Count: 0})
		return
	}

	query := `SELECT si.id, si.instance_id, si.cluster_id, si.node_id, si.service_id, si.version, si.port, si.process_id, si.container_id, si.status, si.health_status, si.started_at, si.stopped_at, si.last_health_check, si.cpu_usage_percent, si.memory_usage_mb, si.created_at, si.updated_at FROM quartermaster.service_instances si WHERE si.service_id = $1`
	args := []interface{}{serviceID}

	// Optional cluster filter
	nextIdx := 2
	if cluster != "" {
		query += " AND si.cluster_id = $2"
		args = append(args, cluster)
		nextIdx = 3
	}

	// Permission filter: non-provider tenants only see owned or granted clusters
	tenantID, role, isProvider := getAuthContext(c)
	if role != "service" && !isProvider {
		ph := fmt.Sprintf("$%d", nextIdx)
		query += " AND (si.cluster_id IN (SELECT cluster_id FROM quartermaster.tenant_cluster_access WHERE tenant_id = " + ph + " AND is_active = true) OR si.cluster_id IN (SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE owner_tenant_id = " + ph + "))"
		args = append(args, tenantID)
		nextIdx++
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		logger.WithError(err).Error("service discovery query failed")
		c.JSON(http.StatusInternalServerError, qmapi.ErrorResponse{Error: "failed to query instances"})
		return
	}
	defer rows.Close()
	instances := []models.ServiceInstance{}
	for rows.Next() {
		var si models.ServiceInstance
		if err := rows.Scan(&si.ID, &si.InstanceID, &si.ClusterID, &si.NodeID, &si.ServiceID, &si.Version, &si.Port, &si.ProcessID, &si.ContainerID, &si.Status, &si.HealthStatus, &si.StartedAt, &si.StoppedAt, &si.LastHealthCheck, &si.CPUUsagePercent, &si.MemoryUsageMB, &si.CreatedAt, &si.UpdatedAt); err == nil {
			instances = append(instances, si)
		}
	}
	c.JSON(http.StatusOK, qmapi.ServiceDiscoveryResponse{Instances: instances, Count: len(instances)})
}

// GET /clusters/access
func GetClustersAccess(c *gin.Context) {
	tenantID, role, isProvider := getAuthContext(c)
	var rows *sql.Rows
	var err error
	if role == "service" || isProvider {
		rows, err = db.Query(`SELECT c.cluster_id, c.cluster_name, 'owner' as access_level, '{}'::jsonb as resource_limits FROM quartermaster.infrastructure_clusters c WHERE c.is_active = true ORDER BY c.cluster_name`)
	} else {
		rows, err = db.Query(`
            SELECT c.cluster_id, c.cluster_name, COALESCE(tca.access_level,'shared') as access_level, COALESCE(tca.resource_limits,'{}') as resource_limits
            FROM quartermaster.infrastructure_clusters c
            JOIN quartermaster.tenant_cluster_access tca ON tca.cluster_id = c.cluster_id AND tca.is_active = true
            WHERE c.is_active = true AND tca.tenant_id = $1
            ORDER BY c.cluster_name`, tenantID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, qmapi.ErrorResponse{Error: "failed to fetch clusters"})
		return
	}
	defer rows.Close()
	list := []qmapi.ClusterAccessEntry{}
	for rows.Next() {
		var id, name, level string
		var limits map[string]interface{}
		var raw []byte
		if err := rows.Scan(&id, &name, &level, &raw); err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &limits)
		}
		if limits == nil {
			limits = map[string]interface{}{}
		}
		list = append(list, qmapi.ClusterAccessEntry{ClusterID: id, ClusterName: name, AccessLevel: level, ResourceLimits: limits})
	}
	c.JSON(http.StatusOK, qmapi.ClustersAccessResponse{Clusters: list, Count: len(list)})
}

// GET /clusters/available
func GetClustersAvailable(c *gin.Context) {
	tenantID, role, isProvider := getAuthContext(c)
	var rows *sql.Rows
	var err error
	if role == "service" || isProvider {
		rows, err = db.Query(`SELECT cluster_id, cluster_name FROM quartermaster.infrastructure_clusters WHERE is_active = true ORDER BY cluster_name`)
	} else {
		rows, err = db.Query(`
            SELECT c.cluster_id, c.cluster_name
            FROM quartermaster.infrastructure_clusters c
            WHERE c.is_active = true AND NOT EXISTS (
                SELECT 1 FROM quartermaster.tenant_cluster_access tca
                WHERE tca.cluster_id = c.cluster_id AND tca.tenant_id = $1 AND tca.is_active = true
            )
            ORDER BY c.cluster_name`, tenantID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, qmapi.ErrorResponse{Error: "failed to fetch clusters"})
		return
	}
	defer rows.Close()
	var out []qmapi.AvailableClusterEntry
	for rows.Next() {
		var id, name string
		_ = rows.Scan(&id, &name)
		out = append(out, qmapi.AvailableClusterEntry{ClusterID: id, ClusterName: name, Tiers: []string{"free"}, AutoEnroll: true})
	}
	c.JSON(http.StatusOK, qmapi.ClustersAvailableResponse{Clusters: out, Count: len(out)})
}
