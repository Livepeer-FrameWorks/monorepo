package coordinator

import (
	"fmt"
	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/google/uuid"
	"net"
	"net/url"
	"sort"
	"time"
)

type Engine struct {
	Network, Environment string
	Locate               func(string) obs.Location
}

func (e Engine) Catalog(s *store.State, resources []obs.Resource, now time.Time) {
	active := map[string]bool{}
	for _, r := range resources {
		active[r.ID] = r.Active
	}
	for key, r := range s.Resources {
		if r.CatalogSource == "advertised" {
			r.Active = active[r.ID]
		} else {
			r.Active = false
		}
		s.Resources[key] = r
	}
	for _, r := range resources {
		r.CatalogSource = "contract"
		s.Resources[r.Key()] = r
		if u, err := url.Parse(r.URI); err == nil && net.ParseIP(u.Hostname()) != nil {
			e.endpoint(s, r, u.Hostname(), now)
		}
	}

	for id, p := range s.Endpoints {
		if r, ok := s.Resources[p.Resource.Key()]; ok {
			p.Resource = r
		}
		s.Endpoints[id] = p
	}
}
func (e Engine) endpoint(s *store.State, r obs.Resource, ip string, now time.Time) {
	p, err := obs.NewEndpoint(r, ip, now)
	if err != nil {
		return
	}
	if old, ok := s.Endpoints[p.ID]; ok {
		if now.After(old.DNSSeen) {
			old.DNSSeen = now
		}
		old.Resource = r
		old.Referenced = true
		s.Endpoints[p.ID] = old
		return
	}
	if e.Locate != nil {
		p.Location = e.Locate(ip)
	}
	s.Endpoints[p.ID] = p
}
func (e Engine) Apply(s *store.State, b obs.Batch, now time.Time) error {
	if _, err := uuid.Parse(b.ID); err != nil {
		return fmt.Errorf("invalid batch ID")
	}
	if _, ok := s.Observers[b.ObserverID]; !ok {
		return fmt.Errorf("unregistered observer")
	}
	if len(b.DNS)+len(b.Results) > 1000 {
		return fmt.Errorf("batch too large")
	}
	if b.Report != nil && (b.Report.ObservedAt.IsZero() || b.Report.ObservedAt.After(now.Add(time.Minute)) || now.Sub(b.Report.ObservedAt) > 30*24*time.Hour || (b.Report.Kind != "heartbeat" && b.Report.Kind != "dns_cycle")) {
		return fmt.Errorf("invalid observer report")
	}
	// Validate the entire batch before mutating any state.
	for _, d := range b.DNS {
		r, ok := s.Resources[d.Resource.Key()]
		if !ok || r.URI != d.Resource.URI {
			return fmt.Errorf("unknown catalog resource")
		}
		if d.ObservedAt.IsZero() || d.ObservedAt.After(now.Add(time.Minute)) || now.Sub(d.ObservedAt) > 30*24*time.Hour {
			return fmt.Errorf("invalid DNS observation time")
		}
		if len(d.IPs) > 256 {
			return fmt.Errorf("too many DNS answers")
		}
	}
	for _, v := range b.Results {
		if _, err := uuid.Parse(v.ID); err != nil {
			return fmt.Errorf("invalid result ID")
		}
		a, ok := s.Assignments[v.AssignmentID]
		if !ok || a.ObserverID != b.ObserverID || a.Endpoint.ID != v.EndpointID || v.Check.ObserverID != b.ObserverID {
			return fmt.Errorf("unknown assignment")
		}
		if v.Check.ObservedAt.Before(a.DueAt.Add(-time.Second)) || v.Check.ObservedAt.After(a.Deadline.Add(time.Minute)) {
			return fmt.Errorf("invalid check time")
		}
		if v.Check.Success && (!v.Check.Qualifying || v.Check.ActualIP != a.Endpoint.IP) {
			return fmt.Errorf("unattributed success")
		}
		if len(v.Hardware) > 65536 || len(v.Capabilities) > 256 {
			return fmt.Errorf("result too large")
		}
	}
	for _, d := range b.DNS {
		if d.ErrorKind == "" {
			r := s.Resources[d.Resource.Key()]
			for _, ip := range obs.NormalizeIPs(d.IPs) {
				e.endpoint(s, r, ip, d.ObservedAt)
			}
		}
	}
	for _, v := range b.Results {
		p := s.Endpoints[v.EndpointID]
		// Older, durably retried observations remain historical without overwriting live state.
		{
			if v.Check.ObservedAt.After(p.LastCheck) {
				p.LastCheck = v.Check.ObservedAt
			}
			found := false
			for i, c := range p.Checks {
				if c.ObserverID == b.ObserverID {
					if !v.Check.ObservedAt.Before(c.ObservedAt) {
						p.Checks[i] = v.Check
					}
					found = true
					break
				}
			}
			if !found {
				p.Checks = append(p.Checks, v.Check)
			}
			if v.Check.Success && !v.Check.ObservedAt.Before(p.LastSuccess) {
				p.LastSuccess = v.Check.ObservedAt
				if !v.Check.ObservedAt.Before(p.LastCheck) {
					p.FailureStreak = 0
				}
				p.Price = v.Quote
				p.Capabilities = v.Capabilities
				p.Hardware = v.Hardware
			}
			s.Endpoints[p.ID] = p
		}
		if v.Check.Success {
			for _, uri := range v.AdvertisedURIs {
				if len(uri) > 2048 {
					continue
				}
				r := p.Resource
				r.URI = uri
				r.CatalogSource = "advertised"
				if _, err := obs.NewEndpoint(r, "", now); err != nil {
					continue
				}
				s.Resources[r.Key()] = r
			}
		}
		a := s.Assignments[v.AssignmentID]
		a.Completed = true
		s.Assignments[a.ID] = a
	}
	return nil
}
func (e Engine) Schedule(s *store.State, now time.Time) {
	observers := []obs.Observer{}
	healthy := map[string]bool{}
	for _, o := range s.Observers {
		if o.Kind != "gateway" {
			observers = append(observers, o)
		}
		healthy[o.ID] = now.Sub(o.LastSeen) <= 2*time.Minute && !o.LastSeen.IsZero()
	}
	pending := map[string][]obs.Assignment{}
	for _, a := range s.Assignments {
		if (a.Settled && now.Sub(a.Deadline) > 5*time.Minute) || now.Sub(a.Deadline) > time.Hour {
			delete(s.Assignments, a.ID)
			continue
		}
		if !a.Settled {
			pending[a.Endpoint.ID] = append(pending[a.Endpoint.ID], a)
		}
	}
	for id, p := range s.Endpoints {
		r := s.Resources[p.Resource.Key()]
		p.Resource = r
		p.Referenced = r.Active && r.URI == p.Resource.URI && now.Sub(p.DNSSeen) < 24*time.Hour
		if u, err := url.Parse(r.URI); err == nil && net.ParseIP(u.Hostname()) != nil && r.Active && u.Hostname() == p.IP {
			p.Referenced = true
		}
		if p.Retirable(now) {
			delete(s.Endpoints, id)
			continue
		}
		assignments := pending[id]
		assigned := append([]string(nil), p.AssignedObservers...)
		settled := len(assignments) > 0
		for _, a := range assignments {
			if !a.Completed && now.Before(a.Deadline) {
				settled = false
			}
		}
		p.Status = obs.Availability(now, time.Duration(p.CadenceSeconds)*time.Second, assigned, p.Checks, healthy)
		if settled {
			cycleChecks := []obs.Check{}
			cycleObservers := []string{}
			for _, a := range assignments {
				cycleObservers = append(cycleObservers, a.ObserverID)
				if !a.Completed {
					continue
				}
				for _, check := range p.Checks {
					if check.ObserverID == a.ObserverID && !check.ObservedAt.Before(a.DueAt) && !check.ObservedAt.After(a.Deadline.Add(time.Minute)) {
						cycleChecks = append(cycleChecks, check)
					}
				}
			}
			cycleStatus := obs.Availability(now, time.Duration(p.CadenceSeconds)*time.Second, cycleObservers, cycleChecks, healthy)
			switch cycleStatus {
			case "unavailable":
				p.FailureStreak++
			case "up":
				p.FailureStreak = 0
			}
			p.CadenceSeconds = int64(obs.Backoff(p.FailureStreak) / time.Second)
			p.NextCheck = now.Add(obs.Backoff(p.FailureStreak))
			for _, a := range assignments {
				a.Settled = true
				s.Assignments[a.ID] = a
			}
		}
		if len(assignments) == 0 || settled {
			if !now.Before(p.NextCheck) {
				selected := obs.Nearest(p.Location, observers, now, 2*time.Minute, 2)
				p.AssignedObservers = nil
				for _, o := range selected {
					outstanding := 0
					for _, a := range s.Assignments {
						if a.ObserverID == o.ID && !a.Settled && !a.Completed {
							outstanding++
						}
					}
					if outstanding >= 16 {
						continue
					}
					p.AssignedObservers = append(p.AssignedObservers, o.ID)
					a := obs.Assignment{ID: uuid.NewString(), Endpoint: p, ObserverID: o.ID, DueAt: now, Deadline: now.Add(45 * time.Second)}
					s.Assignments[a.ID] = a
				}
			}
		}
		s.Endpoints[id] = p
	}
	retained := map[string]bool{}
	for _, p := range s.Endpoints {
		retained[p.Resource.Key()] = true
	}
	for key, r := range s.Resources {
		if !r.Active && !retained[key] && now.Sub(r.CatalogSeen) > 30*24*time.Hour {
			delete(s.Resources, key)
		}
	}
}
func Work(s *store.State, observerID string, now time.Time) obs.Work {
	w := obs.Work{Catalog: []obs.Resource{}, Assignments: []obs.Assignment{}, DNSIntervalSeconds: 300}
	for _, r := range s.Resources {
		if r.Active {
			w.Catalog = append(w.Catalog, r)
		}
	}
	for _, a := range s.Assignments {
		if a.ObserverID == observerID && !a.Completed && now.Before(a.Deadline) {
			w.Assignments = append(w.Assignments, a)
		}
	}
	sort.Slice(w.Catalog, func(i, j int) bool { return w.Catalog[i].Key() < w.Catalog[j].Key() })
	sort.Slice(w.Assignments, func(i, j int) bool { return w.Assignments[i].ID < w.Assignments[j].ID })
	return w
}
