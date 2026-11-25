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
	"io"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"
)

type API struct {
	Store             *store.Store
	Engine            Engine
	Secret, ClusterID string
	MaxObservers      int
	TrustedProxies    []netip.Prefix
	Definitions       map[string]obs.Observer
	Readiness         *Readiness
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", a.metrics)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /ready", a.ready)
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
	mux.HandleFunc("POST /v1/work", protected(a.work))
	mux.HandleFunc("POST /v1/observations", protected(a.ingest))
	mux.HandleFunc("POST /v1/observation-batches", protected(a.ingestBulk))
	mux.HandleFunc("GET /v1/inventory", protected(a.inventory))
	mux.HandleFunc("GET /v1/candidates", protected(a.candidates))
	return mux
}

// ready answers 200 only while the database answers, the contract catalog
// refreshes and the gateway consumer group is consuming; the body names every
// failing condition.
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	var report ReadinessReport
	if a.Readiness != nil {
		report = a.Readiness.Check(time.Now().UTC())
	} else {
		report = ReadinessReport{Ready: true, Reasons: []string{}}
	}
	if err := a.Store.DB.PingContext(r.Context()); err != nil {
		report.Ready = false
		report.Reasons = append([]string{"database: unavailable"}, report.Reasons...)
	}
	status := http.StatusOK
	if !report.Ready {
		status = http.StatusServiceUnavailable
	}
	respond(w, status, report)
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}
func (a *API) work(w http.ResponseWriter, r *http.Request) {
	request := obs.WorkRequest{ObserverID: r.URL.Query().Get("observer_id"), Region: r.URL.Query().Get("region")}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&request); err != nil {
			http.Error(w, "invalid work request", 400)
			return
		}
	}
	if request.ObserverID == "" || len(request.ObserverID) > 128 || len(request.Region) > 128 || strings.ContainsAny(request.ObserverID, "\r\n") || strings.HasPrefix(request.ObserverID, "gateway:") {
		http.Error(w, "invalid observer", 400)
		return
	}
	now := time.Now().UTC()
	ip := a.proberIP(r)
	location := obs.Location{Source: "unknown"}
	if a.Engine.Locate != nil {
		location = a.Engine.Locate(ip)
	}
	if configured, ok := a.Definitions[request.ObserverID]; ok {
		if configured.Region != "" {
			request.Region = configured.Region
		}
		if configured.Location.Valid() {
			location = configured.Location
			location.Source = "configured"
		}
	}
	work, err := a.Store.Poll(r.Context(), request, ip, location, a.MaxObservers, now)
	if err != nil {
		http.Error(w, "work unavailable", http.StatusServiceUnavailable)
		return
	}
	for i := range work.Assignments {
		work.Assignments[i].Signature = assignmentSignature(a.Secret, work.Assignments[i])
	}
	work.ServerTime = time.Now().UTC()
	respond(w, 200, work)
}

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
	if b.ObserverID == "" || len(b.ObserverID) > 128 || strings.HasPrefix(b.ObserverID, "gateway:") {
		http.Error(w, "invalid observer", http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256(raw)
	err = a.Store.Receive(r.Context(), b.ID, hex.EncodeToString(sum[:]), b.ObserverID, "prober", raw)
	if errors.Is(err, store.ErrConflict) {
		respond(w, http.StatusConflict, map[string]string{"code": "EVENT_ID_CONFLICT"})
		return
	}
	if errors.Is(err, store.ErrInboxFull) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "observation capacity reached", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		http.Error(w, "receipt unavailable; retry with the same ID", http.StatusServiceUnavailable)
		return
	}
	respond(w, 200, map[string]any{"accepted": true, "event_id": b.ID, "stage": "received"})
}

func (a *API) inventory(w http.ResponseWriter, r *http.Request) {
	s, err := a.Store.Snapshot(r.Context())
	if err != nil {
		http.Error(w, "inventory unavailable", http.StatusServiceUnavailable)
		return
	}
	now := time.Now().UTC()
	endpoints := []obs.Endpoint{}
	resources := []obs.Resource{}
	observers := []obs.Observer{}
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
	for _, observer := range s.Observers {
		observer.CollectionStatus = "reporting"
		switch {
		case observer.LastSeen.IsZero() || now.Sub(observer.LastSeen) > 2*time.Minute:
			observer.CollectionStatus = "offline"
		case observer.Kind == "prober" && observer.Delivery == nil:
			observer.CollectionStatus = "unverified"
		case observer.Kind == "prober" && !observer.Delivery.Healthy(now):
			observer.CollectionStatus = "delayed"
		case observer.Kind == "prober" && observer.Delivery.Assigned == 0:
			observer.CollectionStatus = "idle"
		}
		observers = append(observers, observer)
	}
	sort.Slice(observers, func(i, j int) bool { return observers[i].ID < observers[j].ID })
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].ID < endpoints[j].ID })
	respond(w, 200, map[string]any{"generated_at": now, "provider": "livepeer", "network": a.Engine.Network, "environment": a.Engine.Environment, "resources": resources, "endpoints": endpoints, "observers": observers})
}
func (a *API) candidates(w http.ResponseWriter, r *http.Request) {
	s, err := a.Store.Snapshot(r.Context())
	if err != nil {
		http.Error(w, "candidates unavailable", http.StatusServiceUnavailable)
		return
	}
	now := time.Now().UTC()
	feed := obs.CandidateFeed{Version: 1, GeneratedAt: now, ExpiresAt: now.Add(5 * time.Minute), Provider: "livepeer", Network: a.Engine.Network, Environment: a.Engine.Environment, Candidates: []obs.Endpoint{}}
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
