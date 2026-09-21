package serviceeventoutbox

import (
	"sort"
	"strings"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// The baseline and the expand migration both carry the shared outbox table
// definition verbatim, so the relay's queries match the table on every path.
func TestDomainEventOutboxDDLIsTheSharedTemplate(t *testing.T) {
	ddl, err := outbox.TableDDL(Schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"schema/quartermaster.sql",
		"migrations/quartermaster/v0.3.11/expand/003_domain_event_outbox.sql",
	} {
		content, err := dbsql.Content.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), ddl) {
			t.Errorf("%s does not contain outbox.TableDDL(%q) verbatim", path, Schema)
		}
	}
}

// Every Quartermaster domain type in the registry has exactly one legacy
// service event that produces it, with the registered scope.
func TestDomainForCoversEveryQuartermasterDomainType(t *testing.T) {
	const tenantID = "11111111-1111-4111-8111-111111111111"
	legacy := map[string]string{
		"tenant_created":                 "tenant.created",
		"tenant_updated":                 "tenant.updated",
		"tenant_deleted":                 "tenant.deleted",
		"tenant_cluster_assigned":        "tenant.cluster_assigned",
		"tenant_cluster_unassigned":      "tenant.cluster_unassigned",
		"cluster_created":                "cluster.created",
		"cluster_updated":                "cluster.updated",
		"cluster_invite_revoked":         "cluster.invite_revoked",
		"cluster_subscription_requested": "cluster.subscription_requested",
		"cluster_subscription_approved":  "cluster.subscription_approved",
		"cluster_subscription_rejected":  "cluster.subscription_rejected",
	}
	produced := map[string]bool{"cluster.invite_created": true}
	for eventType, wantType := range legacy {
		event := &ipcpb.ServiceEvent{
			EventType: eventType, TenantId: tenantID,
			Payload: &ipcpb.ServiceEvent_ClusterEvent{ClusterEvent: &ipcpb.ClusterEvent{
				ClusterId: "cluster-a", InviteId: "invite-a", SubscriptionId: "subscription-a",
			}},
		}
		domain, err := DomainFor(event)
		if err != nil || domain == nil {
			t.Fatalf("%s: domain = %v, err = %v", eventType, domain, err)
		}
		spec, ok := events.SpecFor(domain.Message)
		if !ok || spec.Type != wantType {
			t.Fatalf("%s maps to %q, want %q", eventType, spec.Type, wantType)
		}
		if platform := spec.Scope == eventspb.Scope_SCOPE_PLATFORM; platform != (domain.TenantID == "") {
			t.Fatalf("%s: scope %v with envelope tenant %q", eventType, spec.Scope, domain.TenantID)
		}
		if domain.AggregateID == "" {
			t.Fatalf("%s has no aggregate id", eventType)
		}
		if _, err := events.New(Source, domain.TenantID, domain.AggregateID, domain.Message); err != nil {
			t.Fatalf("%s: %v", eventType, err)
		}
		produced[spec.Type] = true
	}
	var registered []string
	for _, spec := range events.Specs() {
		if strings.HasPrefix(spec.Type, "tenant.") || strings.HasPrefix(spec.Type, "cluster.") {
			registered = append(registered, spec.Type)
			if !produced[spec.Type] {
				t.Errorf("registered Quartermaster type %s has no producing service event", spec.Type)
			}
		}
	}
	sort.Strings(registered)
	if len(registered) != len(produced) {
		t.Fatalf("registered Quartermaster types %v, produced %v", registered, produced)
	}
}

func TestDomainForLeavesAuditsLegacyOnlyAndRefusesIncompleteInvites(t *testing.T) {
	for _, eventType := range []string{"cluster_access_granted", "cluster_access_materialized", "cluster_access_revoked"} {
		domain, err := DomainFor(&ipcpb.ServiceEvent{EventType: eventType})
		if err != nil || domain != nil {
			t.Fatalf("%s: domain = %v, err = %v", eventType, domain, err)
		}
	}
	if _, err := DomainFor(&ipcpb.ServiceEvent{EventType: "cluster_invite_created"}); err == nil {
		t.Fatal("cluster_invite_created was mapped without its invited tenant")
	}
}
