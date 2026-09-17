//go:build schema_verify

package incidents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_incidents/internal/database/lookoutdb"
	"frameworks/api_incidents/internal/lookouttest"
)

const (
	tenantA    = "10000000-0000-0000-0000-00000000000a"
	tenantB    = "10000000-0000-0000-0000-00000000000b"
	userA      = "20000000-0000-0000-0000-00000000000a"
	userB      = "20000000-0000-0000-0000-00000000000b"
	operatorID = "20000000-0000-0000-0000-0000000000ff"
)

func newRealService(t *testing.T, db *sql.DB) (*Service, *sql.DB, *recordingRealtime) {
	t.Helper()
	realtime := &recordingRealtime{}
	svc := &Service{
		DB: db,
		Owners: fakeOwners{
			"tenant-a-cluster": {Kind: ScopeTenant, TenantID: tenantA},
			"tenant-b-cluster": {Kind: ScopeTenant, TenantID: tenantB},
		},
		Router:   allChannelsRouter{},
		Realtime: realtime,
	}
	return svc, db, realtime
}

func mustIngest(t *testing.T, svc *Service, hook AlertmanagerWebhook, wantOutcome string) IngestResult {
	t.Helper()
	result, err := svc.IngestAlertmanager(context.Background(), hook)
	if err != nil {
		t.Fatalf("ingest %s: %v", hook.GroupKey, err)
	}
	if result.Outcome != wantOutcome {
		t.Fatalf("ingest %s outcome = %q, want %q", hook.GroupKey, result.Outcome, wantOutcome)
	}
	return result
}

func mustGet(t *testing.T, svc *Service, access Access, id string) IncidentDetail {
	t.Helper()
	detail, err := svc.Get(context.Background(), access, id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return detail
}

func queryStrings(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func outboxChannels(t *testing.T, db *sql.DB, incidentID string) []string {
	return queryStrings(t, db, `SELECT channel FROM lookout.notification_outbox WHERE incident_id = $1 ORDER BY channel`, incidentID)
}

func eventKinds(t *testing.T, db *sql.DB, incidentID string) []string {
	return queryStrings(t, db, `SELECT kind FROM lookout.incident_events WHERE incident_id = $1 ORDER BY created_at, id`, incidentID)
}

func incidentCount(t *testing.T, db *sql.DB, groupKey string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lookout.incidents WHERE group_key = $1`, groupKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertStrings(t *testing.T, label string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

var unrestricted = Access{Unrestricted: true, ActorUserID: operatorID}

func TestLookoutIngestStateMachine_RealPG(t *testing.T) {
	runIngestStateMachine(t, lookouttest.StartPostgres(t))
}

func TestLookoutIngestStateMachine_RealYugabyte(t *testing.T) {
	runIngestStateMachine(t, lookouttest.StartYugabyte(t))
}

func runIngestStateMachine(t *testing.T, database *sql.DB) {
	svc, db, realtime := newRealService(t, database)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 15, 10, 0, 0, 123456789, time.UTC)
	const group = "platform-group"

	created := mustIngest(t, svc, testWebhook(group, "platform-cluster", testAlert("fp1", alertStatusFiring, "warning", t0)), IngestCreated)
	inc := mustGet(t, svc, unrestricted, created.IncidentID).Incident
	if inc.Scope != ScopePlatform || inc.TenantID.Valid || inc.Status != StatusFiring || inc.Severity != "warning" ||
		inc.ClusterID != "platform-cluster" || inc.Region != "eu-west" || inc.Title != "Edge node down" {
		t.Fatalf("created incident = %+v", inc)
	}
	assertStrings(t, "opened warning notifications", outboxChannels(t, db, created.IncidentID), []string{ChannelDiscord, ChannelSlack})

	repeat := mustIngest(t, svc, testWebhook(group, "platform-cluster", testAlert("fp1", alertStatusFiring, "warning", t0)), IngestUnchanged)
	if repeat.IncidentID != created.IncidentID {
		t.Fatalf("repeat opened %s, want dedupe into %s", repeat.IncidentID, created.IncidentID)
	}
	assertStrings(t, "timeline after repeat", eventKinds(t, db, created.IncidentID), []string{EventAlertFiring})
	repairedHook := testWebhook(group, "platform-cluster", testAlert("fp1", alertStatusFiring, "warning", t0))
	repairedHook.GroupLabels["region"] = "eu-central"
	repairedHook.CommonAnnotations = map[string]string{
		"summary":     "Signalman on regional-eu-1 is unreachable",
		"description": "VictoriaMetrics cannot scrape the Signalman metrics endpoint.",
	}
	mustIngest(t, svc, repairedHook, IngestUnchanged)
	repaired := mustGet(t, svc, unrestricted, created.IncidentID).Incident
	if repaired.Region != "eu-central" || repaired.Title != "Signalman on regional-eu-1 is unreachable" || repaired.Summary != "VictoriaMetrics cannot scrape the Signalman metrics endpoint." {
		t.Fatalf("repaired incident = %+v", repaired)
	}

	mustIngest(t, svc, testWebhook(group, "platform-cluster",
		testAlert("fp1", alertStatusFiring, "warning", t0),
		testAlert("fp2", alertStatusFiring, "critical", t0.Add(time.Minute)),
	), IngestUpdated)
	if got := mustGet(t, svc, unrestricted, created.IncidentID).Incident.Severity; got != "critical" {
		t.Fatalf("severity after critical alert joined = %q", got)
	}
	if page, err := svc.List(ctx, unrestricted, ListFilter{}); err != nil || page.FiringAlertCounts[created.IncidentID] != 2 {
		t.Fatalf("listed firing alert counts = %v, %v; want 2", page.FiringAlertCounts, err)
	}

	mustIngest(t, svc, testWebhook(group, "platform-cluster",
		testAlert("fp1", alertStatusResolved, "warning", t0),
		testAlert("fp2", alertStatusFiring, "critical", t0.Add(time.Minute)),
	), IngestUpdated)
	stillFiring := mustGet(t, svc, unrestricted, created.IncidentID).Incident
	if stillFiring.Status != StatusFiring {
		t.Fatalf("status with one alert still firing = %q", stillFiring.Status)
	}
	if firing, err := svc.FiringAlertCount(ctx, stillFiring); err != nil || firing != 1 {
		t.Fatalf("firing alert count = %d, %v; want 1", firing, err)
	}

	mustIngest(t, svc, testWebhook(group, "platform-cluster",
		testAlert("fp1", alertStatusResolved, "warning", t0),
		testAlert("fp2", alertStatusResolved, "critical", t0.Add(time.Minute)),
	), IngestResolved)
	resolved := mustGet(t, svc, unrestricted, created.IncidentID)
	if resolved.Incident.Status != StatusResolved || resolved.Incident.Resolution.String != ResolutionAuto || !resolved.Incident.ResolvedAt.Valid {
		t.Fatalf("auto-resolved incident = %+v", resolved.Incident)
	}
	assertStrings(t, "auto-resolve timeline", eventKinds(t, db, created.IncidentID),
		[]string{EventAlertFiring, EventAlertFiring, EventAlertResolved, EventAlertResolved, EventResolved})
	assertStrings(t, "opened + critical resolved notifications", outboxChannels(t, db, created.IncidentID),
		[]string{ChannelDiscord, ChannelDiscord, ChannelEmail, ChannelSlack, ChannelSlack})

	mustIngest(t, svc, testWebhook(group, "platform-cluster", testAlert("fp2", alertStatusResolved, "critical", t0.Add(time.Minute))), IngestIgnored)
	mustIngest(t, svc, testWebhook(group, "platform-cluster", testAlert("fp2", alertStatusFiring, "critical", t0.Add(time.Minute))), IngestIgnored)

	reopened := mustIngest(t, svc, testWebhook(group, "platform-cluster", testAlert("fp2", alertStatusFiring, "critical", t0.Add(30*time.Minute))), IngestCreated)
	if reopened.IncidentID == created.IncidentID {
		t.Fatal("resolved->firing transition reused the resolved incident")
	}
	manual, err := svc.Resolve(ctx, unrestricted, reopened.IncidentID)
	if err != nil {
		t.Fatal(err)
	}
	if manual.Status != StatusResolved || manual.Resolution.String != ResolutionManual || manual.ResolvedBy.String != operatorID {
		t.Fatalf("manually resolved incident = %+v", manual)
	}
	mustIngest(t, svc, testWebhook(group, "platform-cluster", testAlert("fp2", alertStatusFiring, "critical", t0.Add(30*time.Minute))), IngestIgnored)
	if got := incidentCount(t, db, group); got != 2 {
		t.Fatalf("incidents after repeats of a manually resolved incident = %d, want 2", got)
	}

	third := mustIngest(t, svc, testWebhook(group, "platform-cluster",
		testAlert("fp2", alertStatusFiring, "critical", t0.Add(30*time.Minute)),
		testAlert("fp3", alertStatusFiring, "warning", t0.Add(40*time.Minute)),
	), IngestCreated)
	if got := len(mustGet(t, svc, unrestricted, third.IncidentID).Alerts); got != 2 {
		t.Fatalf("new-fingerprint incident alerts = %d, want 2", got)
	}
	if got := incidentCount(t, db, group); got != 3 {
		t.Fatalf("incidents after new fingerprint = %d, want 3", got)
	}
	requireAudiences(t, realtime)
	platformEvents := realtime.recipients()
	if len(platformEvents) == 0 {
		t.Fatal("platform incident changes published no operator events")
	}
	for _, recipient := range platformEvents {
		if recipient.audience != operatorAudience || recipient.owner != "" {
			t.Fatalf("platform incident change sent to %+v, want the operator audience only", recipient)
		}
	}
	operatorChanges := []string{}
	for _, recipient := range platformEvents {
		operatorChanges = append(operatorChanges, recipient.change)
	}
	assertStrings(t, "platform incident operator changes", operatorChanges, []string{
		changeOpened, EventAlertFiring, EventAlertResolved, EventResolved, changeOpened, EventResolved, changeOpened,
	})

	tenantIncident := mustIngest(t, svc, testWebhook("tenant-group", "tenant-a-cluster", testAlert("fp9", alertStatusFiring, "critical", t0)), IngestCreated)
	tenantRow := mustGet(t, svc, Access{TenantID: tenantA}, tenantIncident.IncidentID).Incident
	if tenantRow.Scope != ScopeTenant || tenantRow.TenantID.String != tenantA {
		t.Fatalf("tenant incident = %+v", tenantRow)
	}
	assertStrings(t, "tenant incident deliveries", outboxChannels(t, db, tenantIncident.IncidentID), []string{ChannelKafka})
	var rawPayload []byte
	if err := db.QueryRow(`SELECT payload FROM lookout.notification_outbox WHERE incident_id = $1 AND tenant_id = $2`, tenantIncident.IncidentID, tenantA).Scan(&rawPayload); err != nil {
		t.Fatal(err)
	}
	var published KafkaIncidentMessage
	if err := json.Unmarshal(rawPayload, &published); err != nil {
		t.Fatal(err)
	}
	wantPublished := KafkaIncidentMessage{IncidentID: tenantIncident.IncidentID, TenantID: tenantA, ClusterID: "tenant-a-cluster", Severity: "critical", Summary: "An edge node stopped reporting."}
	if published != wantPublished {
		t.Fatalf("lookout.incidents payload = %+v, want %+v", published, wantPublished)
	}
	requireAudiences(t, realtime)
	events := realtime.snapshot()[len(platformEvents):]
	if len(events) != 2 || events[0].GetTenantId() != tenantA || events[0].GetEventType() != realtimeEventType ||
		events[0].GetIncidentEvent().GetChange() != changeOpened || events[0].GetIncidentEvent().GetIncidentId() != tenantIncident.IncidentID {
		t.Fatalf("tenant realtime events = %v", events)
	}
	if got := realtime.recipients()[len(platformEvents)+1]; got != (realtimeRecipient{audience: operatorAudience, owner: tenantA, change: changeOpened}) {
		t.Fatalf("operator event for the tenant incident = %+v", got)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.IngestAlertmanager(ctx, testWebhook("race-group", "platform-cluster", testAlert("race", alertStatusFiring, "warning", t0)))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ingest: %v", err)
		}
	}
	if got := incidentCount(t, db, "race-group"); got != 1 {
		t.Fatalf("concurrent first notifications opened %d incidents, want 1", got)
	}
}

func TestLookoutIncidentActions_RealPG(t *testing.T) {
	runIncidentActions(t, lookouttest.StartPostgres(t))
}

func TestLookoutIncidentActions_RealYugabyte(t *testing.T) {
	runIncidentActions(t, lookouttest.StartYugabyte(t))
}

func runIncidentActions(t *testing.T, database *sql.DB) {
	svc, db, realtime := newRealService(t, database)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	tenantHook := testWebhook("tenant-actions", "tenant-a-cluster", testAlert("t1", alertStatusFiring, "warning", t0))
	tenantRes := mustIngest(t, svc, tenantHook, IngestCreated)
	platformRes := mustIngest(t, svc, testWebhook("platform-actions", "platform-cluster", testAlert("p1", alertStatusFiring, "critical", t0)), IngestCreated)
	tenantAccess := Access{TenantID: tenantA, ActorUserID: userA}
	otherTenant := Access{TenantID: tenantB, ActorUserID: userB}

	if _, err := svc.Get(ctx, otherTenant, tenantRes.IncidentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant get = %v, want ErrNotFound", err)
	}
	if _, err := svc.Get(ctx, tenantAccess, platformRes.IncidentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant get platform incident = %v, want ErrNotFound", err)
	}
	if _, err := svc.Acknowledge(ctx, otherTenant, tenantRes.IncidentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant acknowledge = %v, want ErrNotFound", err)
	}
	if _, err := svc.Resolve(ctx, tenantAccess, platformRes.IncidentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant resolve platform incident = %v, want ErrNotFound", err)
	}
	if _, err := svc.List(ctx, tenantAccess, ListFilter{Scope: ScopePlatform}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("tenant platform list = %v, want ErrPermissionDenied", err)
	}
	if _, err := svc.List(ctx, Access{}, ListFilter{}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("tenantless restricted list = %v, want ErrPermissionDenied", err)
	}
	listIDs := func(access Access, filter ListFilter) []string {
		t.Helper()
		page, err := svc.List(ctx, access, filter)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, inc := range page.Incidents {
			ids = append(ids, inc.ID)
		}
		return ids
	}
	assertStrings(t, "tenant A list", listIDs(tenantAccess, ListFilter{}), []string{tenantRes.IncidentID})
	assertStrings(t, "tenant B list", listIDs(otherTenant, ListFilter{}), []string{})
	assertStrings(t, "operator platform list", listIDs(unrestricted, ListFilter{Scope: ScopePlatform}), []string{platformRes.IncidentID})
	assertStrings(t, "operator tenant filter", listIDs(unrestricted, ListFilter{TenantID: tenantA}), []string{tenantRes.IncidentID})
	if got := len(listIDs(unrestricted, ListFilter{})); got != 2 {
		t.Fatalf("operator list = %d incidents, want 2", got)
	}

	acked, err := svc.Acknowledge(ctx, tenantAccess, tenantRes.IncidentID)
	if err != nil || acked.Status != StatusAcknowledged || acked.AcknowledgedBy.String != userA {
		t.Fatalf("acknowledge = %+v, %v", acked, err)
	}
	if again, err := svc.Acknowledge(ctx, tenantAccess, tenantRes.IncidentID); err != nil || again.Status != StatusAcknowledged {
		t.Fatalf("repeat acknowledge = %+v, %v", again, err)
	}
	mustIngest(t, svc, tenantHook, IngestUnchanged)
	if got := mustGet(t, svc, tenantAccess, tenantRes.IncidentID).Incident.Status; got != StatusAcknowledged {
		t.Fatalf("status after repeat notification = %q, want acknowledged", got)
	}

	if _, err := svc.Assign(ctx, tenantAccess, tenantRes.IncidentID, "not-a-uuid"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("assign invalid user = %v", err)
	}
	if assigned, err := svc.Assign(ctx, tenantAccess, tenantRes.IncidentID, userB); err != nil || assigned.AssignedTo.String != userB {
		t.Fatalf("assign = %+v, %v", assigned, err)
	}
	if _, err := svc.AddNote(ctx, tenantAccess, tenantRes.IncidentID, "   "); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty note = %v", err)
	}
	if _, err := svc.AddNote(ctx, tenantAccess, tenantRes.IncidentID, "Restarted the edge"); err != nil {
		t.Fatal(err)
	}

	const reportID = "30000000-0000-0000-0000-000000000001"
	for i := 0; i < 2; i++ {
		if _, err := svc.AttachInvestigation(ctx, tenantRes.IncidentID, tenantA, reportID); err != nil {
			t.Fatalf("attach investigation %d: %v", i, err)
		}
	}
	if _, err := svc.AttachInvestigation(ctx, tenantRes.IncidentID, tenantB, reportID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attach with another tenant = %v, want ErrNotFound", err)
	}
	if _, err := svc.AttachInvestigation(ctx, platformRes.IncidentID, tenantA, reportID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attach to platform incident = %v, want ErrNotFound", err)
	}

	resolved, err := svc.Resolve(ctx, tenantAccess, tenantRes.IncidentID)
	if err != nil || resolved.Resolution.String != ResolutionManual || resolved.ResolvedBy.String != userA {
		t.Fatalf("resolve = %+v, %v", resolved, err)
	}
	if _, err := svc.Resolve(ctx, tenantAccess, tenantRes.IncidentID); err != nil {
		t.Fatalf("repeat resolve = %v", err)
	}
	if _, err := svc.Acknowledge(ctx, tenantAccess, tenantRes.IncidentID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("acknowledge resolved = %v, want ErrInvalidState", err)
	}
	if _, err := svc.Assign(ctx, tenantAccess, tenantRes.IncidentID, userA); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("assign resolved = %v, want ErrInvalidState", err)
	}
	assertStrings(t, "tenant incident deliveries", outboxChannels(t, db, tenantRes.IncidentID), []string{ChannelKafka})

	detail := mustGet(t, svc, tenantAccess, tenantRes.IncidentID)
	kinds := []string{}
	for _, event := range detail.Events {
		kinds = append(kinds, event.Kind)
	}
	assertStrings(t, "tenant timeline", kinds, []string{EventAlertFiring, EventAcknowledged, EventAssigned, EventNote, EventInvestigationAttached, EventResolved})
	if detail.Events[1].ActorUserID.String != userA || len(detail.Alerts) != 1 {
		t.Fatalf("detail events/alerts = %+v / %+v", detail.Events, detail.Alerts)
	}
	requireAudiences(t, realtime)
	tenantChanges, operatorChanges := []string{}, []string{}
	for _, recipient := range realtime.recipients() {
		switch recipient.audience {
		case tenantA:
			tenantChanges = append(tenantChanges, recipient.change)
		case operatorAudience:
			operatorChanges = append(operatorChanges, recipient.owner+":"+recipient.change)
		default:
			t.Fatalf("realtime event for %+v, want tenant A or operators", recipient)
		}
	}
	allTenantChanges := []string{changeOpened, EventAcknowledged, EventAssigned, EventNote, EventInvestigationAttached, EventResolved}
	assertStrings(t, "tenant realtime changes", tenantChanges, allTenantChanges)
	wantOperator := []string{tenantA + ":" + changeOpened, ":" + changeOpened}
	for _, change := range allTenantChanges[1:] {
		wantOperator = append(wantOperator, tenantA+":"+change)
	}
	assertStrings(t, "operator realtime changes", operatorChanges, wantOperator)

	before := len(realtime.snapshot())
	if _, err := svc.Resolve(ctx, unrestricted, platformRes.IncidentID); err != nil {
		t.Fatal(err)
	}
	if got := realtime.recipients()[before:]; !reflect.DeepEqual(got, []realtimeRecipient{{audience: operatorAudience, change: EventResolved}}) {
		t.Fatalf("realtime events for an operator resolving a platform incident = %+v, want the operator audience only", got)
	}
	assertStrings(t, "platform opened + resolved notifications", outboxChannels(t, db, platformRes.IncidentID),
		[]string{ChannelDiscord, ChannelDiscord, ChannelEmail, ChannelEmail, ChannelSlack, ChannelSlack})

	mustIngest(t, svc, testWebhook("platform-pagination", "platform-cluster", testAlert("p2", alertStatusFiring, "warning", t0)), IngestCreated)
	first, err := svc.List(ctx, unrestricted, ListFilter{First: 2})
	if err != nil || len(first.Incidents) != 2 || !first.HasNextPage || first.TotalCount != 3 {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := svc.List(ctx, unrestricted, ListFilter{First: 2, After: first.EndCursor})
	if err != nil || len(second.Incidents) != 1 || second.HasNextPage {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	seen := map[string]bool{}
	for _, inc := range append(append([]lookoutdb.LookoutIncident(nil), first.Incidents...), second.Incidents...) {
		if seen[inc.ID] {
			t.Fatalf("incident %s appeared on two pages", inc.ID)
		}
		seen[inc.ID] = true
	}
}

func TestLookoutIncidentRescope_RealPG(t *testing.T) {
	runIncidentRescope(t, lookouttest.StartPostgres(t))
}

func TestLookoutIncidentRescope_RealYugabyte(t *testing.T) {
	runIncidentRescope(t, lookouttest.StartYugabyte(t))
}

func TestLookoutOwnershipIngestRace_RealPG(t *testing.T) {
	runOwnershipIngestRace(t, lookouttest.StartPostgres(t))
}

func TestLookoutOwnershipIngestRace_RealYugabyte(t *testing.T) {
	runOwnershipIngestRace(t, lookouttest.StartYugabyte(t))
}

// runOwnershipIngestRace models two Lookout replicas sharing the database. One
// ingests an alert for a cluster whose stored owner is tenant A and pauses
// after reading that scope; the other reconciles the cluster to tenant B
// meanwhile. The incident must end with tenant B whichever finishes first, and
// a later notification on the first replica, whose Quartermaster still answers
// tenant A, must not move it back.
func runOwnershipIngestRace(t *testing.T, db *sql.DB) {
	ctx := context.Background()
	const cluster = "race-cluster"
	ingester := &Service{DB: db, Owners: fakeOwners{cluster: {Kind: ScopeTenant, TenantID: tenantA}}, Router: allChannelsRouter{}}
	reconcilerOwners := &switchableOwners{}
	reconcilerOwners.set(Scope{Kind: ScopeTenant, TenantID: tenantB})
	reconciler := &Service{DB: db, Owners: reconcilerOwners, Router: allChannelsRouter{}}

	if _, err := ingester.ReconcileClusterScope(ctx, cluster); err != nil {
		t.Fatalf("store tenant A as the cluster owner: %v", err)
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	ingester.afterClusterScopeLock = func(string) {
		once.Do(func() {
			close(locked)
			<-release
		})
	}

	hook := testWebhook("race-group", cluster, testAlert("race-1", alertStatusFiring, "warning", time.Date(2026, 9, 15, 15, 0, 0, 0, time.UTC)))
	type ingestOutcome struct {
		result IngestResult
		err    error
	}
	ingested := make(chan ingestOutcome, 1)
	go func() {
		result, err := ingester.IngestAlertmanager(ctx, hook)
		ingested <- ingestOutcome{result, err}
	}()
	select {
	case <-locked:
	case <-time.After(30 * time.Second):
		t.Fatal("ingestion never locked the cluster scope")
	}

	type reconcileOutcome struct {
		moved int
		err   error
	}
	reconciled := make(chan reconcileOutcome, 1)
	go func() {
		moved, err := reconciler.ReconcileClusterScope(ctx, cluster)
		reconciled <- reconcileOutcome{moved, err}
	}()
	// Give the reconciliation time to finish if nothing holds it back.
	var early *reconcileOutcome
	select {
	case outcome := <-reconciled:
		early = &outcome
	case <-time.After(time.Second):
	}
	close(release)

	ingest := <-ingested
	if ingest.err != nil || ingest.result.Outcome != IngestCreated {
		t.Fatalf("ingest = %+v, %v; want created", ingest.result, ingest.err)
	}
	reconcile := reconcileOutcome{}
	if early != nil {
		reconcile = *early
	} else {
		reconcile = <-reconciled
	}
	if reconcile.err != nil {
		t.Fatalf("reconcile: %v", reconcile.err)
	}
	id := ingest.result.IncidentID
	if inc := mustGet(t, reconciler, unrestricted, id).Incident; inc.Scope != ScopeTenant || inc.TenantID.String != tenantB {
		t.Fatalf("incident after the race = %+v (reconcile finished before ingest: %v), want tenant B", inc, early != nil)
	}
	if _, err := ingester.Get(ctx, Access{TenantID: tenantA}, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("former owner get after the race = %v, want ErrNotFound", err)
	}
	requireArmedKafkaPublication(t, db, id, tenantB)

	mustIngest(t, ingester, testWebhook("race-group", cluster, testAlert("race-2", alertStatusFiring, "warning", time.Date(2026, 9, 15, 15, 5, 0, 0, time.UTC))), IngestUpdated)
	if inc := mustGet(t, reconciler, unrestricted, id).Incident; inc.TenantID.String != tenantB {
		t.Fatalf("notification on a replica with a stale owner moved the incident to %+v", inc)
	}

	// An owner change whose incident move never ran, as when the process stops
	// between the two transactions, is finished by the next notification for
	// the cluster, including incidents of other groups.
	if err := lookoutdb.New(db).StoreVerifiedClusterScope(ctx, lookoutdb.StoreVerifiedClusterScopeParams{
		ClusterID:       cluster,
		Scope:           ScopeTenant,
		TenantID:        nullString(tenantA),
		SourceUpdatedAt: sql.NullTime{Time: time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	other := mustIngest(t, reconciler, testWebhook("race-other-group", cluster, testAlert("race-3", alertStatusFiring, "warning", time.Date(2026, 9, 15, 15, 10, 0, 0, time.UTC))), IngestCreated)
	for _, incidentID := range []string{id, other.IncidentID} {
		if inc := mustGet(t, reconciler, unrestricted, incidentID).Incident; inc.TenantID.String != tenantA {
			t.Fatalf("incident %s after the interrupted owner change = %+v, want tenant A", incidentID, inc)
		}
	}
}

func requireArmedKafkaPublication(t *testing.T, db *sql.DB, incidentID, tenantID string) {
	t.Helper()
	var (
		raw       []byte
		rowTenant string
		attempts  int
		delivered sql.NullTime
		failed    sql.NullTime
	)
	if err := db.QueryRow(`SELECT payload, tenant_id::text, attempts, delivered_at, failed_at FROM lookout.notification_outbox WHERE incident_id = $1 AND channel = 'kafka'`, incidentID).
		Scan(&raw, &rowTenant, &attempts, &delivered, &failed); err != nil {
		t.Fatalf("kafka publication for %s: %v", incidentID, err)
	}
	var message KafkaIncidentMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	if rowTenant != tenantID || message.TenantID != tenantID || message.IncidentID != incidentID || attempts != 0 || delivered.Valid || failed.Valid {
		t.Fatalf("kafka publication tenant=%s message=%+v attempts=%d delivered=%v failed=%v, want armed for %s", rowTenant, message, attempts, delivered, failed, tenantID)
	}
}

// runIncidentRescope opens an incident while Quartermaster cannot answer, then
// proves the open incident follows the stored verified owner only: to tenant A
// on the next notification that verifies it, not away from A on a later
// outage or a changed Quartermaster answer alone, to tenant B through
// reconciliation after A's publication was delivered, not back to A on an
// older answer, and to platform once the cluster loses its owner.
func runIncidentRescope(t *testing.T, database *sql.DB) {
	svc, db, realtime := newRealService(t, database)
	owners := &switchableOwners{}
	svc.Owners = owners
	ctx := context.Background()
	t0 := time.Date(2026, 9, 15, 14, 0, 0, 0, time.UTC)
	hook := testWebhook("rescope-group", "owned-cluster", testAlert("r1", alertStatusFiring, "warning", t0))
	tenantAAccess := Access{TenantID: tenantA}
	tenantBAccess := Access{TenantID: tenantB}

	owners.fail()
	opened := mustIngest(t, svc, hook, IngestCreated)
	id := opened.IncidentID
	if inc := mustGet(t, svc, unrestricted, id).Incident; inc.Scope != ScopePlatform || inc.TenantID.Valid {
		t.Fatalf("incident opened without a verified owner = %+v, want platform scope", inc)
	}
	assertStrings(t, "operator notifications for the unverified incident", outboxChannels(t, db, id), []string{ChannelDiscord, ChannelSlack})
	if _, err := svc.Get(ctx, tenantAAccess, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant get before verification = %v, want ErrNotFound", err)
	}

	owners.set(Scope{Kind: ScopeTenant, TenantID: tenantA})
	if res := mustIngest(t, svc, hook, IngestUnchanged); res.IncidentID != id {
		t.Fatalf("verified notification touched %s, want %s", res.IncidentID, id)
	}
	if inc := mustGet(t, svc, tenantAAccess, id).Incident; inc.Scope != ScopeTenant || inc.TenantID.String != tenantA || inc.Status != StatusFiring {
		t.Fatalf("incident after verified tenant owner = %+v", inc)
	}
	assertStrings(t, "outbox after rescope to tenant A", outboxChannels(t, db, id), []string{ChannelDiscord, ChannelKafka, ChannelSlack})
	requireArmedKafkaPublication(t, db, id, tenantA)

	lookups := owners.lookups()
	owners.fail()
	mustIngest(t, svc, hook, IngestUnchanged)
	if inc := mustGet(t, svc, tenantAAccess, id).Incident; inc.Scope != ScopeTenant || inc.TenantID.String != tenantA {
		t.Fatalf("outage moved the incident: %+v", inc)
	}
	if got := owners.lookups(); got != lookups {
		t.Fatalf("Quartermaster lookups = %d, want %d: a verified stored scope needs none", got, lookups)
	}

	if _, err := db.Exec(`UPDATE lookout.notification_outbox SET delivered_at = NOW() WHERE incident_id = $1 AND channel = 'kafka'`, id); err != nil {
		t.Fatal(err)
	}
	owners.set(Scope{Kind: ScopeTenant, TenantID: tenantB})
	mustIngest(t, svc, hook, IngestUnchanged)
	if inc := mustGet(t, svc, tenantAAccess, id).Incident; inc.TenantID.String != tenantA {
		t.Fatalf("notification without reconciliation moved the incident: %+v", inc)
	}
	if moved, err := svc.ReconcileClusterScope(ctx, "owned-cluster"); err != nil || moved != 1 {
		t.Fatalf("reconcile to tenant B = %d, %v; want 1 moved", moved, err)
	}
	if _, err := svc.Get(ctx, tenantAAccess, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("previous tenant get after ownership moved = %v, want ErrNotFound", err)
	}
	if inc := mustGet(t, svc, tenantBAccess, id).Incident; inc.TenantID.String != tenantB {
		t.Fatalf("incident after move to tenant B = %+v", inc)
	}
	assertStrings(t, "outbox after rescope to tenant B", outboxChannels(t, db, id), []string{ChannelDiscord, ChannelKafka, ChannelSlack})
	requireArmedKafkaPublication(t, db, id, tenantB)

	owners.setAnswer(ClusterOwner{
		Scope:     Scope{Kind: ScopeTenant, TenantID: tenantA},
		UpdatedAt: time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC),
	})
	if moved, err := svc.ReconcileClusterScope(ctx, "owned-cluster"); err != nil || moved != 0 {
		t.Fatalf("reconcile with an older answer = %d, %v; want nothing moved", moved, err)
	}
	if inc := mustGet(t, svc, tenantBAccess, id).Incident; inc.TenantID.String != tenantB {
		t.Fatalf("older answer moved the incident: %+v", inc)
	}

	owners.set(Scope{Kind: ScopePlatform})
	if moved, err := svc.ReconcileClusterScope(ctx, "owned-cluster"); err != nil || moved != 1 {
		t.Fatalf("reconcile to platform = %d, %v; want 1 moved", moved, err)
	}
	if inc := mustGet(t, svc, unrestricted, id).Incident; inc.Scope != ScopePlatform || inc.TenantID.Valid {
		t.Fatalf("incident after ownership removal = %+v, want platform scope", inc)
	}
	if _, err := svc.Get(ctx, tenantBAccess, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("former owner get after ownership removal = %v, want ErrNotFound", err)
	}
	assertStrings(t, "outbox after ownership removal cancels the pending publication", outboxChannels(t, db, id), []string{ChannelDiscord, ChannelSlack})

	mustIngest(t, svc, testWebhook("rescope-group", "owned-cluster", testAlert("r1", alertStatusResolved, "warning", t0)), IngestResolved)
	assertStrings(t, "operator notifications after auto-resolve", outboxChannels(t, db, id), []string{ChannelDiscord, ChannelDiscord, ChannelSlack, ChannelSlack})

	// Tenants hear only while they can see the incident; operators hear every
	// change once, including while it is platform scope.
	want := []realtimeRecipient{
		{audience: operatorAudience, owner: "", change: changeOpened},
		{audience: tenantA, owner: tenantA, change: changeScopeChanged},
		{audience: operatorAudience, owner: tenantA, change: changeScopeChanged},
		{audience: tenantA, owner: tenantA, change: changeScopeChanged},
		{audience: tenantB, owner: tenantB, change: changeScopeChanged},
		{audience: operatorAudience, owner: tenantB, change: changeScopeChanged},
		{audience: tenantB, owner: tenantB, change: changeScopeChanged},
		{audience: operatorAudience, owner: "", change: changeScopeChanged},
		{audience: operatorAudience, owner: "", change: EventResolved},
	}
	requireAudiences(t, realtime)
	if got := realtime.recipients(); !reflect.DeepEqual(got, want) {
		t.Fatalf("realtime recipients = %+v, want %+v", got, want)
	}

	rows, err := db.Query(`SELECT body::text FROM lookout.incident_events WHERE incident_id = $1 AND kind = 'scope_changed' ORDER BY created_at, id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var toScopes []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, tenantA) || strings.Contains(raw, tenantB) {
			t.Fatalf("scope_changed timeline body %s names a tenant", raw)
		}
		var body map[string]string
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatal(err)
		}
		toScopes = append(toScopes, body["to_scope"])
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	assertStrings(t, "scope_changed timeline", toScopes, []string{ScopeTenant, ScopeTenant, ScopePlatform})
}
