package handlers

import (
	"net/http"
	"testing"

	billingmollie "frameworks/api_billing/internal/mollie"
	"github.com/sirupsen/logrus"
)

func TestParseMollieWebhookID(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		contentType string
		want        string
		wantErr     bool
	}{
		{
			name:        "form encoded id",
			body:        "id=tr_WDqYK6vllg",
			contentType: "application/x-www-form-urlencoded",
			want:        "tr_WDqYK6vllg",
		},
		{
			name:        "form encoded with charset",
			body:        "id=tr_WDqYK6vllg",
			contentType: "application/x-www-form-urlencoded; charset=utf-8",
			want:        "tr_WDqYK6vllg",
		},
		{
			name:        "form encoded empty id",
			body:        "id=",
			contentType: "application/x-www-form-urlencoded",
			want:        "",
		},
		{
			name:        "form encoded no id key",
			body:        "foo=bar",
			contentType: "application/x-www-form-urlencoded",
			want:        "",
		},
		{
			name:        "json id for json content type",
			body:        `{"id":"tr_json123","resource":"payment"}`,
			contentType: "application/json",
			want:        "tr_json123",
		},
		{
			name:        "json invalid",
			body:        `{"id":`,
			contentType: "application/json",
			wantErr:     true,
		},
		{
			name:        "no content type defaults to form parser",
			body:        "id=tr_default",
			contentType: "",
			want:        "tr_default",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMollieWebhookID([]byte(tc.body), tc.contentType)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (got=%q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProcessMollieWebhookGRPC_NilClient(t *testing.T) {
	logger = logrus.New()
	mollieClient = nil

	ok, _, code := ProcessMollieWebhookGRPC([]byte("id=tr_x"), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	})
	if ok {
		t.Fatal("expected ok=false")
	}
	if code != 503 {
		t.Fatalf("expected 503, got %d", code)
	}
}

func TestProcessMollieWebhookGRPC_MissingID(t *testing.T) {
	logger = logrus.New()
	mc, err := billingmollie.NewClient(billingmollie.Config{APIKey: "test_unused", Logger: logger})
	if err != nil {
		t.Fatalf("failed to construct mollie client: %v", err)
	}
	mollieClient = mc
	t.Cleanup(func() { mollieClient = nil })

	ok, _, code := ProcessMollieWebhookGRPC([]byte(""), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	})
	if ok {
		t.Fatal("expected ok=false")
	}
	if code != 400 {
		t.Fatalf("expected 400, got %d", code)
	}
}

func TestProcessMollieWebhookGRPC_UnknownPayment(t *testing.T) {
	logger = logrus.New()
	mc, err := billingmollie.NewClient(billingmollie.Config{APIKey: "test_unused", Logger: logger})
	if err != nil {
		t.Fatalf("failed to construct mollie client: %v", err)
	}
	mollieClient = mc
	t.Cleanup(func() { mollieClient = nil })

	withDefaultTransport(t, testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return newJSONResponse(http.StatusNotFound, `{"status":404,"title":"Not Found","detail":"No payment exists with token tr_unknown"}`), nil
	}))

	ok, msg, code := ProcessMollieWebhookGRPC([]byte("id=tr_unknown"), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	})
	if ok {
		t.Fatalf("expected ok=false, got true (msg=%q)", msg)
	}
	if code != 404 {
		t.Fatalf("expected 404, got %d", code)
	}
}
