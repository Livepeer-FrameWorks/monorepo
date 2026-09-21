package frameworks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"
)

type operationsFixture struct {
	Operations []struct {
		Name      string                     `json:"name"`
		Kind      string                     `json:"kind"`
		Variables map[string]json.RawMessage `json:"variables"`
		Response  struct {
			Data json.RawMessage `json:"data"`
		} `json:"response"`
	} `json:"operations"`
}

type vars map[string]json.RawMessage

// arg decodes one fixture variable into the generated argument type; a
// variable the fixture leaves out is the zero value.
func arg[T any](v vars, name string) T {
	var out T
	if raw, ok := v[name]; ok {
		if err := json.Unmarshal(raw, &out); err != nil {
			panic(err)
		}
	}
	return out
}

type operationCall struct {
	query string
	call  func(ctx context.Context, c *Client, v vars) (any, error)
}

// operationCalls calls every generated query and mutation function with the
// fixture's variables. TestGeneratedOperations fails when the fixture has an
// operation this table lacks.
var operationCalls = map[string]operationCall{
	"AbortVodUpload": {AbortVodUpload_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return AbortVodUpload(ctx, c, arg[string](v, "uploadId"))
	}},
	"CompleteVodUpload": {CompleteVodUpload_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return CompleteVodUpload(ctx, c, arg[CompleteVodUploadInput](v, "input"))
	}},
	"CreateClip": {CreateClip_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return CreateClip(ctx, c, arg[CreateClipInput](v, "input"))
	}},
	"CreateDeveloperToken": {CreateDeveloperToken_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return CreateDeveloperToken(ctx, c, arg[CreateDeveloperTokenInput](v, "input"))
	}},
	"CreatePushTarget": {CreatePushTarget_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return CreatePushTarget(ctx, c, arg[string](v, "streamId"), arg[CreatePushTargetInput](v, "input"))
	}},
	"CreateSigningKey": {CreateSigningKey_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return CreateSigningKey(ctx, c, arg[CreateSigningKeyInput](v, "input"))
	}},
	"CreateStream": {CreateStream_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return CreateStream(ctx, c, arg[CreateStreamInput](v, "input"))
	}},
	"CreateStreamKey": {CreateStreamKey_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return CreateStreamKey(ctx, c, arg[string](v, "streamId"), arg[CreateStreamKeyInput](v, "input"))
	}},
	"CreateVodUpload": {CreateVodUpload_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return CreateVodUpload(ctx, c, arg[CreateVodUploadInput](v, "input"))
	}},
	"DeleteClip": {DeleteClip_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return DeleteClip(ctx, c, arg[string](v, "id"))
	}},
	"DeleteDVR": {DeleteDVR_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return DeleteDVR(ctx, c, arg[string](v, "dvrHash"))
	}},
	"DeletePushTarget": {DeletePushTarget_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return DeletePushTarget(ctx, c, arg[string](v, "id"))
	}},
	"DeleteStream": {DeleteStream_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return DeleteStream(ctx, c, arg[string](v, "id"))
	}},
	"DeleteStreamKey": {DeleteStreamKey_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return DeleteStreamKey(ctx, c, arg[string](v, "streamId"), arg[string](v, "keyId"))
	}},
	"DeleteVodAsset": {DeleteVodAsset_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return DeleteVodAsset(ctx, c, arg[string](v, "id"))
	}},
	"GetClip": {GetClip_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return GetClip(ctx, c, arg[string](v, "id"))
	}},
	"GetDVRChapter": {GetDVRChapter_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return GetDVRChapter(ctx, c, arg[string](v, "dvrId"), arg[float64](v, "startMs"), arg[float64](v, "endMs"), arg[*DVRChapterMode](v, "mode"), arg[*int](v, "intervalSeconds"))
	}},
	"GetSigningKey": {GetSigningKey_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return GetSigningKey(ctx, c, arg[string](v, "id"))
	}},
	"GetStream": {GetStream_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return GetStream(ctx, c, arg[string](v, "id"))
	}},
	"GetTenantUsage": {GetTenantUsage_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return GetTenantUsage(ctx, c, arg[*TimeRangeInput](v, "timeRange"))
	}},
	"GetUsageAggregates": {GetUsageAggregates_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return GetUsageAggregates(ctx, c, arg[TimeRangeInput](v, "timeRange"), arg[*string](v, "granularity"), arg[[]string](v, "usageTypes"))
	}},
	"GetVodAsset": {GetVodAsset_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return GetVodAsset(ctx, c, arg[string](v, "id"))
	}},
	"GetVodUploadStatus": {GetVodUploadStatus_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return GetVodUploadStatus(ctx, c, arg[string](v, "uploadId"))
	}},
	"ListArtifacts": {ListArtifacts_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return ListArtifacts(ctx, c, arg[*StorageArtifactsInput](v, "input"))
	}},
	"ListDVRChapters": {ListDVRChapters_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return ListDVRChapters(ctx, c, arg[string](v, "dvrId"), arg[*DVRChapterMode](v, "mode"), arg[*int](v, "intervalSeconds"), arg[*float64](v, "rangeStartMs"), arg[*float64](v, "rangeEndMs"), arg[*int](v, "pageSize"), arg[*string](v, "pageToken"))
	}},
	"ListDeveloperTokens": {ListDeveloperTokens_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return ListDeveloperTokens(ctx, c, arg[*ConnectionInput](v, "page"))
	}},
	"ListPushTargets": {ListPushTargets_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return ListPushTargets(ctx, c, arg[string](v, "streamId"))
	}},
	"ListSigningKeys": {ListSigningKeys_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return ListSigningKeys(ctx, c, arg[*string](v, "status"), arg[*ConnectionInput](v, "page"))
	}},
	"ListStreamKeys": {ListStreamKeys_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return ListStreamKeys(ctx, c, arg[string](v, "streamId"), arg[*ConnectionInput](v, "page"))
	}},
	"ListStreams": {ListStreams_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return ListStreams(ctx, c, arg[*ConnectionInput](v, "page"), arg[*string](v, "search"))
	}},
	"ListUsageRecords": {ListUsageRecords_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return ListUsageRecords(ctx, c, arg[*ConnectionInput](v, "page"), arg[*TimeRangeInput](v, "timeRange"))
	}},
	"RefreshStreamKey": {RefreshStreamKey_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return RefreshStreamKey(ctx, c, arg[string](v, "id"))
	}},
	"ResolveIngestEndpoint": {ResolveIngestEndpoint_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return ResolveIngestEndpoint(ctx, c, arg[string](v, "streamKey"), arg[*MediaIngestProtocol](v, "protocol"))
	}},
	"ResolveViewerEndpoint": {ResolveViewerEndpoint_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return ResolveViewerEndpoint(ctx, c, arg[string](v, "contentId"), arg[*MediaViewerProtocol](v, "protocol"))
	}},
	"RevokeDeveloperToken": {RevokeDeveloperToken_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return RevokeDeveloperToken(ctx, c, arg[string](v, "id"))
	}},
	"RevokeSigningKey": {RevokeSigningKey_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return RevokeSigningKey(ctx, c, arg[string](v, "id"))
	}},
	"ServerInfo": {ServerInfo_Operation, func(ctx context.Context, c *Client, _ vars) (any, error) {
		return ServerInfo(ctx, c)
	}},
	"SetPlaybackPolicy": {SetPlaybackPolicy_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return SetPlaybackPolicy(ctx, c, arg[SetPlaybackPolicyInput](v, "input"))
	}},
	"StartDVR": {StartDVR_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return StartDVR(ctx, c, arg[string](v, "streamId"))
	}},
	"StopDVR": {StopDVR_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return StopDVR(ctx, c, arg[string](v, "dvrHash"))
	}},
	"TestPlaybackAccess": {TestPlaybackAccess_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return TestPlaybackAccess(ctx, c, arg[TestPlaybackAccessInput](v, "input"))
	}},
	"UpdatePushTarget": {UpdatePushTarget_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return UpdatePushTarget(ctx, c, arg[string](v, "id"), arg[UpdatePushTargetInput](v, "input"))
	}},
	"UpdateStream": {UpdateStream_Operation, func(ctx context.Context, c *Client, v vars) (any, error) {
		return UpdateStream(ctx, c, arg[string](v, "id"), arg[UpdateStreamInput](v, "input"))
	}},
}

// sentVariablesMatch reports whether the variables sent carry every fixture
// variable unchanged and nothing else but nulls.
func sentVariablesMatch(sent map[string]any, fixture vars) bool {
	for name, raw := range fixture {
		var want any
		_ = json.Unmarshal(raw, &want)
		if !jsonEqual(sent[name], want) {
			return false
		}
	}
	for name, v := range sent {
		if _, ok := fixture[name]; !ok && v != nil {
			return false
		}
	}
	return true
}

func TestGeneratedOperations(t *testing.T) {
	var fx operationsFixture
	loadFixture(t, "operations.json", &fx)

	names := []string{}
	for _, op := range fx.Operations {
		names = append(names, op.Name)
	}
	manifest := []string{}
	for name := range operations {
		manifest = append(manifest, name)
	}
	sort.Strings(names)
	sort.Strings(manifest)
	if !jsonEqual(names, manifest) {
		t.Fatalf("fixture operations %v differ from the manifest %v", names, manifest)
	}

	for _, op := range fx.Operations {
		t.Run(op.Name, func(t *testing.T) {
			if op.Kind == "subscription" {
				testSubscriptionOperation(t, op.Name, op.Variables, op.Response.Data)
				return
			}
			entry, ok := operationCalls[op.Name]
			if !ok {
				t.Fatalf("no call for %s in operationCalls", op.Name)
			}
			var sent struct {
				Query         string         `json:"query"`
				OperationName string         `json:"operationName"`
				Variables     map[string]any `json:"variables"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &sent)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": op.Response.Data})
			}))
			defer srv.Close()
			c, err := NewClient(ClientOptions{URL: srv.URL, DisableServerCheck: true})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := entry.call(context.Background(), c, op.Variables)
			if err != nil {
				t.Fatalf("%s: %v", op.Name, err)
			}
			if sent.OperationName != op.Name || sent.Query != entry.query {
				t.Errorf("sent operation %q with a different document", sent.OperationName)
			}
			if !sentVariablesMatch(sent.Variables, op.Variables) {
				t.Errorf("sent variables %v, fixture %s", sent.Variables, mustJSON(op.Variables))
			}
			var want any
			_ = json.Unmarshal(op.Response.Data, &want)
			if !jsonEqual(resp, want) {
				t.Errorf("decoded response re-encodes as %s, want %s", mustJSON(resp), op.Response.Data)
			}
		})
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func testSubscriptionOperation(t *testing.T, name string, variables vars, data json.RawMessage) {
	t.Helper()
	if name != "TenantEvents" {
		t.Fatalf("no subscription wrapper for %s", name)
	}
	var mu sync.Mutex
	var subscribed map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			var msg struct {
				Type    string         `json:"type"`
				ID      string         `json:"id"`
				Payload map[string]any `json:"payload"`
			}
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			switch msg.Type {
			case "connection_init":
				_ = conn.WriteJSON(map[string]any{"type": "connection_ack"})
			case "subscribe":
				mu.Lock()
				subscribed = msg.Payload
				mu.Unlock()
				_ = conn.WriteJSON(map[string]any{"id": msg.ID, "type": "next", "payload": map[string]any{"data": data}})
				_ = conn.WriteJSON(map[string]any{"id": msg.ID, "type": "complete"})
			}
		}
	}))
	defer srv.Close()
	sc, err := NewSubscriptionClient(SubscriptionOptions{URL: wsURL(srv.URL), Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var events []*TenantEventsResponse
	for ev, err := range SubscribeTenantEvents(ctx, sc, arg[[]string](variables, "types"), arg[*string](variables, "streamId")) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	var want any
	_ = json.Unmarshal(data, &want)
	if len(events) != 1 || !jsonEqual(events[0], want) {
		t.Fatalf("events = %s, want [%s]", mustJSON(events), data)
	}
	mu.Lock()
	defer mu.Unlock()
	if subscribed["operationName"] != name || subscribed["query"] != TenantEvents_Operation {
		t.Errorf("subscribed with %v", subscribed["operationName"])
	}
}
