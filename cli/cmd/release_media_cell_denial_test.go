package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	metpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/metering_contract"
	limitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"google.golang.org/protobuf/proto"
)

func TestMediaAuthorityDoctorSignedDenials(t *testing.T) {
	const tenant = "60000000-0000-4000-8000-000000000001"
	cases := []struct {
		name   string
		change func(*mediapb.TenantAuthority)
		want   int64
	}{
		{"spent prepaid", func(*mediapb.TenantAuthority) {}, 0},
		{"suspended", func(p *mediapb.TenantAuthority) {
			p.BillingDecision = mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED
		}, 0},
		{"inactive", func(p *mediapb.TenantAuthority) {
			p.Lifecycle = mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
			p.BillingDecision = mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_INACTIVE
		}, 0},
		{"permission still waits", func(p *mediapb.TenantAuthority) {
			p.BillingDecision = mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW
		}, 1},
		{"unknown decision still waits", func(p *mediapb.TenantAuthority) { p.BillingDecision = mediapb.TenantBillingDecision(999) }, 1},
		{"unknown lifecycle still waits", func(p *mediapb.TenantAuthority) { p.Lifecycle = mediapb.AuthorityLifecycle(999) }, 1},
		{"denial with grants still waits", func(p *mediapb.TenantAuthority) {
			p.EffectiveClusterGrants = []*mediapb.TenantClusterGrant{{ClusterId: "cell-a"}}
		}, 1},
		{"denial allowing shared playback still waits", func(p *mediapb.TenantAuthority) { p.AllowPlatformSharedPlayback = true }, 1},
		{"different tenant still waits", func(p *mediapb.TenantAuthority) { p.TenantId = "60000000-0000-4000-8000-000000000002" }, 1},
		{"different schema still waits", func(p *mediapb.TenantAuthority) { p.SchemaVersion = 2 }, 1},
		{"unknown fields still wait", func(p *mediapb.TenantAuthority) { p.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01}) }, 1},
		{"missing billing model still waits", func(p *mediapb.TenantAuthority) {
			p.BillingModel = mediapb.TenantBillingModel_TENANT_BILLING_MODEL_UNSPECIFIED
		}, 1},
		{"negative limits still wait", func(p *mediapb.TenantAuthority) { p.ResourceLimits = &limitspb.TenantResourceLimits{MaxStreams: -1} }, 1},
		{"negative tier still waits", func(p *mediapb.TenantAuthority) { p.TierLevel = -1 }, 1},
		{"invalid allowance still waits", func(p *mediapb.TenantAuthority) { p.Allowances = []*metpb.MeterAllowance{{Included: -1}} }, 1},
		{"nested unknown fields still wait", func(p *mediapb.TenantAuthority) {
			p.ResourceLimits = &limitspb.TenantResourceLimits{}
			p.ResourceLimits.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := &mediapb.TenantAuthority{SchemaVersion: 1, TenantId: tenant, Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED, BillingModel: mediapb.TenantBillingModel_TENANT_BILLING_MODEL_PREPAID, DecisionReason: "prepaid_balance_unavailable"}
			tc.change(payload)
			wire, err := proto.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(wire)
			encoded, err := json.Marshal(legacyDenialProbeFields(tenant, wire, digest[:]))
			if err != nil {
				t.Fatal(err)
			}
			output := "######## database commodore\ncapabilities_table=1\ncell_ready:cell-a=1\nlegacy_schema_tenants_on_ready_cells=1\nlegacy_schema_tenants_cell:cell-a=1\nlegacy_schema_tenant_payload=" + string(encoded) + "\n"
			values := parseMediaAuthorityDoctorOutput(output)
			state, err := parseMediaCellAuthority(values["commodore"], "cell-a")
			if err != nil || state.LegacyTenants != tc.want {
				t.Fatalf("gate counted %d legacy permission(s), want %d; err=%v", state.LegacyTenants, tc.want, err)
			}
			if got := values["commodore"]["legacy_schema_tenants_on_ready_cells"]; got != tc.want {
				t.Fatalf("doctor counted %d legacy permission(s), want %d", got, tc.want)
			}
		})
	}
}

func TestMediaAuthorityDoctorMalformedDenialsKeepTheGateClosed(t *testing.T) {
	for _, raw := range []string{`not-json`, `{}`, `{"cell_id":"cell-a","tenant_id":"tenant","payload_hex":"not-hex"}`, `{"cell_id":"cell-a","tenant_id":"tenant","payload_hex":"ff"}`} {
		t.Run(fmt.Sprintf("payload_%x", raw), func(t *testing.T) {
			values := parseMediaAuthorityDoctorOutput("######## database commodore\nlegacy_schema_tenants_on_ready_cells=1\nlegacy_schema_tenants_cell:cell-a=1\nlegacy_schema_tenant_payload=" + raw + "\n")
			if values["commodore"]["legacy_schema_tenants_on_ready_cells"] != 1 || values["commodore"]["legacy_schema_tenants_cell:cell-a"] != 1 {
				t.Fatal("malformed denial cleared the gate")
			}
		})
	}
	values := parseMediaAuthorityDoctorOutput("######## database commodore\nlegacy_schema_tenant_payload={}\n")
	if _, reported := values["commodore"]["legacy_schema_tenants_on_ready_cells"]; reported {
		t.Fatal("a payload invented a missing probe count")
	}
}

func TestMediaAuthorityDoctorDenialDoesNotHideAnotherTenant(t *testing.T) {
	const tenant = "60000000-0000-4000-8000-000000000001"
	wire, err := proto.Marshal(&mediapb.TenantAuthority{SchemaVersion: 1, TenantId: tenant, Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED, BillingModel: mediapb.TenantBillingModel_TENANT_BILLING_MODEL_PREPAID})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(wire)
	encoded, err := json.Marshal(legacyDenialProbeFields(tenant, wire, digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	row := "legacy_schema_tenant_payload=" + string(encoded) + "\n"
	output := "######## database commodore\nlegacy_schema_tenants_on_ready_cells=2\nlegacy_schema_tenants_cell:cell-a=2\n" + strings.Repeat(row, 2)
	values := parseMediaAuthorityDoctorOutput(output)
	if got := values["commodore"]["legacy_schema_tenants_cell:cell-a"]; got != 1 {
		t.Fatalf("duplicate denial hid another tenant: gate count=%d, want 1", got)
	}
}

func legacyDenialProbeFields(tenant string, wire, digest []byte) map[string]string {
	issuedAt := time.Unix(1700000000, 0).UTC()
	return map[string]string{
		"cell_id": "cell-a", "tenant_id": tenant,
		"payload_hex": hex.EncodeToString(wire), "payload_sha256_hex": hex.EncodeToString(digest),
		"issued_at":     issuedAt.Format(time.RFC3339Nano),
		"refresh_after": issuedAt.Add(time.Hour).Format(time.RFC3339Nano),
		"valid_until":   issuedAt.Add(24 * time.Hour).Format(time.RFC3339Nano),
	}
}
