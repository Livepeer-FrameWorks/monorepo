//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	meteringpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/metering_contract"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"google.golang.org/protobuf/proto"
)

// An object compile captures its tenant and is pinned to it until publication.
// Metering publishes a new tenant version on every allowance change; that
// version changes nothing an object is derived from and fans out to no object,
// so it must not supersede an object compile in flight. A change to a field
// objects derive from still does.
func TestMediaAuthorityObjectCompileAllowanceOnlyTenantPublishDoesNotSupersede_RealPG(t *testing.T) {
	testMediaAuthorityObjectCompileAllowanceOnlyTenantPublishDoesNotSupersede(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityObjectCompileAllowanceOnlyTenantPublishDoesNotSupersede_RealYugabyte(t *testing.T) {
	testMediaAuthorityObjectCompileAllowanceOnlyTenantPublishDoesNotSupersede(t, startPlacementDeliveryYugabyte(t, "parent_pin"))
}

func testMediaAuthorityObjectCompileAllowanceOnlyTenantPublishDoesNotSupersede(t *testing.T, db *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "parent-pin", mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
	revisions := []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "parent-pin"}}
	inUse := withMediaAuthorityInUse(ctx, true)
	publishTenant := func(tenant *mediapb.TenantAuthority) {
		t.Helper()
		now := time.Now().UTC()
		if err := s.persistTenantAuthority(inUse, tenant, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	tenantVersion := func() int64 {
		t.Helper()
		current, err := commodoredb.New(db).GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "tenant", AuthorityID: obligationTenantID})
		if err != nil {
			t.Fatal(err)
		}
		return current.AuthorityVersion
	}
	compileObject := func(parent *mediapb.TenantAuthority, streamID string) (bool, error) {
		t.Helper()
		object := useStreamPayload("parent-pin-" + streamID)
		object.GetLiveStream().StreamId = streamID
		id := "live_stream:" + streamID
		err := s.persistMediaObjectAuthority(withMediaObjectParent(inUse, parent), id, object, []string{"cell-a"}, revisions, time.Now().UTC(), time.Now().UTC().Add(time.Hour))
		_, readErr := commodoredb.New(db).GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: id})
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			t.Fatal(readErr)
		}
		return readErr == nil, err
	}

	parent := obligationTenantPayload()
	publishTenant(parent)
	first := tenantVersion()

	metered := proto.CloneOf(parent)
	metered.Allowances = []*meteringpb.MeterAllowance{{Meter: "delivered_minutes", Included: 1000, Used: 400, Remaining: 600}}
	metered.ResourceLimits = &tenantlimitspb.TenantResourceLimits{MaxStreams: 3}
	metered.DecisionReason = "allowance updated"
	publishTenant(metered)
	if tenantVersion() != first+1 {
		t.Fatalf("metering change published tenant version %d, want %d", tenantVersion(), first+1)
	}
	if published, err := compileObject(parent, "parent-pin-metered"); err != nil || !published {
		t.Fatalf("object compiled on the pre-metering tenant: err=%v published=%v, want published", err, published)
	}

	for _, tt := range []struct {
		name   string
		change func(*mediapb.TenantAuthority)
	}{
		{"preferred-cluster", func(tenant *mediapb.TenantAuthority) { tenant.PreferredClusterId = "cell-b" }},
		{"cluster-grants", func(tenant *mediapb.TenantAuthority) {
			tenant.EffectiveClusterGrants[0].EligibleServingCellIds = append(tenant.EffectiveClusterGrants[0].EligibleServingCellIds, "cell-b")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			captured := proto.CloneOf(metered)
			changed := proto.CloneOf(metered)
			tt.change(changed)
			publishTenant(changed)
			defer publishTenant(metered)
			published, err := compileObject(captured, "parent-pin-"+tt.name)
			if !errors.Is(err, errMediaAuthorityCompileSuperseded) || published {
				t.Fatalf("object compiled across a %s change: err=%v published=%v, want superseded", tt.name, err, published)
			}
		})
	}
}
