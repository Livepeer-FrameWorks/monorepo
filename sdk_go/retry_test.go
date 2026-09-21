package frameworks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Khan/genqlient/graphql"
)

type retryFixture struct {
	Policy struct {
		MaxAttempts     int  `json:"maxAttempts"`
		BaseDelayMs     int  `json:"baseDelayMs"`
		MaxDelayMs      int  `json:"maxDelayMs"`
		MaxRetryAfterMs int  `json:"maxRetryAfterMs"`
		Jitter          bool `json:"jitter"`
	} `json:"policy"`
	Cases []struct {
		Name           string            `json:"name"`
		Kind           string            `json:"kind"`
		IdempotencyKey string            `json:"idempotencyKey"`
		Responses      []fixtureResponse `json:"responses"`
		Expect         struct {
			Attempts        int             `json:"attempts"`
			DelaysMs        []int64         `json:"delaysMs"`
			Data            json.RawMessage `json:"data"`
			Error           map[string]any  `json:"error"`
			IdempotencyKeys []string        `json:"idempotencyKeys"`
		} `json:"expect"`
	} `json:"cases"`
}

func TestRetryMatrix(t *testing.T) {
	var fx retryFixture
	loadFixture(t, "retry.json", &fx)
	policy := RetryPolicy{
		MaxAttempts:   fx.Policy.MaxAttempts,
		BaseDelay:     time.Duration(fx.Policy.BaseDelayMs) * time.Millisecond,
		MaxDelay:      time.Duration(fx.Policy.MaxDelayMs) * time.Millisecond,
		MaxRetryAfter: time.Duration(fx.Policy.MaxRetryAfterMs) * time.Millisecond,
		Jitter:        fx.Policy.Jitter,
	}
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			transport := newScripted(map[string][]fixtureResponse{"*": tc.Responses})
			c, err := NewClient(ClientOptions{URL: "https://retry.test/graphql", HTTPClient: &http.Client{Transport: transport}, Retry: &policy, DisableServerCheck: true})
			if err != nil {
				t.Fatal(err)
			}
			delays := recordSleeps(c)
			ctx := context.Background()
			if tc.IdempotencyKey != "" {
				ctx = WithIdempotencyKey(ctx, tc.IdempotencyKey)
			}
			var data json.RawMessage
			err = c.MakeRequest(ctx, &graphql.Request{Query: fmt.Sprintf("%s RetryProbe { ok }", tc.Kind), OpName: "RetryProbe"}, &graphql.Response{Data: &data})
			if tc.Expect.Error != nil {
				checkError(t, err, tc.Expect.Error)
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				var got, want any
				_ = json.Unmarshal(data, &got)
				_ = json.Unmarshal(tc.Expect.Data, &want)
				if !reflect.DeepEqual(got, want) {
					t.Errorf("data = %s, want %s", data, tc.Expect.Data)
				}
			}
			if len(transport.requests) != tc.Expect.Attempts {
				t.Errorf("attempts = %d, want %d", len(transport.requests), tc.Expect.Attempts)
			}
			if len(*delays) != len(tc.Expect.DelaysMs) || (len(tc.Expect.DelaysMs) > 0 && !reflect.DeepEqual(*delays, tc.Expect.DelaysMs)) {
				t.Errorf("delays = %v, want %v", *delays, tc.Expect.DelaysMs)
			}
			if tc.Expect.IdempotencyKeys != nil {
				var keys []string
				for _, r := range transport.requests {
					keys = append(keys, r.Header.Get("Idempotency-Key"))
				}
				if !reflect.DeepEqual(keys, tc.Expect.IdempotencyKeys) {
					t.Errorf("idempotency keys = %v, want %v", keys, tc.Expect.IdempotencyKeys)
				}
			}
		})
	}
}

func TestBackoffJitterStaysWithinHalfAndFull(t *testing.T) {
	p := DefaultRetryPolicy
	for i := 0; i < 100; i++ {
		d := backoffDelay(p, 2)
		if d < 250*time.Millisecond || d > 500*time.Millisecond {
			t.Fatalf("jittered second retry %v outside [250ms, 500ms]", d)
		}
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	if n, ok := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); !ok || n != 90 {
		t.Fatalf("HTTP date Retry-After = %d, %v; want 90, true", n, ok)
	}
}
