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

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// StartHealthPoller launches a background goroutine that polls HTTP/gRPC health endpoints
// for all registered service instances and updates their health status in the database.
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
		id, serviceID, proto, host, path string
		port                             int
	}
	var list []inst
	for rows.Next() {
		var i inst
		var host sql.NullString
		var path sql.NullString
		if err := rows.Scan(&i.id, &i.serviceID, new(string), &i.proto, &host, &i.port, &path); err == nil {
			if host.Valid {
				i.host = host.String
			}
			if path.Valid {
				i.path = path.String
			}
			list = append(list, i)
		}
	}

	logger.WithField("count", len(list)).Debug("Health poller checking instances")

	for _, it := range list {
		if it.host == "" || it.port == 0 {
			logger.WithField("instance_id", it.id).WithField("service", it.serviceID).Warn("Skipping health check: missing host or port")
			continue
		}
		// HTTP health
		if it.proto == "http" {
			// path required for http; skip if not known
			if it.path == "" {
				logger.WithField("instance_id", it.id).WithField("service", it.serviceID).Warn("Skipping HTTP health check: no path configured")
				continue
			}
			sem <- struct{}{}
			go func(ii inst) {
				defer func() { <-sem }()
				url := fmt.Sprintf("http://%s:%d%s", ii.host, ii.port, ii.path)
				status := "healthy"
				resp, err := client.Get(url)
				if err != nil {
					status = "unhealthy"
					logger.WithError(err).WithField("service", ii.serviceID).WithField("url", url).Debug("HTTP health check failed")
				} else if resp.StatusCode != 200 {
					status = "unhealthy"
					logger.WithField("service", ii.serviceID).WithField("url", url).WithField("status_code", resp.StatusCode).Debug("HTTP health check returned non-200")
				} else {
					logger.WithField("service", ii.serviceID).WithField("url", url).Debug("HTTP health check passed")
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
					logger.WithError(err).WithField("service", ii.serviceID).WithField("addr", addr).Debug("gRPC health check dial failed")
					_, _ = db.Exec(`UPDATE quartermaster.service_instances SET health_status=$1, last_health_check=NOW(), updated_at=NOW() WHERE instance_id=$2`, status, ii.id)
					return
				}
				defer conn.Close()
				hc := healthpb.NewHealthClient(conn)
				if _, err := hc.Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
					status = "unhealthy"
					logger.WithError(err).WithField("service", ii.serviceID).WithField("addr", addr).Debug("gRPC health check failed")
				} else {
					logger.WithField("service", ii.serviceID).WithField("addr", addr).Debug("gRPC health check passed")
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
