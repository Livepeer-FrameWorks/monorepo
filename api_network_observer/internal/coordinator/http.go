package coordinator

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"io"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
)

type API struct {
	Store             *store.Store
	Engine            Engine
	Secret, ClusterID string
	MaxObservers      int
	TrustedProxies    []netip.Prefix
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Store.DB.PingContext(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(200)
	})
	protected := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			x, y := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(a.Secret))
			if a.Secret == "" || subtle.ConstantTimeCompare(x[:], y[:]) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /v1/work", protected(a.work))
	mux.HandleFunc("POST /v1/observations", protected(a.ingest))
	mux.HandleFunc("GET /v1/inventory", protected(a.inventory))
	mux.HandleFunc("GET /v1/candidates", protected(a.candidates))
	return mux
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}
func (a *API) work(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("observer_id")
	region := r.URL.Query().Get("region")
	if id == "" || len(id) > 128 || len(region) > 128 || strings.ContainsAny(id, "\r\n") || strings.HasPrefix(id, "gateway:") {
		http.Error(w, "invalid observer", 400)
		return
	}
	now := time.Now().UTC()
	var work obs.Work
	err := a.Store.Update(r.Context(), func(s *store.State, _ *sql.Tx) error {
		o, exists := s.Observers[id]
		if !exists {
			probers := 0
			for _, observer := range s.Observers {
				if observer.Kind != "gateway" {
					probers++
				}
			}
			if probers >= a.MaxObservers {
				return errors.New("observer limit")
			}
			o = obs.Observer{ID: id, Region: region, Kind: "prober"}
		}
		if !o.Location.Valid() && a.Engine.Locate != nil {
			if ip := a.proberIP(r); ip != "" {
				o.Location = a.Engine.Locate(ip)
			}
		}
		o.LastSeen = now
		s.Observers[id] = o
		a.Engine.Schedule(s, now)
		work = Work(s, id, now)
		for i := range work.Assignments {
			work.Assignments[i].Signature = assignmentSignature(a.Secret, work.Assignments[i])
		}
		return nil
	})
	if err != nil {
		http.Error(w, "work unavailable", http.StatusServiceUnavailable)
		return
	}
	respond(w, 200, work)
}

var errConflict = errors.New("event ID conflict")

func (a *API) ingest(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		http.Error(w, "invalid batch", 400)
		return
	}
	var b obs.Batch
	if json.Unmarshal(raw, &b) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if _, err = uuid.Parse(b.ID); err != nil {
		http.Error(w, "invalid ID", 400)
		return
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	now := time.Now().UTC()
	err = a.Store.Update(r.Context(), func(s *store.State, tx *sql.Tx) error {
		var old string
		err = tx.QueryRowContext(r.Context(), "SELECT digest FROM network_observer.ingest_receipts WHERE tenant_id=$1 AND id=$2", a.Store.TenantID, b.ID).Scan(&old)
		if err == nil {
			if old != digest {
				return errConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err = a.resolveAssignments(s, b, now); err != nil {
			return err
		}
		if err = a.Engine.Apply(s, b, now); err != nil {
			return err
		}
		var count int
		if err = tx.QueryRowContext(r.Context(), "SELECT count(*) FROM network_observer.telemetry_outbox WHERE tenant_id=$1", a.Store.TenantID).Scan(&count); err != nil {
			return err
		}
		if count+2*len(b.Results)+1 > 100000 {
			return errors.New("outbox full")
		}
		if _, err = tx.ExecContext(r.Context(), "INSERT INTO network_observer.ingest_receipts (tenant_id,id,digest) VALUES ($1,$2,$3)", a.Store.TenantID, b.ID, digest); err != nil {
			return err
		}
		if b.Report != nil {
			v := b.Report
			event := &pb.GatewayTelemetryEvent{EventId: uuid.NewSHA1(uuid.NameSpaceOID, []byte(b.ID+":report")).String(), SchemaVersion: 3, NetworkId: a.Engine.Network, Environment: a.Engine.Environment, ProducerKind: "prober", GatewayId: b.ObserverID, GatewayRegion: s.Observers[b.ObserverID].Region, ClusterId: a.ClusterID, ClusterOwnerTenantId: a.Store.TenantID, Timestamp: timestamppb.New(v.ObservedAt), Payload: &pb.GatewayTelemetryEvent_ObserverReport{ObserverReport: &pb.OrchestratorObserverReport{ReportKind: v.Kind, Expected: v.Expected, Completed: v.Completed, Failed: v.Failed, CadenceSeconds: v.CadenceSeconds, QueueRecords: v.QueueRecords, DroppedRecords: v.DroppedRecords, Healthy: v.Healthy}}}
			raw, marshalErr := proto.Marshal(event)
			if marshalErr != nil {
				return marshalErr
			}
			if _, err = tx.ExecContext(r.Context(), "INSERT INTO network_observer.telemetry_outbox (tenant_id,id,payload) VALUES ($1,$2,$3) ON CONFLICT (tenant_id,id) DO NOTHING", a.Store.TenantID, event.EventId, raw); err != nil {
				return err
			}
		}
		for _, v := range b.Results {
			p := s.Endpoints[v.EndpointID]
			o := s.Observers[b.ObserverID]
			event := &pb.GatewayTelemetryEvent{EventId: v.ID, SchemaVersion: 3, NetworkId: a.Engine.Network, Environment: a.Engine.Environment, ProducerKind: "prober", GatewayId: b.ObserverID, GatewayRegion: o.Region, ClusterId: a.ClusterID, ClusterOwnerTenantId: a.Store.TenantID, Timestamp: timestamppb.New(v.Check.ObservedAt), Payload: &pb.GatewayTelemetryEvent_Discovery{Discovery: &pb.OrchestratorDiscoveryObserved{OrchAddr: p.Resource.ID, OrchUrl: p.Resource.URI, Vantage: &pb.OrchestratorVantageGeo{ResolvedIp: v.Check.ActualIP, Dialed: v.Check.ActualIP != ""}, TargetIp: p.IP, Reachable: v.Check.Success, DiscoveryLatencyMs: v.Check.LatencyMS, FailureKind: v.Check.FailureKind, AttemptId: v.AssignmentID, SamplingSource: "prober", ScheduledIntervalSeconds: uint32(p.CadenceSeconds), ObserverHealthy: true}}}
			if v.Check.Success {
				price := &pb.OrchestratorStateUpdate{OrchAddr: p.Resource.ID, CanonicalUrl: p.Resource.URI, AdvertisedNodeUrls: v.AdvertisedURIs, Capabilities: v.Capabilities, Hardware: v.Hardware, Source: "prober", Vantage: &pb.OrchestratorVantageGeo{ResolvedIp: v.Check.ActualIP, Dialed: true}}
				if v.Quote != nil {
					num, e1 := strconv.ParseInt(v.Quote.Numerator, 10, 64)
					den, e2 := strconv.ParseInt(v.Quote.Denominator, 10, 64)
					if e1 == nil && e2 == nil && den > 0 {
						price.PriceKnown = true
						price.PricePerUnit = num
						price.PixelsPerUnit = den
						price.PriceCurrency = v.Quote.Currency
						price.PriceUnit = v.Quote.Unit
					}
				}
				stateEvent, ok := proto.Clone(event).(*pb.GatewayTelemetryEvent)
				if !ok {
					return errors.New("unexpected telemetry clone type")
				}
				stateEvent.EventId = uuid.NewSHA1(uuid.NameSpaceOID, []byte(v.ID+":state")).String()
				stateEvent.Payload = &pb.GatewayTelemetryEvent_State{State: price}
				stateRaw, e := proto.Marshal(stateEvent)
				if e != nil {
					return e
				}
				if _, e = tx.ExecContext(r.Context(), "INSERT INTO network_observer.telemetry_outbox (tenant_id,id,payload) VALUES ($1,$2,$3) ON CONFLICT (tenant_id,id) DO NOTHING", a.Store.TenantID, stateEvent.EventId, stateRaw); e != nil {
					return e
				}
			}
			payload, marshalErr := proto.Marshal(event)
			if marshalErr != nil {
				return marshalErr
			}
			if _, err = tx.ExecContext(r.Context(), "INSERT INTO network_observer.telemetry_outbox (tenant_id,id,payload) VALUES ($1,$2,$3) ON CONFLICT (tenant_id,id) DO NOTHING", a.Store.TenantID, event.EventId, payload); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errConflict) {
		http.Error(w, "event ID conflict", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "batch rejected; retry with the same ID", http.StatusServiceUnavailable)
		return
	}
	respond(w, 200, map[string]any{"accepted": true, "event_id": b.ID})
}
func (a *API) inventory(w http.ResponseWriter, r *http.Request) {
	s, err := a.Store.Snapshot(r.Context())
	if err != nil {
		http.Error(w, "inventory unavailable", http.StatusServiceUnavailable)
		return
	}
	now := time.Now().UTC()
	a.Engine.Schedule(s, now)
	endpoints := []obs.Endpoint{}
	resources := []obs.Resource{}
	for _, p := range s.Endpoints {
		if r.URL.Query().Get("include_inactive") != "true" && p.Hidden(now) {
			continue
		}
		p.Checks = nil
		p.AssignedObservers = nil
		endpoints = append(endpoints, p)
	}
	for _, v := range s.Resources {
		resources = append(resources, v)
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].ID < endpoints[j].ID })
	respond(w, 200, map[string]any{"generated_at": now, "provider": "livepeer", "network": a.Engine.Network, "environment": a.Engine.Environment, "resources": resources, "endpoints": endpoints})
}
func (a *API) candidates(w http.ResponseWriter, r *http.Request) {
	s, err := a.Store.Snapshot(r.Context())
	if err != nil {
		http.Error(w, "candidates unavailable", http.StatusServiceUnavailable)
		return
	}
	now := time.Now().UTC()
	feed := obs.CandidateFeed{Version: 1, GeneratedAt: now, ExpiresAt: now.Add(5 * time.Minute), Network: a.Engine.Network, Environment: a.Engine.Environment, Candidates: []obs.Endpoint{}}
	for _, p := range s.Endpoints {
		if p.Resource.Active && p.Referenced && now.Sub(p.LastSuccess) < 30*time.Minute {
			p.Checks = nil
			p.AssignedObservers = nil
			p.Hardware = ""
			feed.Candidates = append(feed.Candidates, p)
		}
	}
	sort.Slice(feed.Candidates, func(i, j int) bool { return feed.Candidates[i].ID < feed.Candidates[j].ID })
	respond(w, 200, feed)
}

type Sender interface {
	SendGatewayTelemetryContext(context.Context, *pb.GatewayTelemetryEvent) error
}

func (a *API) Drain(ctx context.Context, sender Sender) error {
	// Raw transaction: an external Kafka send cannot be replayed by a SQL transaction retry.
	tx, err := a.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback() //nolint:errcheck // Retain the original SQL or external delivery error; committed transactions are already closed.
	}()
	var id string
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT id,payload FROM network_observer.telemetry_outbox WHERE tenant_id=$1 AND next_attempt_at<=now() ORDER BY next_attempt_at LIMIT 1 FOR UPDATE SKIP LOCKED", a.Store.TenantID).Scan(&id, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	if err != nil {
		return err
	}
	var event pb.GatewayTelemetryEvent
	if err = proto.Unmarshal(raw, &event); err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = sender.SendGatewayTelemetryContext(sendCtx, &event)
	cancel()
	if err != nil {
		if _, e := tx.ExecContext(ctx, "UPDATE network_observer.telemetry_outbox SET attempts=attempts+1,next_attempt_at=now()+interval '30 seconds' WHERE tenant_id=$1 AND id=$2", a.Store.TenantID, id); e != nil {
			return e
		}
		if e := tx.Commit(); e != nil {
			return e
		}
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM network_observer.telemetry_outbox WHERE tenant_id=$1 AND id=$2", a.Store.TenantID, id); err != nil {
		return err
	}
	return tx.Commit()
}
