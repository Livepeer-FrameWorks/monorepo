package handlers

import (
	"net/http"

	qmapi "frameworks/pkg/api/quartermaster"

	"github.com/gin-gonic/gin"
)

// ResolveNodeFingerprint matches or creates a stable node mapping from fingerprint+peer IP, returning tenant and canonical node_id.
// Security: Only callable by internal services (protected group enforces JWT/service token).
func ResolveNodeFingerprint(c *gin.Context) {
	var req qmapi.ResolveNodeFingerprintRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PeerIP == "" {
		c.JSON(http.StatusBadRequest, qmapi.ErrorResponse{Error: "invalid request; peer_ip required"})
		return
	}

	// 1) Try exact match by machine-id hash
	var tenantID, nodeID string
	if req.MachineIDSHA256 != nil && *req.MachineIDSHA256 != "" {
		err := db.QueryRow(`SELECT tenant_id::text, node_id FROM quartermaster.node_fingerprints WHERE fingerprint_machine_sha256 = $1`, *req.MachineIDSHA256).Scan(&tenantID, &nodeID)
		if err == nil {
			_ = upsertSeenIP(nodeID, req)
			c.JSON(http.StatusOK, qmapi.ResolveNodeFingerprintResponse{TenantID: tenantID, CanonicalNodeID: nodeID})
			return
		}
	}
	// 2) Then by MACs hash
	if req.MacsSHA256 != nil && *req.MacsSHA256 != "" {
		err := db.QueryRow(`SELECT tenant_id::text, node_id FROM quartermaster.node_fingerprints WHERE fingerprint_macs_sha256 = $1`, *req.MacsSHA256).Scan(&tenantID, &nodeID)
		if err == nil {
			_ = upsertSeenIP(nodeID, req)
			c.JSON(http.StatusOK, qmapi.ResolveNodeFingerprintResponse{TenantID: tenantID, CanonicalNodeID: nodeID})
			return
		}
	}
	// 3) Lastly by seen IP
	err := db.QueryRow(`SELECT tenant_id::text, node_id FROM quartermaster.node_fingerprints WHERE $1::inet = ANY(seen_ips) ORDER BY last_seen DESC LIMIT 1`, req.PeerIP).Scan(&tenantID, &nodeID)
	if err == nil {
		_ = upsertSeenIP(nodeID, req)
		c.JSON(http.StatusOK, qmapi.ResolveNodeFingerprintResponse{TenantID: tenantID, CanonicalNodeID: nodeID})
		return
	}

	// No match: do not create mappings here to avoid bypassing enrollment.
	// Fingerprint mappings must be provisioned/admin-created. Return 404.
	c.JSON(http.StatusNotFound, qmapi.ErrorResponse{Error: "fingerprint not recognized"})
}

func upsertSeenIP(nodeID string, req qmapi.ResolveNodeFingerprintRequest) error {
	if req.PeerIP == "" {
		return nil
	}
	_, err := db.Exec(`UPDATE quartermaster.node_fingerprints SET last_seen = NOW(), seen_ips = array_append(seen_ips, $1::inet) WHERE node_id = $2 AND NOT ($1::inet = ANY(seen_ips))`, req.PeerIP, nodeID)
	return err
}

func nullStr(p *string) interface{} {
	if p == nil {
		return nil
	}
	if *p == "" {
		return nil
	}
	return *p
}
