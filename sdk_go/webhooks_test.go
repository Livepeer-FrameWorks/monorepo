package frameworks

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"
	"google.golang.org/protobuf/encoding/protojson"
)

type webhooksFixture struct {
	Verify []struct {
		Name    string            `json:"name"`
		Secret  string            `json:"secret"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
		Now     int64             `json:"now"`
		Valid   bool              `json:"valid"`
	} `json:"verify"`
	Parse []struct {
		Name   string `json:"name"`
		Body   string `json:"body"`
		Expect struct {
			Known        bool              `json:"known"`
			ID           string            `json:"id"`
			Type         string            `json:"type"`
			APIVersion   string            `json:"apiVersion"`
			CreatedAt    string            `json:"createdAt"`
			ExpectFields map[string]string `json:"expectFields"`
			RawData      json.RawMessage   `json:"rawData"`
		} `json:"expect"`
	} `json:"parse"`
}

func TestWebhookVerification(t *testing.T) {
	var fx webhooksFixture
	loadFixture(t, "webhooks.json", &fx)
	for _, tc := range fx.Verify {
		t.Run(tc.Name, func(t *testing.T) {
			r, err := NewWebhookReceiver(tc.Secret)
			if err != nil {
				t.Fatal(err)
			}
			r.now = func() time.Time { return time.Unix(tc.Now, 0) }
			h := http.Header{}
			for k, v := range tc.Headers {
				h.Set(k, v)
			}
			err = r.Verify([]byte(tc.Body), h)
			if tc.Valid && err != nil {
				t.Fatalf("valid request rejected: %v", err)
			}
			var verr *WebhookVerificationError
			if !tc.Valid && !errors.As(err, &verr) {
				t.Fatalf("invalid request: err = %v, want *WebhookVerificationError", err)
			}
		})
	}
}

// protoJSONPath reads a dot path from a payload's standard protobuf JSON.
func protoJSONPath(t *testing.T, ev *WebhookEvent, path string) any {
	t.Helper()
	raw, err := protojson.MarshalOptions{UseProtoNames: false}.Marshal(ev.Data)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, part := range strings.Split(path, ".") {
		m, _ := v.(map[string]any)
		v = m[part]
	}
	return v
}

func TestWebhookParsing(t *testing.T) {
	var fx webhooksFixture
	loadFixture(t, "webhooks.json", &fx)
	for _, tc := range fx.Parse {
		t.Run(tc.Name, func(t *testing.T) {
			ev, err := ParseWebhookEvent([]byte(tc.Body))
			if err != nil {
				t.Fatal(err)
			}
			if ev.Known != tc.Expect.Known || ev.ID != tc.Expect.ID || ev.Type != tc.Expect.Type || ev.APIVersion != tc.Expect.APIVersion || ev.CreatedAt != tc.Expect.CreatedAt {
				t.Fatalf("event = %+v, want %+v", ev, tc.Expect)
			}
			if tc.Expect.Known {
				for path, want := range tc.Expect.ExpectFields {
					if got := protoJSONPath(t, ev, path); got != want {
						t.Errorf("%s = %v, want %s", path, got, want)
					}
				}
				return
			}
			var got, want any
			_ = json.Unmarshal(ev.RawData, &got)
			_ = json.Unmarshal(tc.Expect.RawData, &want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("raw data = %s, want %s", ev.RawData, tc.Expect.RawData)
			}
		})
	}
}

func TestWebhookRoundTripAgainstALocalServer(t *testing.T) {
	var fx webhooksFixture
	loadFixture(t, "webhooks.json", &fx)
	secret := "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	receiver, err := NewWebhookReceiver(secret)
	if err != nil {
		t.Fatal(err)
	}
	var received []*WebhookEvent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ev, err := receiver.Receive(body, r.Header)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		received = append(received, ev)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	body := []byte(fx.Parse[1].Body)
	msgID := "0199a3c3-0000-7000-8000-000000000001"
	now := time.Now()
	send := func(signingSecret string) int {
		signer, err := standardwebhooks.NewWebhook(signingSecret)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := signer.Sign(msgID, now, body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("webhook-id", msgID)
		req.Header.Set("webhook-timestamp", strconv.FormatInt(now.Unix(), 10))
		req.Header.Set("webhook-signature", sig)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	if got := send(secret); got != http.StatusNoContent {
		t.Fatalf("signed request: status %d", got)
	}
	if got := send("whsec_" + base64.StdEncoding.EncodeToString([]byte("another secret key!!"))); got != http.StatusUnauthorized {
		t.Fatalf("request signed with another secret: status %d", got)
	}
	if len(received) != 1 || received[0].Type != "stream.live" || !received[0].Known {
		t.Fatalf("received = %+v", received)
	}
}
