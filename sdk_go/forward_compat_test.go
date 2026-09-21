package frameworks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type forwardCompatFixture struct {
	Cases []struct {
		Name      string `json:"name"`
		Operation string `json:"operation"`
		Variables vars   `json:"variables"`
		Response  struct {
			Data json.RawMessage `json:"data"`
		} `json:"response"`
		UnknownMember *struct {
			Path     []string        `json:"path"`
			Typename string          `json:"typename"`
			Raw      json.RawMessage `json:"raw"`
		} `json:"unknownMember"`
		ExpectResult *struct {
			Field   string   `json:"field"`
			Success []string `json:"success"`
		} `json:"expectResult"`
		Error     map[string]any `json:"error"`
		EnumValue *struct {
			Path  []string `json:"path"`
			Value string   `json:"value"`
		} `json:"enumValue"`
	} `json:"cases"`
}

// forwardCompatExpect runs ExpectResult with the generated success type of
// each union field the fixture names.
var forwardCompatExpect = map[string]func(v any) (any, error){
	"vodUploadStatus": func(v any) (any, error) {
		return ExpectResult[*GetVodUploadStatusVodUploadStatus](v)
	},
}

// fieldAt follows a fixture path through the generated getters
// (["stream", "ingestMode"] calls GetStream, then GetIngestMode).
func fieldAt(t *testing.T, v any, path []string) any {
	t.Helper()
	for _, name := range path {
		m := reflect.ValueOf(v).MethodByName("Get" + strings.ToUpper(name[:1]) + name[1:])
		if !m.IsValid() {
			t.Fatalf("%T has no getter for %s", v, name)
		}
		v = m.Call(nil)[0].Interface()
	}
	return v
}

func TestForwardCompatibility(t *testing.T) {
	var fx forwardCompatFixture
	loadFixture(t, "forward_compat.json", &fx)
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			entry, ok := operationCalls[tc.Operation]
			if !ok {
				t.Fatalf("no call for %s in operationCalls", tc.Operation)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": tc.Response.Data})
			}))
			defer srv.Close()
			c, err := NewClient(ClientOptions{URL: srv.URL, DisableServerCheck: true})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := entry.call(context.Background(), c, tc.Variables)
			if err != nil {
				t.Fatalf("%s: %v", tc.Operation, err)
			}
			var want any
			_ = json.Unmarshal(tc.Response.Data, &want)
			if !jsonEqual(resp, want) {
				t.Errorf("decoded response re-encodes as %s, want %s", mustJSON(resp), tc.Response.Data)
			}
			if um := tc.UnknownMember; um != nil {
				got, ok := fieldAt(t, resp, um.Path).(*UnknownMember)
				if !ok {
					t.Fatalf("%v decoded as %T, want *UnknownMember", um.Path, fieldAt(t, resp, um.Path))
				}
				var raw any
				_ = json.Unmarshal(um.Raw, &raw)
				if got.Typename != um.Typename || !jsonEqual(got.Raw, raw) {
					t.Errorf("unknown member = %s %s, want %s %s", got.Typename, got.Raw, um.Typename, um.Raw)
				}
			}
			if ev := tc.EnumValue; ev != nil {
				got := reflect.ValueOf(fieldAt(t, resp, ev.Path))
				if got.Kind() != reflect.String || got.String() != ev.Value {
					t.Errorf("enum at %v = %v, want %q", ev.Path, got, ev.Value)
				}
			}
			if er := tc.ExpectResult; er != nil {
				expect, ok := forwardCompatExpect[er.Field]
				if !ok {
					t.Fatalf("no success type for %s in forwardCompatExpect", er.Field)
				}
				_, err := expect(fieldAt(t, resp, []string{er.Field}))
				if tc.Error != nil {
					checkError(t, err, tc.Error)
				} else if err != nil {
					t.Errorf("ExpectResult: %v", err)
				}
			}
		})
	}
}
