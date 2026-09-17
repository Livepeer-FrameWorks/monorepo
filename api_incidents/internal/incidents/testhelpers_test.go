package incidents

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"
)

var errQuartermasterUnavailable = errors.New("quartermaster unavailable")

// fakeOwners answers every listed cluster with its owner and any other cluster
// as missing from Quartermaster.
type fakeOwners map[string]Scope

func (f fakeOwners) LookupOwner(_ context.Context, clusterID string) (ClusterOwner, error) {
	if scope, ok := f[clusterID]; ok {
		return ClusterOwner{Scope: scope, UpdatedAt: time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC)}, nil
	}
	return ClusterOwner{Scope: Scope{Kind: ScopePlatform}}, nil
}

// switchableOwners returns one owner for every cluster; tests change it between
// calls to model a Quartermaster outage and later ownership answers. Each set
// advances the cluster's updated_at unless the test pins it.
type switchableOwners struct {
	mu        sync.Mutex
	owner     ClusterOwner
	err       error
	updatedAt time.Time
	calls     int
}

func (s *switchableOwners) set(scope Scope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updatedAt.IsZero() {
		s.updatedAt = time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC)
	}
	s.updatedAt = s.updatedAt.Add(time.Minute)
	s.owner, s.err = ClusterOwner{Scope: scope, UpdatedAt: s.updatedAt}, nil
}

func (s *switchableOwners) setAnswer(owner ClusterOwner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owner, s.err = owner, nil
}

func (s *switchableOwners) fail() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = errQuartermasterUnavailable
}

func (s *switchableOwners) lookups() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *switchableOwners) LookupOwner(context.Context, string) (ClusterOwner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return ClusterOwner{}, s.err
	}
	return s.owner, nil
}

// allChannelsRouter mirrors the production routing with every channel configured.
type allChannelsRouter struct{}

func (allChannelsRouter) ChannelsFor(severity string) []string {
	if severity == "critical" {
		return []string{ChannelEmail, ChannelSlack, ChannelDiscord}
	}
	return []string{ChannelSlack, ChannelDiscord}
}

type recordingRealtime struct {
	mu     sync.Mutex
	events []*ipcpb.ServiceEvent
}

func (r *recordingRealtime) SendServiceEvent(event *ipcpb.ServiceEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *recordingRealtime) snapshot() []*ipcpb.ServiceEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*ipcpb.ServiceEvent(nil), r.events...)
}

// realtimeRecipient is one recorded send: tenant events name their tenant and
// operator events use the "operator" audience with the incident's owner.
type realtimeRecipient struct {
	audience string
	owner    string
	change   string
}

const operatorAudience = "operator"

func (r *recordingRealtime) recipients() []realtimeRecipient {
	out := []realtimeRecipient{}
	for _, event := range r.snapshot() {
		recipient := realtimeRecipient{
			audience: event.GetTenantId(),
			owner:    event.GetIncidentEvent().GetTenantId(),
			change:   event.GetIncidentEvent().GetChange(),
		}
		if event.GetEventType() == serviceevents.PlatformIncidentUpdated {
			recipient.audience = operatorAudience
		}
		out = append(out, recipient)
	}
	return out
}

// requireAudiences fails unless every tenant event names a tenant and every
// operator event is tenantless, which is what keeps platform incidents away
// from tenant subscribers.
func requireAudiences(t testing.TB, realtime *recordingRealtime) {
	t.Helper()
	for _, event := range realtime.snapshot() {
		switch event.GetEventType() {
		case realtimeEventType:
			if event.GetTenantId() == "" || event.GetIncidentEvent().GetTenantId() != event.GetTenantId() {
				t.Fatalf("tenant incident event without a matching tenant: %v", event)
			}
		case serviceevents.PlatformIncidentUpdated:
			if event.GetTenantId() != "" {
				t.Fatalf("operator incident event carries envelope tenant %q", event.GetTenantId())
			}
		default:
			t.Fatalf("unexpected realtime event type %q", event.GetEventType())
		}
	}
}

func testWebhook(groupKey, cluster string, alerts ...AlertmanagerAlert) AlertmanagerWebhook {
	return AlertmanagerWebhook{
		Version:           "4",
		GroupKey:          groupKey,
		Status:            "firing",
		Receiver:          "lookout",
		GroupLabels:       map[string]string{"alertname": "EdgeDown", "cluster": cluster, "region": "eu-west"},
		CommonAnnotations: map[string]string{"summary": "Edge node down", "description": "An edge node stopped reporting."},
		Alerts:            alerts,
	}
}

func testAlert(fingerprint, status, severity string, startsAt time.Time) AlertmanagerAlert {
	alert := AlertmanagerAlert{
		Status:       status,
		Labels:       map[string]string{"alertname": "EdgeDown", "severity": severity, "instance": fingerprint},
		Annotations:  map[string]string{"summary": "Edge node down"},
		StartsAt:     startsAt,
		GeneratorURL: "http://vmalert/" + fingerprint,
		Fingerprint:  fingerprint,
	}
	if status == alertStatusResolved {
		alert.EndsAt = startsAt.Add(10 * time.Minute)
	}
	return alert
}
