package coordinator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"frameworks/api_network_observer/internal/store"
	"github.com/DATA-DOG/go-sqlmock"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
)

func TestCandidatesFeedProvider(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty catalog", true: "livepeer catalog"}[populated], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			const tenant = "11111111-1111-4111-8111-111111111111"
			now := time.Now().UTC()
			resource := obs.Resource{Provider: "livepeer", Network: "42161", Environment: "staging", ID: "0xabc", URI: "https://orch.example:8935", Active: true}
			mock.ExpectBegin()
			for _, table := range []string{"resources", "endpoints", "observers", "assignments"} {
				rows := sqlmock.NewRows([]string{"id", "document"})
				if populated && (table == "resources" || table == "endpoints") {
					var value any = resource
					if table == "endpoints" {
						value = obs.Endpoint{ID: "endpoint", Resource: resource, Referenced: true, LastSuccess: now}
					}
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					rows.AddRow("test-id", raw)
				}
				mock.ExpectQuery(regexp.QuoteMeta("SELECT id,document FROM network_observer." + table + " WHERE tenant_id=$1 ORDER BY id LIMIT 20001")).WithArgs(tenant).WillReturnRows(rows)
			}
			mock.ExpectCommit()
			api := &API{Store: &store.Store{DB: db, TenantID: tenant}, Engine: Engine{Network: "42161", Environment: "staging"}, Secret: "test-secret"}
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/candidates", nil)
			r.Header.Set("Authorization", "Bearer "+api.Secret)
			w := httptest.NewRecorder()
			api.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			var feed obs.CandidateFeed
			if err := json.Unmarshal(w.Body.Bytes(), &feed); err != nil {
				t.Fatal(err)
			}
			if feed.Provider != resource.Provider {
				t.Errorf("candidate feed provider=%q, want catalog provider %q", feed.Provider, resource.Provider)
			}
			if feed.Version != 1 || feed.Network != resource.Network || feed.Environment != resource.Environment || !feed.ExpiresAt.After(now) || feed.GeneratedAt.After(time.Now()) {
				t.Errorf("invalid candidate feed namespace/version/lifetime: %+v", feed)
			}
			wantCount := 0
			if populated {
				wantCount = 1
			}
			if len(feed.Candidates) != wantCount {
				t.Errorf("candidate count=%d, want %d", len(feed.Candidates), wantCount)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
