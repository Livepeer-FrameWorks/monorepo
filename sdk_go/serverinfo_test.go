package frameworks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

type serverInfoFixture struct {
	Cases []struct {
		Name           string                       `json:"name"`
		OperationSince map[string]string            `json:"operationSince"`
		Responses      map[string][]fixtureResponse `json:"responses"`
		Calls          []serverInfoStep             `json:"calls"`
		Expect         struct {
			Calls []struct {
				OK    bool           `json:"ok"`
				Error map[string]any `json:"error"`
			} `json:"calls"`
			Sent          map[string]int `json:"sent"`
			ServerVersion *string        `json:"serverVersion"`
			Verified      *bool          `json:"verified"`
		} `json:"expect"`
	} `json:"cases"`
}

// serverInfoStep is one calls entry: an operation name, or {advanceMs}.
type serverInfoStep struct {
	Call      string
	AdvanceMs int64
}

func (s *serverInfoStep) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &s.Call); err == nil {
		return nil
	}
	var advance struct {
		AdvanceMs int64 `json:"advanceMs"`
	}
	if err := json.Unmarshal(data, &advance); err != nil {
		return err
	}
	s.AdvanceMs = advance.AdvanceMs
	return nil
}

func TestServerInfoGate(t *testing.T) {
	var fx serverInfoFixture
	loadFixture(t, "server_info.json", &fx)
	// serverVersion may be listed as null, which a *string cannot tell from
	// absent; read the raw expectation too.
	var raw struct {
		Cases []struct {
			Expect map[string]any `json:"expect"`
		} `json:"cases"`
	}
	loadFixture(t, "server_info.json", &raw)
	for i, tc := range fx.Cases {
		_, checkVersion := raw.Cases[i].Expect["serverVersion"]
		t.Run(tc.Name, func(t *testing.T) {
			transport := newScripted(tc.Responses)
			c, err := NewClient(ClientOptions{
				URL:        fmt.Sprintf("https://server-info-%d.test/graphql", i),
				HTTPClient: &http.Client{Transport: transport},
				Retry:      &RetryPolicy{MaxAttempts: 1},
			})
			if err != nil {
				t.Fatal(err)
			}
			c.operationSince = tc.OperationSince
			clock := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
			c.now = func() time.Time { return clock }
			ctx := context.Background()
			j := 0
			for _, step := range tc.Calls {
				switch step.Call {
				case "":
					clock = clock.Add(time.Duration(step.AdvanceMs) * time.Millisecond)
					continue
				case "GetStream":
					_, err = GetStream(ctx, c, "s1")
				case "DeleteStream":
					_, err = DeleteStream(ctx, c, "s1")
				default:
					t.Fatalf("unknown call %s", step.Call)
				}
				want := tc.Expect.Calls[j]
				j++
				if want.Error != nil {
					checkError(t, err, want.Error)
				} else if err != nil {
					t.Errorf("call %d: unexpected error %v", j, err)
				}
			}
			for name, want := range tc.Expect.Sent {
				if got := transport.sent(name); got != want {
					t.Errorf("sent %s = %d, want %d", name, got, want)
				}
			}
			if tc.Expect.Verified != nil || checkVersion {
				status, err := c.ServerInfo(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if tc.Expect.Verified != nil && status.Verified != *tc.Expect.Verified {
					t.Errorf("verified = %v, want %v", status.Verified, *tc.Expect.Verified)
				}
				if checkVersion && fmt.Sprint(derefOrNil(status.Version)) != fmt.Sprint(derefOrNil(tc.Expect.ServerVersion)) {
					t.Errorf("version = %v, want %v", derefOrNil(status.Version), derefOrNil(tc.Expect.ServerVersion))
				}
			}
		})
	}
}
