package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type serviceInstanceHealth struct {
	InstanceID      string     `json:"instance_id"`
	ServiceID       string     `json:"service_id"`
	ClusterID       string     `json:"cluster_id"`
	Protocol        string     `json:"protocol"`
	Host            *string    `json:"host,omitempty"`
	Port            int        `json:"port"`
	HealthEndpoint  *string    `json:"health_endpoint,omitempty"`
	Status          string     `json:"status"`
	LastHealthCheck *time.Time `json:"last_health_check,omitempty"`
}

// GetServicesHealth returns current health status for all service instances
func GetServicesHealth(c *gin.Context) {
	tenantID, role, isProvider := getAuthContext(c)
	// Return all active instances with a recent health check; do not collapse to one-per-service.
	// This supports horizontal scaling without showing stale rows after restarts.
	// Consider a row stale if last_health_check is older than 180 seconds.
	query := `
        SELECT si.instance_id, si.service_id, si.cluster_id, si.protocol, si.advertise_host, si.port,
               si.health_endpoint_override, s.health_check_path, si.health_status, si.last_health_check
        FROM quartermaster.service_instances si
        JOIN quartermaster.services s ON si.service_id = s.service_id
        WHERE si.status IN ('running','starting')
          AND si.last_health_check IS NOT NULL
          AND si.last_health_check >= NOW() - INTERVAL '180 seconds'`
	var rows *sql.Rows
	var err error
	if role == "service" || isProvider {
		rows, err = db.Query(query)
	} else {
		// Restrict to clusters the tenant owns or has access to
		query += ` AND cluster_id IN (
            SELECT cluster_id FROM quartermaster.tenant_cluster_access WHERE tenant_id = $1 AND is_active = true
        ) OR cluster_id IN (
            SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE owner_tenant_id = $1
        )`
		rows, err = db.Query(query, tenantID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query health"})
		return
	}
	defer rows.Close()
	var out []serviceInstanceHealth
	for rows.Next() {
		var h serviceInstanceHealth
		var hostNS, healthNS, defaultHealthNS sql.NullString
		var lastNT sql.NullTime
		if err := rows.Scan(&h.InstanceID, &h.ServiceID, &h.ClusterID, &h.Protocol, &hostNS, &h.Port, &healthNS, &defaultHealthNS, &h.Status, &lastNT); err == nil {
			if hostNS.Valid {
				v := hostNS.String
				h.Host = &v
			}
			// Prefer explicit override; fall back to service default
			if healthNS.Valid {
				v := healthNS.String
				h.HealthEndpoint = &v
			} else if defaultHealthNS.Valid {
				v := defaultHealthNS.String
				h.HealthEndpoint = &v
			}
			if lastNT.Valid {
				t := lastNT.Time
				h.LastHealthCheck = &t
			}
			out = append(out, h)
		}
	}
	c.JSON(http.StatusOK, gin.H{"instances": out, "count": len(out)})
}

// GetServiceHealth returns health for instances of a specific service
func GetServiceHealth(c *gin.Context) {
	sid := c.Param("id")
	tenantID, role, isProvider := getAuthContext(c)
	query := `
        SELECT si.instance_id, si.service_id, si.cluster_id, si.protocol, si.advertise_host, si.port,
               si.health_endpoint_override, s.health_check_path, si.health_status, si.last_health_check
        FROM quartermaster.service_instances si
        JOIN quartermaster.services s ON si.service_id = s.service_id
        WHERE si.service_id = $1
          AND si.status IN ('running','starting')
          AND si.last_health_check IS NOT NULL
          AND si.last_health_check >= NOW() - INTERVAL '180 seconds'`
	var rows *sql.Rows
	var err error
	if role == "service" || isProvider {
		rows, err = db.Query(query, sid)
	} else {
		query += ` AND (cluster_id IN (
            SELECT cluster_id FROM quartermaster.tenant_cluster_access WHERE tenant_id = $2 AND is_active = true
        ) OR cluster_id IN (
            SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE owner_tenant_id = $2
        ))`
		rows, err = db.Query(query, sid, tenantID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query health"})
		return
	}
	defer rows.Close()
	var out []serviceInstanceHealth
	for rows.Next() {
		var h serviceInstanceHealth
		var hostNS, healthNS, defaultHealthNS sql.NullString
		var lastNT sql.NullTime
		if err := rows.Scan(&h.InstanceID, &h.ServiceID, &h.ClusterID, &h.Protocol, &hostNS, &h.Port, &healthNS, &defaultHealthNS, &h.Status, &lastNT); err == nil {
			if hostNS.Valid {
				v := hostNS.String
				h.Host = &v
			}
			if healthNS.Valid {
				v := healthNS.String
				h.HealthEndpoint = &v
			} else if defaultHealthNS.Valid {
				v := defaultHealthNS.String
				h.HealthEndpoint = &v
			}
			if lastNT.Valid {
				t := lastNT.Time
				h.LastHealthCheck = &t
			}
			out = append(out, h)
		}
	}
	c.JSON(http.StatusOK, gin.H{"instances": out, "count": len(out)})
}

// StartHealthPoller launches a background goroutine that polls HTTP health endpoints
func StartHealthPoller() {
	interval := time.Duration(getenvInt("QM_HEALTH_POLL_INTERVAL_SECONDS", 30)) * time.Second
	timeout := time.Duration(getenvInt("QM_HEALTH_TIMEOUT_MS", 2000)) * time.Millisecond
	maxConc := getenvInt("QM_HEALTH_MAX_CONCURRENCY", 8)
	if maxConc <= 0 {
		maxConc = 8
	}

	client := &http.Client{Timeout: timeout}
	sem := make(chan struct{}, maxConc)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := pollOnce(client, sem); err != nil {
				logger.WithError(err).Warn("health poller iteration failed")
			}
			<-ticker.C
		}
	}()
}

func pollOnce(client *http.Client, sem chan struct{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `
        SELECT si.instance_id, si.service_id, si.cluster_id, si.protocol, si.advertise_host, si.port,
               COALESCE(si.health_endpoint_override, s.health_check_path) AS path
        FROM quartermaster.service_instances si
        JOIN quartermaster.services s ON si.service_id = s.service_id
        WHERE si.status IN ('running','starting')`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type inst struct {
		id, proto, host, path string
		port                  int
	}
	var list []inst
	for rows.Next() {
		var i inst
		var host sql.NullString
		var path sql.NullString
		if err := rows.Scan(&i.id, new(string), new(string), &i.proto, &host, &i.port, &path); err == nil {
			if host.Valid {
				i.host = host.String
			}
			if path.Valid {
				i.path = path.String
			}
			list = append(list, i)
		}
	}
	for _, it := range list {
		if it.host == "" || it.port == 0 {
			continue
		}
		// HTTP health
		if it.proto == "http" {
			// path required for http; skip if not known
			if it.path == "" {
				continue
			}
			sem <- struct{}{}
			go func(ii inst) {
				defer func() { <-sem }()
				url := fmt.Sprintf("http://%s:%d%s", ii.host, ii.port, ii.path)
				status := "healthy"
				resp, err := client.Get(url)
				if err != nil || resp.StatusCode != 200 {
					status = "unhealthy"
				}
				if resp != nil {
					_ = resp.Body.Close()
				}
				_, _ = db.Exec(`UPDATE quartermaster.service_instances SET health_status=$1, last_health_check=NOW(), updated_at=NOW() WHERE instance_id=$2`, status, ii.id)
			}(it)
			continue
		}
		// gRPC health
		if it.proto == "grpc" {
			sem <- struct{}{}
			go func(ii inst) {
				defer func() { <-sem }()
				addr := fmt.Sprintf("%s:%d", ii.host, ii.port)
				status := "healthy"
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
				if err != nil {
					status = "unhealthy"
					_, _ = db.Exec(`UPDATE quartermaster.service_instances SET health_status=$1, last_health_check=NOW(), updated_at=NOW() WHERE instance_id=$2`, status, ii.id)
					return
				}
				defer conn.Close()
				hc := healthpb.NewHealthClient(conn)
				if _, err := hc.Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
					status = "unhealthy"
				}
				_, _ = db.Exec(`UPDATE quartermaster.service_instances SET health_status=$1, last_health_check=NOW(), updated_at=NOW() WHERE instance_id=$2`, status, ii.id)
			}(it)
			continue
		}
	}
	return nil
}

func getenvInt(key string, def int) int {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return def
	}
	return n
}
