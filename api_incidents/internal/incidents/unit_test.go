package incidents

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"frameworks/api_incidents/internal/database/lookoutdb"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAlertmanagerWebhookDecodeAndFacts(t *testing.T) {
	raw := `{
		"version": "4",
		"groupKey": "{}/{}:{alertname=\"EdgeDown\", cluster=\"c1\", region=\"eu\"}",
		"status": "firing",
		"receiver": "lookout",
		"groupLabels": {"alertname": "EdgeDown", "cluster": "c1", "region": "eu"},
		"commonLabels": {"alertname": "EdgeDown", "severity": "critical"},
		"commonAnnotations": {"summary": "Edge down", "description": "Edge stopped reporting"},
		"alerts": [{
			"status": "firing",
			"labels": {"alertname": "EdgeDown", "severity": "critical"},
			"annotations": {},
			"startsAt": "2026-09-15T10:00:00.123456789Z",
			"endsAt": "0001-01-01T00:00:00Z",
			"generatorURL": "http://vmalert",
			"fingerprint": "abc"
		}]
	}`
	var hook AlertmanagerWebhook
	if err := json.Unmarshal([]byte(raw), &hook); err != nil {
		t.Fatal(err)
	}
	if err := hook.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	facts := hook.facts()
	if facts.ClusterID != "c1" || facts.Region != "eu" || facts.Alertname != "EdgeDown" || facts.Title != "Edge down" || facts.Summary != "Edge stopped reporting" {
		t.Fatalf("facts = %+v", facts)
	}
	alert := hook.Alerts[0]
	if got := alert.normalizedStart(); got.Nanosecond() != 123456000 {
		t.Fatalf("normalizedStart nanos = %d, want microsecond truncation", got.Nanosecond())
	}
	if alert.normalizedEnd() != nil {
		t.Fatal("firing alert must have no end time")
	}
}

func TestAlertmanagerWebhookValidateRejects(t *testing.T) {
	start := time.Now()
	cases := map[string]AlertmanagerWebhook{
		"missing group key":   {Alerts: []AlertmanagerAlert{{Status: "firing", Fingerprint: "a", StartsAt: start}}},
		"no alerts":           {GroupKey: "g"},
		"missing fingerprint": {GroupKey: "g", Alerts: []AlertmanagerAlert{{Status: "firing", StartsAt: start}}},
		"bad status":          {GroupKey: "g", Alerts: []AlertmanagerAlert{{Status: "pending", Fingerprint: "a", StartsAt: start}}},
		"missing startsAt":    {GroupKey: "g", Alerts: []AlertmanagerAlert{{Status: "firing", Fingerprint: "a"}}},
	}
	for name, hook := range cases {
		if err := hook.Validate(); !errors.Is(err, ErrInvalidWebhook) {
			t.Errorf("%s: err = %v, want ErrInvalidWebhook", name, err)
		}
	}
}

func TestAlertmanagerFactsUsePerAlertAnnotationsAndLabels(t *testing.T) {
	hook := AlertmanagerWebhook{
		GroupKey:    "service-down",
		GroupLabels: map[string]string{"alertname": "ServiceDown"},
		Alerts: []AlertmanagerAlert{
			{
				Status:      alertStatusFiring,
				Labels:      map[string]string{"alertname": "ServiceDown", "cluster": "platform", "region": "eu-west"},
				Annotations: map[string]string{"summary": "signalman on regional-eu-1 is unreachable", "description": "VictoriaMetrics cannot scrape 127.0.0.1:18013."},
			},
		},
	}
	facts := hook.facts()
	if facts.Title != "signalman on regional-eu-1 is unreachable" || facts.Summary != "VictoriaMetrics cannot scrape 127.0.0.1:18013." {
		t.Fatalf("facts text = %+v", facts)
	}
	if facts.ClusterID != "platform" || facts.Region != "eu-west" {
		t.Fatalf("facts placement = %+v", facts)
	}
}

func TestAlertmanagerFactsDescribeGroupedTargets(t *testing.T) {
	hook := AlertmanagerWebhook{
		GroupKey:    "service-down",
		GroupLabels: map[string]string{"alertname": "ServiceDown", "cluster": "platform", "region": "eu-west"},
		Alerts: []AlertmanagerAlert{
			{Status: alertStatusFiring, Annotations: map[string]string{"summary": "signalman on regional-eu-1 is unreachable", "description": "Cannot scrape signalman on regional-eu-1."}},
			{Status: alertStatusFiring, Annotations: map[string]string{"summary": "decklog on regional-eu-2 is unreachable", "description": "Cannot scrape decklog on regional-eu-2."}},
		},
	}
	facts := hook.facts()
	if facts.Title != "Service Down affects 2 targets" {
		t.Fatalf("title = %q", facts.Title)
	}
	if facts.Summary != "Cannot scrape signalman on regional-eu-1.\nCannot scrape decklog on regional-eu-2." {
		t.Fatalf("summary = %q", facts.Summary)
	}
}

func TestAlertmanagerFactsHumanizeMissingAnnotations(t *testing.T) {
	hook := AlertmanagerWebhook{
		GroupKey:    "service-down",
		GroupLabels: map[string]string{"alertname": "ServiceDown", "cluster": "platform"},
		Alerts:      []AlertmanagerAlert{{Status: alertStatusFiring}},
	}
	facts := hook.facts()
	if facts.Title != "Service Down" || facts.Summary != "Service Down" {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestAlertmanagerFactsUseDiagnosticLabelsWithoutAnnotations(t *testing.T) {
	hook := AlertmanagerWebhook{
		GroupKey:    "service-down",
		GroupLabels: map[string]string{"alertname": "ServiceDown"},
		Alerts: []AlertmanagerAlert{{
			Status: alertStatusFiring,
			Labels: map[string]string{"frameworks_service": "signalman", "node_id": "regional-eu-1"},
		}},
	}
	facts := hook.facts()
	if facts.Title != "Service Down: signalman on regional-eu-1" || facts.Summary != facts.Title {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestAlertmanagerFactsRejectTautologicalAnnotations(t *testing.T) {
	hook := AlertmanagerWebhook{
		GroupKey:          "service-down",
		GroupLabels:       map[string]string{"alertname": "ServiceDown"},
		CommonAnnotations: map[string]string{"summary": "ServiceDown", "description": "ServiceDown"},
		Alerts: []AlertmanagerAlert{{
			Status: alertStatusFiring,
			Labels: map[string]string{"frameworks_service": "signalman", "node_id": "regional-eu-1"},
		}},
	}
	facts := hook.facts()
	if facts.Title != "Service Down: signalman on regional-eu-1" || facts.Summary != facts.Title {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestAlertmanagerFactsRetainResolvedAlertAnnotations(t *testing.T) {
	hook := AlertmanagerWebhook{
		GroupKey:    "service-down",
		GroupLabels: map[string]string{"alertname": "ServiceDown"},
		Alerts: []AlertmanagerAlert{{
			Status:      alertStatusResolved,
			Annotations: map[string]string{"summary": "signalman on regional-eu-1 is reachable again", "description": "The metrics endpoint recovered."},
		}},
	}
	facts := hook.facts()
	if facts.Title != "signalman on regional-eu-1 is reachable again" || facts.Summary != "The metrics endpoint recovered." {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestHigherSeverity(t *testing.T) {
	if got := higherSeverity("warning", "critical"); got != "critical" {
		t.Fatalf("got %q", got)
	}
	if got := higherSeverity("critical", "warning"); got != "critical" {
		t.Fatalf("got %q", got)
	}
	if got := higherSeverity("", "custom"); got != "custom" {
		t.Fatalf("got %q", got)
	}
	if got := higherSeverity("info", "custom"); got != "info" {
		t.Fatalf("got %q", got)
	}
}

type fakeClusterGetter struct {
	calls   int
	cluster *quartermasterpb.InfrastructureCluster
	err     error
}

func (f *fakeClusterGetter) GetCluster(context.Context, string) (*quartermasterpb.ClusterResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &quartermasterpb.ClusterResponse{Cluster: f.cluster}, nil
}

func TestReconcileClusterScopeRequiresVerifiedOwner(t *testing.T) {
	owners := &switchableOwners{}
	owners.fail()
	// A nil DB proves no scope or incident is written before the owner is verified.
	svc := &Service{Owners: owners}
	moved, err := svc.ReconcileClusterScope(context.Background(), "c1")
	if !errors.Is(err, ErrScopeUnverified) || moved != 0 {
		t.Fatalf("reconcile = %d, %v; want ErrScopeUnverified", moved, err)
	}
	if moved, err := svc.ReconcileClusterScope(context.Background(), " "); err != nil || moved != 0 {
		t.Fatalf("reconcile without cluster = %d, %v; want no-op", moved, err)
	}
}

func TestQuartermasterOwners(t *testing.T) {
	owner := "10000000-0000-0000-0000-000000000001"
	updated := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

	t.Run("tenant-owned cluster is tenant scope with its updated_at", func(t *testing.T) {
		getter := &fakeClusterGetter{cluster: &quartermasterpb.InfrastructureCluster{OwnerTenantId: &owner, UpdatedAt: timestamppb.New(updated)}}
		got, err := (&QuartermasterOwners{Clusters: getter}).LookupOwner(context.Background(), "c1")
		if err != nil || got.Scope != (Scope{Kind: ScopeTenant, TenantID: owner}) || !got.UpdatedAt.Equal(updated) {
			t.Fatalf("owner = %+v, %v", got, err)
		}
	})

	t.Run("platform-official cluster with an owner is platform scope", func(t *testing.T) {
		getter := &fakeClusterGetter{cluster: &quartermasterpb.InfrastructureCluster{OwnerTenantId: &owner, IsPlatformOfficial: true, UpdatedAt: timestamppb.New(updated)}}
		got, err := (&QuartermasterOwners{Clusters: getter}).LookupOwner(context.Background(), "c1")
		if err != nil || got.Scope != (Scope{Kind: ScopePlatform}) {
			t.Fatalf("owner = %+v, %v", got, err)
		}
	})

	t.Run("not found is platform scope without updated_at", func(t *testing.T) {
		getter := &fakeClusterGetter{err: status.Error(codes.NotFound, "missing")}
		got, err := (&QuartermasterOwners{Clusters: getter}).LookupOwner(context.Background(), "c1")
		if err != nil || got.Scope != (Scope{Kind: ScopePlatform}) || !got.UpdatedAt.IsZero() {
			t.Fatalf("owner = %+v, %v", got, err)
		}
	})

	t.Run("lookup failure is an error", func(t *testing.T) {
		getter := &fakeClusterGetter{err: status.Error(codes.Unavailable, "down")}
		if got, err := (&QuartermasterOwners{Clusters: getter}).LookupOwner(context.Background(), "c1"); err == nil {
			t.Fatalf("owner = %+v, want error", got)
		}
	})

	t.Run("no Quartermaster client is an error", func(t *testing.T) {
		if got, err := (&QuartermasterOwners{}).LookupOwner(context.Background(), "c1"); err == nil {
			t.Fatalf("owner = %+v, want error", got)
		}
	})
}

func base64Cursor(createdAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "|" + id))
}

func TestCursorRoundTrip(t *testing.T) {
	at, id, err := decodeCursor(base64Cursor("2026-09-15T10:00:00.123456Z", "10000000-0000-0000-0000-000000000001"))
	if err != nil || !at.Valid || !id.Valid {
		t.Fatalf("decode = %v %v %v", at, id, err)
	}
	if _, _, err := decodeCursor("not-a-cursor"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v", err)
	}
}

func TestPublishSeparatesTenantAndOperatorAudiences(t *testing.T) {
	const (
		ownerA = "10000000-0000-0000-0000-00000000000a"
		ownerB = "10000000-0000-0000-0000-00000000000b"
	)
	incident := func(scope, tenantID string) lookoutdb.LookoutIncident {
		return lookoutdb.LookoutIncident{
			ID:        "incident-1",
			Scope:     scope,
			TenantID:  sql.NullString{String: tenantID, Valid: tenantID != ""},
			ClusterID: "cluster-1",
			Status:    StatusFiring,
			Severity:  "critical",
			Title:     "Edge node down",
			UpdatedAt: time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC),
		}
	}
	cases := []struct {
		name    string
		changes []realtimeChange
		want    []realtimeRecipient
	}{
		{
			name:    "platform incident reaches operators only",
			changes: []realtimeChange{{incident: incident(ScopePlatform, ""), change: changeOpened}},
			want:    []realtimeRecipient{{audience: operatorAudience, change: changeOpened}},
		},
		{
			name:    "tenant incident reaches its tenant and operators",
			changes: []realtimeChange{{incident: incident(ScopeTenant, ownerA), change: EventAcknowledged}},
			want: []realtimeRecipient{
				{audience: ownerA, owner: ownerA, change: EventAcknowledged},
				{audience: operatorAudience, owner: ownerA, change: EventAcknowledged},
			},
		},
		{
			name: "move between tenants reaches operators once",
			changes: []realtimeChange{
				{incident: incident(ScopeTenant, ownerB), change: changeScopeChanged, recipientTenantID: ownerA},
				{incident: incident(ScopeTenant, ownerB), change: changeScopeChanged},
			},
			want: []realtimeRecipient{
				{audience: ownerA, owner: ownerA, change: changeScopeChanged},
				{audience: ownerB, owner: ownerB, change: changeScopeChanged},
				{audience: operatorAudience, owner: ownerB, change: changeScopeChanged},
			},
		},
		{
			name: "move to platform scope tells the former tenant and operators",
			changes: []realtimeChange{
				{incident: incident(ScopePlatform, ""), change: changeScopeChanged, recipientTenantID: ownerB},
			},
			want: []realtimeRecipient{
				{audience: ownerB, owner: ownerB, change: changeScopeChanged},
				{audience: operatorAudience, change: changeScopeChanged},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			realtime := &recordingRealtime{}
			svc := &Service{Realtime: realtime}
			svc.publish(tc.changes)
			requireAudiences(t, realtime)
			if got := realtime.recipients(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("recipients = %+v, want %+v", got, tc.want)
			}
		})
	}
}
