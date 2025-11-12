package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	qmapi "frameworks/pkg/api/quartermaster"
	"github.com/gin-gonic/gin"
)

// CreateBootstrapToken POST /admin/bootstrap-tokens
func CreateBootstrapToken(c *gin.Context) {
	// Only provider or service role
	_, role, isProvider := getAuthContext(c)
	if role != "service" && !isProvider {
		c.JSON(http.StatusForbidden, qmapi.ErrorResponse{Error: "forbidden"})
		return
	}
	var req qmapi.CreateBootstrapTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, qmapi.ErrorResponse{Error: "invalid request"})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, qmapi.ErrorResponse{Error: "name required"})
		return
	}
	if req.Metadata == nil {
		req.Metadata = map[string]interface{}{}
	}
	if req.Kind != "edge_node" && req.Kind != "service" {
		c.JSON(http.StatusBadRequest, qmapi.ErrorResponse{Error: "invalid kind"})
		return
	}
	if req.Kind == "edge_node" && (req.TenantID == nil || *req.TenantID == "") {
		c.JSON(http.StatusBadRequest, qmapi.ErrorResponse{Error: "tenant_id required for edge_node"})
		return
	}
	ttl := 24 * time.Hour
	if req.TTL != "" {
		if d, err := time.ParseDuration(req.TTL); err == nil {
			ttl = d
		} else {
			c.JSON(http.StatusBadRequest, qmapi.ErrorResponse{Error: "invalid ttl"})
			return
		}
	}
	expires := time.Now().Add(ttl)

	// Generate random token using DB function for consistency
	var token string
	if err := db.QueryRow(`SELECT quartermaster.generate_random_string(48)`).Scan(&token); err != nil {
		logger.WithError(err).Error("failed to generate token")
		c.JSON(http.StatusInternalServerError, qmapi.ErrorResponse{Error: "failed to generate token"})
		return
	}

	// Insert token
	var bt qmapi.BootstrapToken
	sMeta, _ := json.Marshal(req.Metadata)
	var tenantIDStr, clusterIDStr, expectedIPStr, createdByStr sql.NullString
	var metaOut sql.NullString
	var usageLimit sql.NullInt64
	err := db.QueryRow(`
	        INSERT INTO quartermaster.bootstrap_tokens (token, kind, name, tenant_id, cluster_id, expected_ip, metadata, usage_limit, expires_at)
	        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	        RETURNING id, token, kind, name, tenant_id::text, cluster_id, expected_ip::text, metadata::text, usage_limit, usage_count, expires_at, used_at, created_by::text, created_at
	    `, token, req.Kind, req.Name, req.TenantID, req.ClusterID, req.ExpectedIP, string(sMeta), req.UsageLimit, expires).Scan(
		&bt.ID, &bt.Token, &bt.Kind, &bt.Name, &tenantIDStr, &clusterIDStr, &expectedIPStr, &metaOut, &usageLimit, &bt.UsageCount, &bt.ExpiresAt, &bt.UsedAt, &createdByStr, &bt.CreatedAt,
	)
	if err != nil {
		logger.WithError(err).Error("failed to insert bootstrap token")
		c.JSON(http.StatusInternalServerError, qmapi.ErrorResponse{Error: "failed to create token"})
		return
	}
	if tenantIDStr.Valid && tenantIDStr.String != "" {
		s := tenantIDStr.String
		bt.TenantID = &s
	}
	if clusterIDStr.Valid && clusterIDStr.String != "" {
		s := clusterIDStr.String
		bt.ClusterID = &s
	}
	if expectedIPStr.Valid && expectedIPStr.String != "" {
		s := expectedIPStr.String
		bt.ExpectedIP = &s
	}
	if metaOut.Valid && metaOut.String != "" {
		var md map[string]interface{}
		_ = json.Unmarshal([]byte(metaOut.String), &md)
		bt.Metadata = md
	}
	if createdByStr.Valid && createdByStr.String != "" {
		s := createdByStr.String
		bt.CreatedBy = &s
	}
	if usageLimit.Valid {
		v := int(usageLimit.Int64)
		bt.UsageLimit = &v
	}
	c.JSON(http.StatusCreated, qmapi.CreateBootstrapTokenResponse{Token: bt})
}

// ListBootstrapTokens GET /admin/bootstrap-tokens
func ListBootstrapTokens(c *gin.Context) {
	// Only provider or service role
	_, role, isProvider := getAuthContext(c)
	if role != "service" && !isProvider {
		c.JSON(http.StatusForbidden, qmapi.ErrorResponse{Error: "forbidden"})
		return
	}
	rows, err := db.Query(`
	        SELECT id, token, kind, name, COALESCE(tenant_id::text,NULL), COALESCE(cluster_id,NULL), COALESCE(expected_ip::text,NULL), metadata, usage_limit, usage_count, expires_at, used_at, COALESCE(created_by::text,NULL), created_at
	        FROM quartermaster.bootstrap_tokens
	        ORDER BY created_at DESC
	    `)
	if err != nil {
		logger.WithError(err).Error("failed to list bootstrap tokens")
		c.JSON(http.StatusInternalServerError, qmapi.ErrorResponse{Error: "failed to list tokens"})
		return
	}
	defer rows.Close()
	out := []qmapi.BootstrapToken{}
	for rows.Next() {
		var bt qmapi.BootstrapToken
		var tenantIDStr, clusterIDStr, expectedIPStr, createdByStr sql.NullString
		var metaJSON sql.NullString
		var usageLimit sql.NullInt64
		if err := rows.Scan(&bt.ID, &bt.Token, &bt.Kind, &bt.Name, &tenantIDStr, &clusterIDStr, &expectedIPStr, &metaJSON, &usageLimit, &bt.UsageCount, &bt.ExpiresAt, &bt.UsedAt, &createdByStr, &bt.CreatedAt); err == nil {
			if tenantIDStr.Valid && tenantIDStr.String != "" {
				s := tenantIDStr.String
				bt.TenantID = &s
			}
			if clusterIDStr.Valid && clusterIDStr.String != "" {
				s := clusterIDStr.String
				bt.ClusterID = &s
			}
			if expectedIPStr.Valid && expectedIPStr.String != "" {
				s := expectedIPStr.String
				bt.ExpectedIP = &s
			}
			if createdByStr.Valid && createdByStr.String != "" {
				s := createdByStr.String
				bt.CreatedBy = &s
			}
			if usageLimit.Valid {
				v := int(usageLimit.Int64)
				bt.UsageLimit = &v
			}
			if metaJSON.Valid && metaJSON.String != "" {
				var md map[string]interface{}
				_ = json.Unmarshal([]byte(metaJSON.String), &md)
				bt.Metadata = md
			}
			out = append(out, bt)
		}
	}
	c.JSON(http.StatusOK, qmapi.BootstrapTokensResponse{Tokens: out, Count: len(out)})
}

// RevokeBootstrapToken DELETE /admin/bootstrap-tokens/:id
func RevokeBootstrapToken(c *gin.Context) {
	// Only provider or service role
	_, role, isProvider := getAuthContext(c)
	if role != "service" && !isProvider {
		c.JSON(http.StatusForbidden, qmapi.ErrorResponse{Error: "forbidden"})
		return
	}
	id := c.Param("id")
	res, err := db.Exec(`DELETE FROM quartermaster.bootstrap_tokens WHERE id = $1`, id)
	if err != nil {
		logger.WithError(err).WithField("id", id).Error("failed to revoke bootstrap token")
		c.JSON(http.StatusInternalServerError, qmapi.ErrorResponse{Error: "failed to revoke token"})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, qmapi.ErrorResponse{Error: "not found"})
		return
	}
	c.JSON(http.StatusOK, qmapi.SuccessResponse{Message: "revoked"})
}
