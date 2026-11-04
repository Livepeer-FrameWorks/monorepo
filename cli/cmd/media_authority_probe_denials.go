package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type legacySchemaTenantPayload struct {
	CellID           string    `json:"cell_id"`
	TenantID         string    `json:"tenant_id"`
	PayloadHex       string    `json:"payload_hex"`
	PayloadSHA256Hex string    `json:"payload_sha256_hex"`
	IssuedAt         time.Time `json:"issued_at"`
	RefreshAfter     time.Time `json:"refresh_after"`
	ValidUntil       time.Time `json:"valid_until"`
}

// A signed denial needs no placement permission. Missing, inconsistent, or
// unreadable payloads keep their original count, so they cannot open the gate.
func excludeLegacySchemaDenials(state map[string]int64, rows []string) {
	seen := map[[2]string]bool{}
	for _, raw := range rows {
		var row legacySchemaTenantPayload
		if json.Unmarshal([]byte(raw), &row) != nil || row.CellID == "" || uuid.Validate(row.TenantID) != nil {
			continue
		}
		identity := [2]string{row.CellID, row.TenantID}
		if seen[identity] || !legacySchemaPayloadDenies(row) {
			continue
		}
		seen[identity] = true
		cellKey := "legacy_schema_tenants_cell:" + row.CellID
		if state["legacy_schema_tenants_on_ready_cells"] > 0 && state[cellKey] > 0 {
			state["legacy_schema_tenants_on_ready_cells"]--
			state[cellKey]--
		}
	}
}

func legacySchemaPayloadDenies(row legacySchemaTenantPayload) bool {
	if row.IssuedAt.IsZero() || row.RefreshAfter.IsZero() || row.ValidUntil.IsZero() || row.RefreshAfter.Before(row.IssuedAt) || !row.ValidUntil.After(row.RefreshAfter) {
		return false
	}
	wire, err := hex.DecodeString(row.PayloadHex)
	if err != nil || len(wire) == 0 {
		return false
	}
	digest := sha256.Sum256(wire)
	if hex.EncodeToString(digest[:]) != row.PayloadSHA256Hex {
		return false
	}
	var tenant mediapb.TenantAuthority
	if proto.Unmarshal(wire, &tenant) != nil {
		return false
	}
	envelope := &mediapb.AuthorityEnvelope{
		SchemaVersion: sharedauthority.SchemaVersion, AuthorityId: row.TenantID,
		IssuedAt: timestamppb.New(row.IssuedAt), RefreshAfter: timestamppb.New(row.RefreshAfter), ValidUntil: timestamppb.New(row.ValidUntil),
	}
	if sharedauthority.ValidateTenantPayload(envelope, &tenant) != nil {
		return false
	}
	if _, known := mediapb.TenantBillingModel_name[int32(tenant.GetBillingModel())]; !known {
		return false
	}
	if tenant.GetMediaPlacement() != nil || len(tenant.GetEffectiveClusterGrants()) != 0 || tenant.GetAllowPlatformSharedPlayback() {
		return false
	}
	switch tenant.GetLifecycle() {
	case mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE:
		return tenant.GetBillingDecision() == mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED || tenant.GetBillingDecision() == mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED
	case mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE:
		return tenant.GetBillingDecision() == mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_INACTIVE
	default:
		return false
	}
}
