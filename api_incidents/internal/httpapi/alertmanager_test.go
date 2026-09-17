package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"frameworks/api_incidents/internal/incidents"

	"github.com/gin-gonic/gin"
)

type fakeIngester struct {
	calls  int
	result incidents.IngestResult
	err    error
	hook   incidents.AlertmanagerWebhook
}

func (f *fakeIngester) IngestAlertmanager(_ context.Context, hook incidents.AlertmanagerWebhook) (incidents.IngestResult, error) {
	f.calls++
	f.hook = hook
	return f.result, f.err
}

const validBody = `{"groupKey":"g","alerts":[{"status":"firing","fingerprint":"f","startsAt":"2026-09-15T10:00:00Z"}]}`

func serve(t *testing.T, ingester *fakeIngester, token, authorization, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/v1/alertmanager", AlertmanagerHandler(ingester, func() string { return token }, nil, nil))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/alertmanager", strings.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAlertmanagerHandlerRejectsUnauthenticatedRequests(t *testing.T) {
	cases := map[string]struct{ token, header string }{
		"missing header":         {token: "secret", header: ""},
		"wrong token":            {token: "secret", header: "Bearer nope"},
		"basic scheme":           {token: "secret", header: "Basic secret"},
		"unconfigured token":     {token: "", header: "Bearer "},
		"unconfigured non-empty": {token: "", header: "Bearer secret"},
	}
	for name, tc := range cases {
		ingester := &fakeIngester{}
		rec := serve(t, ingester, tc.token, tc.header, validBody)
		if rec.Code != http.StatusUnauthorized || ingester.calls != 0 {
			t.Errorf("%s: status=%d calls=%d, want 401 without ingest", name, rec.Code, ingester.calls)
		}
	}
}

func TestAlertmanagerHandlerStatusCodes(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		err    error
		status int
	}{
		{name: "malformed JSON", body: "{", status: http.StatusBadRequest},
		{name: "invalid notification", body: validBody, err: fmt.Errorf("%w: groupKey is required", incidents.ErrInvalidWebhook), status: http.StatusBadRequest},
		{name: "storage failure is retryable", body: validBody, err: errors.New("database unavailable"), status: http.StatusInternalServerError},
	}
	for _, tc := range cases {
		rec := serve(t, &fakeIngester{err: tc.err}, "secret", "Bearer secret", tc.body)
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d", tc.name, rec.Code, tc.status)
		}
	}
}

func TestAlertmanagerHandlerAcceptsNotification(t *testing.T) {
	ingester := &fakeIngester{result: incidents.IngestResult{Outcome: incidents.IngestCreated, IncidentID: "i1"}}
	rec := serve(t, ingester, "secret", "Bearer secret", validBody)
	if rec.Code != http.StatusOK || ingester.calls != 1 || ingester.hook.GroupKey != "g" {
		t.Fatalf("status=%d calls=%d hook=%+v", rec.Code, ingester.calls, ingester.hook)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["outcome"] != incidents.IngestCreated || body["incident_id"] != "i1" {
		t.Fatalf("body = %s, %v", rec.Body.String(), err)
	}
}
