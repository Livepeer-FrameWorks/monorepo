package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	purserapi "frameworks/pkg/api/purser"
	"frameworks/pkg/logging"
	"frameworks/pkg/models"

	"github.com/gin-gonic/gin"
)

// IngestUsageData handles usage data from Periscope-Query
func IngestUsageData(c *gin.Context) {
	var req models.UsageIngestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, purserapi.UsageIngestResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	summaries := req.UsageSummaries
	if len(summaries) == 0 {
		c.JSON(http.StatusBadRequest, purserapi.UsageIngestResponse{
			Success: false,
			Error:   "No usage summaries provided",
		})
		return
	}

	logger.WithFields(logging.Fields{
		"summaries_count": len(summaries),
		"source":          req.Source,
	}).Info("Processing usage summaries")

	// Process each usage summary
	var processedCount int
	var errors []string
	processedTenants := make(map[string]struct{})

	for _, summary := range summaries {
		source := req.Source
		if source == "" {
			source = "periscope-query"
		}

		if err := processUsageSummary(summary, source); err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"tenant_id":  summary.TenantID,
				"cluster_id": summary.ClusterID,
			}).Error("Failed to process usage summary")
			errors = append(errors, err.Error())
			continue
		}
		processedCount++
		processedTenants[summary.TenantID] = struct{}{}
	}

	// Synchronously update invoice drafts for processed tenants (no periodic job required)
	for tenantID := range processedTenants {
		if err := updateInvoiceDraft(tenantID); err != nil {
			logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to update invoice draft after ingestion")
		}
	}

	// Response with results
	response := purserapi.UsageIngestResponse{
		ProcessedCount: processedCount,
		Success:        len(errors) == 0,
	}

	if len(errors) > 0 {
		response.Error = fmt.Sprintf("Failed to process %d summaries: %v", len(errors), errors)
		logger.WithField("error_count", len(errors)).Warn("Some usage summaries failed to process")
	}

	logger.WithField("processed_count", processedCount).Info("Successfully processed usage summaries")
	c.JSON(http.StatusOK, response)
}

// processUsageSummary processes a single usage summary and stores it in the usage records table
func processUsageSummary(summary models.UsageSummary, source string) error {
	// Validate tenant has access to this cluster
	var hasAccess bool
	err := db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM tenant_cluster_access tca
			WHERE tca.tenant_id = $1 AND tca.cluster_id = $2 AND tca.is_active = true
		)
	`, summary.TenantID, summary.ClusterID).Scan(&hasAccess)

	if err != nil {
		return err
	}

	if !hasAccess {
		logger.WithFields(logging.Fields{
			"tenant_id":  summary.TenantID,
			"cluster_id": summary.ClusterID,
		}).Warn("Tenant does not have access to cluster, skipping usage summary")
		return nil // Skip but don't error
	}

	// Convert summary to individual usage records
	billingMonth := summary.Timestamp.Format("2006-01")
	usageTypes := map[string]float64{
		"stream_hours":        summary.StreamHours,
		"egress_gb":           summary.EgressGB,
		"recording_gb":        summary.RecordingGB,
		"peak_bandwidth_mbps": summary.PeakBandwidthMbps,
	}

	// Insert/update usage records for each usage type
	for usageType, usageValue := range usageTypes {
		if usageValue <= 0 {
			continue // Skip zero usage
		}

		usageDetails := models.JSONB{
			"max_viewers":       summary.MaxViewers,
			"total_streams":     summary.TotalStreams,
			"unique_users":      summary.UniqueUsers,
			"avg_viewers":       summary.AvgViewers,
			"unique_countries":  summary.UniqueCountries,
			"unique_cities":     summary.UniqueCities,
			"avg_buffer_health": summary.AvgBufferHealth,
			"avg_bitrate":       summary.AvgBitrate,
			"packet_loss_rate":  summary.PacketLossRate,
			"source":            source,
		}

		// Check if this usage record already exists
		var existingID string
		err = db.QueryRow(`
			SELECT id FROM usage_records 
			WHERE tenant_id = $1 AND cluster_id = $2 AND usage_type = $3 AND billing_month = $4
		`, summary.TenantID, summary.ClusterID, usageType, billingMonth).Scan(&existingID)

		if err == sql.ErrNoRows {
			// Insert new usage record
			_, err = db.Exec(`
				INSERT INTO usage_records (
					tenant_id, cluster_id, usage_type, usage_value, usage_details, billing_month, created_at
				) VALUES ($1, $2, $3, $4, $5, $6, NOW())
			`, summary.TenantID, summary.ClusterID, usageType, usageValue, usageDetails, billingMonth)

			if err != nil {
				return err
			}

			logger.WithFields(logging.Fields{
				"tenant_id":     summary.TenantID,
				"cluster_id":    summary.ClusterID,
				"usage_type":    usageType,
				"usage_value":   usageValue,
				"billing_month": billingMonth,
			}).Debug("Inserted new usage record")

		} else if err != nil {
			return err
		} else {
			// Update existing usage record (in case of reprocessing)
			_, err = db.Exec(`
				UPDATE usage_records SET
					usage_value = $3, usage_details = $4, created_at = NOW()
				WHERE id = $1 AND tenant_id = $2
			`, existingID, summary.TenantID, usageValue, usageDetails)

			if err != nil {
				return err
			}

			logger.WithFields(logging.Fields{
				"tenant_id":   summary.TenantID,
				"cluster_id":  summary.ClusterID,
				"usage_type":  usageType,
				"existing_id": existingID,
			}).Debug("Updated existing usage record")
		}
	}

	return nil
}

// updateInvoiceDraft creates or updates an invoice draft for the tenant based on usage
func updateInvoiceDraft(tenantID string) error {
	// Get tenant's current subscription and tier info (billing data belongs to Purser)
	var tierID, subscriptionStatus string
	err := db.QueryRow(`
		SELECT ts.tier_id, ts.status 
		FROM tenant_subscriptions ts
		WHERE ts.tenant_id = $1 AND ts.status = 'active'
	`, tenantID).Scan(&tierID, &subscriptionStatus)

	if err != nil {
		return err
	}

	if subscriptionStatus != "active" {
		logger.WithField("tenant_id", tenantID).Debug("Tenant billing not active, skipping invoice draft update")
		return nil
	}

	// Get current billing period (monthly)
	now := time.Now()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	periodEnd := periodStart.AddDate(0, 1, 0).Add(-time.Second)

	// Aggregate usage for current billing period
	var totalStreamHours, totalEgressGB, totalRecordingGB float64
	var maxViewersInPeriod, totalStreamsInPeriod int

	err = db.QueryRow(`
		SELECT 
			COALESCE(SUM(stream_hours), 0),
			COALESCE(SUM(egress_gb), 0),
			COALESCE(MAX(max_viewers), 0),
			COALESCE(SUM(total_streams), 0),
			COALESCE(SUM(recording_gb), 0)
			FROM usage_records 
			WHERE tenant_id = $1 
			AND toDate(billing_month || '-01') BETWEEN $2::date AND $3::date
	`, tenantID, periodStart, periodEnd).Scan(
		&totalStreamHours, &totalEgressGB, &maxViewersInPeriod,
		&totalStreamsInPeriod, &totalRecordingGB)

	if err != nil {
		return err
	}

	// Calculate charges based on usage (simplified pricing model)
	// This would be enhanced with actual plan-based pricing
	var totalAmount float64

	// Example pricing: $0.10 per stream hour, $0.05 per GB egress, $0.02 per GB recording
	totalAmount += totalStreamHours * 0.10
	totalAmount += totalEgressGB * 0.05
	totalAmount += totalRecordingGB * 0.02

	// Create or update invoice draft
	_, err = db.Exec(`
		INSERT INTO invoice_drafts (
			tenant_id, billing_period_start, billing_period_end,
			stream_hours, egress_gb, recording_gb, max_viewers, total_streams,
			calculated_amount, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'draft', NOW(), NOW())
		ON CONFLICT (tenant_id, billing_period_start) DO UPDATE SET
			stream_hours = EXCLUDED.stream_hours,
			egress_gb = EXCLUDED.egress_gb,
			recording_gb = EXCLUDED.recording_gb,
			max_viewers = EXCLUDED.max_viewers,
			total_streams = EXCLUDED.total_streams,
			calculated_amount = EXCLUDED.calculated_amount,
			updated_at = NOW()
	`, tenantID, periodStart, periodEnd, totalStreamHours, totalEgressGB,
		totalRecordingGB, maxViewersInPeriod, totalStreamsInPeriod, totalAmount)

	if err != nil {
		return err
	}

	logger.WithFields(logging.Fields{
		"tenant_id":         tenantID,
		"billing_period":    periodStart.Format("2006-01"),
		"stream_hours":      totalStreamHours,
		"egress_gb":         totalEgressGB,
		"calculated_amount": totalAmount,
	}).Debug("Updated invoice draft for tenant")

	return nil
}

// GetUsageRecords returns usage records for a tenant
func GetUsageRecords(c *gin.Context) {
	// Try to get tenant_id from context first (user requests)
	tenantID := c.GetString("tenant_id")

	// If not in context, check query parameters (service-to-service requests)
	if tenantID == "" {
		tenantID = c.Query("tenant_id")
	}

	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Tenant ID is required"})
		return
	}

	// Parse query parameters
	clusterID := c.Query("cluster_id")
	usageType := c.Query("usage_type")
	billingMonth := c.Query("billing_month")

	// Build dynamic query
	query := `
		SELECT ur.id, ur.tenant_id, ur.cluster_id, ur.usage_type, 
		       ur.usage_value, ur.usage_details, ur.billing_month, ur.created_at,
		       ic.cluster_name
		FROM usage_records ur
		LEFT JOIN infrastructure_clusters ic ON ur.cluster_id = ic.cluster_id
		WHERE ur.tenant_id = $1
	`

	args := []interface{}{tenantID}
	argCount := 1

	if clusterID != "" {
		argCount++
		query += fmt.Sprintf(" AND ur.cluster_id = $%d", argCount)
		args = append(args, clusterID)
	}

	if usageType != "" {
		argCount++
		query += fmt.Sprintf(" AND ur.usage_type = $%d", argCount)
		args = append(args, usageType)
	}

	if billingMonth != "" {
		argCount++
		query += fmt.Sprintf(" AND ur.billing_month = $%d", argCount)
		args = append(args, billingMonth)
	}

	query += " ORDER BY ur.created_at DESC LIMIT 100"

	rows, err := db.Query(query, args...)
	if err != nil {
		logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to query usage records")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var records []map[string]interface{}
	for rows.Next() {
		var record struct {
			ID           string       `json:"id"`
			TenantID     string       `json:"tenant_id"`
			ClusterID    string       `json:"cluster_id"`
			UsageType    string       `json:"usage_type"`
			UsageValue   float64      `json:"usage_value"`
			UsageDetails models.JSONB `json:"usage_details"`
			BillingMonth string       `json:"billing_month"`
			CreatedAt    time.Time    `json:"created_at"`
			ClusterName  *string      `json:"cluster_name"`
		}

		err := rows.Scan(
			&record.ID, &record.TenantID, &record.ClusterID, &record.UsageType,
			&record.UsageValue, &record.UsageDetails, &record.BillingMonth,
			&record.CreatedAt, &record.ClusterName,
		)
		if err != nil {
			logger.WithError(err).Error("Failed to scan usage record")
			continue
		}

		recordMap := map[string]interface{}{
			"id":            record.ID,
			"tenant_id":     record.TenantID,
			"cluster_id":    record.ClusterID,
			"cluster_name":  record.ClusterName,
			"usage_type":    record.UsageType,
			"usage_value":   record.UsageValue,
			"usage_details": record.UsageDetails,
			"billing_month": record.BillingMonth,
			"created_at":    record.CreatedAt,
		}
		records = append(records, recordMap)
	}

	c.JSON(http.StatusOK, gin.H{
		"usage_records": records,
		"tenant_id":     tenantID,
		"filters": map[string]interface{}{
			"cluster_id":    clusterID,
			"usage_type":    usageType,
			"billing_month": billingMonth,
		},
	})
}
