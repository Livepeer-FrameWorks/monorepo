package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/networkobserver"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/metadata"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type networkReaderStub struct {
	Request       *pb.NetworkObservationsRequest
	History       obs.History
	Err           error
	Authorization string
}

func (r *networkReaderStub) GetNetworkObservations(ctx context.Context, req *pb.NetworkObservationsRequest) (*pb.NetworkObservationsResponse, error) {
	r.Request = req
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		r.Authorization = strings.Join(md.Get("authorization"), ",")
	}
	if r.Err != nil {
		return nil, r.Err
	}
	raw, _ := json.Marshal(r.History)
	return &pb.NetworkObservationsResponse{Json: raw}, nil
}
func networkFixture(t *testing.T) (*NetworkHandlers, *networkReaderStub, *gin.Engine) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ingest-secret" {
			t.Error("inventory did not use private credential")
		}
		_ = json.NewEncoder(w).Encode(networkobserver.Inventory{Provider: "livepeer", Network: "42161", Environment: "production", Resources: []obs.Resource{}, Endpoints: []obs.Endpoint{{ID: "node", IP: "192.0.2.1", Checks: []obs.Check{{ObserverID: "private-gateway"}}, AssignedObservers: []string{"private-prober"}}}})
	}))
	t.Cleanup(server.Close)
	client, err := networkobserver.New(server.URL, "ingest-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	reader := &networkReaderStub{History: obs.History{Cohorts: []obs.Cohort{}, Summaries: []obs.Cohort{}, Observations: []obs.Metric{{ObserverID: "private-gateway", Kind: "segment"}}, Details: []json.RawMessage{json.RawMessage(`{"session_id":"customer-session"}`)}}}
	h := &NetworkHandlers{Observer: client, Analytics: reader, TenantIDs: []string{"2f64c7d0-8c66-4b3b-88c4-421f8a3027f2"}, Token: "internal-token", Network: "42161", Environment: "production", JWTSecret: []byte("test-key")}
	router := gin.New()
	router.GET("/network", h.Public)
	router.GET("/diagnostics", middleware.RequireJWTAuth(h.JWTSecret), h.Diagnostics)
	return h, reader, router
}
func TestPublicNetworkUsesServerScopeAndRemovesPrivateFields(t *testing.T) {
	_, reader, router := networkFixture(t)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), "GET", "/network?tenant_id=attacker&details=true", nil))
	if response.Code != 200 {
		t.Fatalf("status %d: %s", response.Code, response.Body)
	}
	if reader.Request.IncludeDetails || len(reader.Request.TenantIds) != 1 || reader.Request.TenantIds[0] != "2f64c7d0-8c66-4b3b-88c4-421f8a3027f2" {
		t.Fatal("public caller altered scope or requested private details")
	}
	for _, secret := range []string{"customer-session", "private-gateway", "private-prober", "ingest-secret", "internal-token"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("public response leaked %s", secret)
		}
	}
	if reader.Authorization != "Bearer internal-token" {
		t.Fatal("analytics did not use internal service auth")
	}
}
func TestNetworkHistoryOutageRetainsInventory(t *testing.T) {
	_, reader, router := networkFixture(t)
	reader.Err = errors.New("internal database error")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), "GET", "/network", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"history_status":"unavailable"`) || !strings.Contains(response.Body.String(), "192.0.2.1") {
		t.Fatalf("inventory lost during analytics outage: %s", response.Body)
	}
	if strings.Contains(response.Body.String(), "internal database error") {
		t.Fatal("public response leaked internal error")
	}
}

func TestPublicNetworkPublishesObserverLocationsWithoutPrivateIdentity(t *testing.T) {
	h, _, router := networkFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"provider":"livepeer","network":"42161","environment":"production","resources":[],"endpoints":[],"observers":[{"id":"private-gateway","kind":"gateway","region":"eu","location":{"known":false}},{"id":"private-prober","kind":"prober","region":"eu","location":{"known":true,"latitude":52,"longitude":5,"source":"mmdb"}}]}`))
	}))
	t.Cleanup(server.Close)
	client, err := networkobserver.New(server.URL, "ingest-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	h.Observer = client
	var first string
	for range 2 {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), "GET", "/network", nil))
		var body struct {
			Inventory struct {
				Observers []obs.Observer `json:"observers"`
			} `json:"inventory"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Inventory.Observers) != 2 {
			t.Fatalf("observer locations missing: %d %s", response.Code, response.Body)
		}
		gateway, prober := body.Inventory.Observers[0], body.Inventory.Observers[1]
		if gateway.Kind != "gateway" || gateway.Location.Known || prober.Kind != "prober" || !prober.Location.Valid() || prober.Region != "eu" {
			t.Fatalf("observer role/geography changed: %+v", body.Inventory.Observers)
		}
		if strings.Contains(response.Body.String(), "private-gateway") || strings.Contains(response.Body.String(), "private-prober") || prober.ID == "" || prober.ID == gateway.ID {
			t.Fatal("observer map identities are private, missing or ambiguous")
		}
		if first != "" && first != prober.ID {
			t.Fatal("public observer identity changed between refreshes")
		}
		first = prober.ID
	}
}
func TestNetworkDiagnosticsRequiresPlatformOperator(t *testing.T) {
	h, reader, router := networkFixture(t)
	for _, operator := range []bool{false, true} {
		roles := []string{}
		if operator {
			roles = []string{"platform_operator"}
		}
		token, err := auth.GenerateSessionJWT("user", "2f64c7d0-8c66-4b3b-88c4-421f8a3027f2", "user@example.com", "admin", roles, time.Now(), h.JWTSecret)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(context.Background(), "GET", "/diagnostics", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if operator {
			if response.Code != 200 || !reader.Request.IncludeDetails || !strings.Contains(response.Body.String(), "customer-session") || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("operator diagnostics failed: %d %s", response.Code, response.Body)
			}
		} else if response.Code != 403 {
			t.Fatalf("tenant administrator accessed operator diagnostics: %d", response.Code)
		}
	}
}

// Probers reach the observer only through Bridge: the public route maps to the
// observer's path, the observer authenticates the prober's own secret, and the
// source address is Bridge's resolved client address, not one the prober sent.
func TestProberRelayForwardsToObserverOverTheMesh(t *testing.T) {
	type seen struct{ method, path, query, auth, forwarded, body string }
	var calls []seen
	observer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("X-Forwarded-For"), string(raw)})
		if r.Header.Get("Authorization") != "Bearer prober-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"assignments":[]}`))
	}))
	t.Cleanup(observer.Close)
	client, err := networkobserver.New(observer.URL, "bridge-inventory-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &NetworkHandlers{Observer: client, ClientIP: func(*http.Request) string { return "203.0.113.9" }}
	router := gin.New()
	router.GET("/network-observer/v1/work", h.ProberRelay("/v1/work"))
	router.POST("/network-observer/v1/observations", h.ProberRelay("/v1/observations"))
	serve := func(method, target, auth, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		req.Header.Set("X-Forwarded-For", "10.0.0.1")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}

	work := serve(http.MethodGet, "/network-observer/v1/work?observer_id=p1&region=eu", "Bearer prober-secret", "")
	if work.Code != 200 || work.Body.String() != `{"assignments":[]}` || work.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("work relay = %d %q", work.Code, work.Body)
	}
	ingest := serve(http.MethodPost, "/network-observer/v1/observations", "Bearer prober-secret", `{"id":"b1"}`)
	if ingest.Code != 200 {
		t.Fatalf("observation relay = %d %q", ingest.Code, ingest.Body)
	}
	want := []seen{
		{http.MethodGet, "/v1/work", "observer_id=p1&region=eu", "Bearer prober-secret", "203.0.113.9", ""},
		{http.MethodPost, "/v1/observations", "", "Bearer prober-secret", "203.0.113.9", `{"id":"b1"}`},
	}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("observer received %+v, want %+v", calls, want)
	}

	if rejected := serve(http.MethodGet, "/network-observer/v1/work", "Bearer wrong", ""); rejected.Code != http.StatusUnauthorized {
		t.Fatalf("wrong prober secret = %d, want the observer's 401", rejected.Code)
	}
	before := len(calls)
	if anonymous := serve(http.MethodGet, "/network-observer/v1/work", "", ""); anonymous.Code != http.StatusUnauthorized || len(calls) != before {
		t.Fatalf("request without a bearer = %d after %d observer calls, want 401 without relaying", anonymous.Code, len(calls)-before)
	}
	oversized := serve(http.MethodPost, "/network-observer/v1/observations", "Bearer prober-secret", strings.Repeat("x", networkobserver.MaxBodyBytes+1))
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized batch = %d, want 413", oversized.Code)
	}

	unconfigured := gin.New()
	unconfigured.GET("/network-observer/v1/work", (&NetworkHandlers{}).ProberRelay("/v1/work"))
	response := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/network-observer/v1/work", nil)
	req.Header.Set("Authorization", "Bearer prober-secret")
	unconfigured.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("relay without an observer = %d, want 503", response.Code)
	}
}
