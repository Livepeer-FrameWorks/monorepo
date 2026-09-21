package frameworks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type uploadFixture struct {
	PresignedSignature string `json:"presignedSignature"`
	Cases              []struct {
		Name           string           `json:"name"`
		SizeBytes      int64            `json:"sizeBytes"`
		PartSize       int64            `json:"partSize"`
		Concurrency    int              `json:"concurrency"`
		PartAttempts   int              `json:"partAttempts"`
		PutFailures    map[string][]int `json:"putFailures"`
		PutRetryAfter  string           `json:"putRetryAfter"`
		PutDrops       map[string]int   `json:"putDrops"`
		CreateResult   map[string]any   `json:"createResult"`
		CompleteResult map[string]any   `json:"completeResult"`
		Expect         struct {
			OK                bool             `json:"ok"`
			Error             map[string]any   `json:"error"`
			ErrorOmits        string           `json:"errorOmits"`
			Puts              map[string]int   `json:"puts"`
			PartBytes         map[string]int   `json:"partBytes"`
			CompletedParts    []map[string]any `json:"completedParts"`
			Aborted           bool             `json:"aborted"`
			Completed         *bool            `json:"completed"`
			MaxConcurrentPuts int              `json:"maxConcurrentPuts"`
		} `json:"expect"`
	} `json:"cases"`
}

var fixtureVodAsset = map[string]any{
	"__typename": "VodAsset", "id": "vod-1", "artifactHash": "hash", "playbackId": "play", "streamId": nil,
	"title": nil, "description": nil, "filename": "file.mp4", "status": "PROCESSING", "sizeBytes": 10,
	"durationMs": nil, "resolution": nil, "videoCodec": nil, "audioCodec": nil, "bitrateKbps": nil,
	"createdAt": "2026-09-19T14:03:27Z", "updatedAt": "2026-09-19T14:03:27Z", "expiresAt": nil,
	"errorMessage": nil, "playbackPolicy": nil, "thumbnailAssets": nil, "effectiveRetention": nil,
}

func TestUploadVod(t *testing.T) {
	var fx uploadFixture
	loadFixture(t, "upload.json", &fx)
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var (
				mu             sync.Mutex
				puts           = map[string]int{}
				partBytes      = map[string]int{}
				inFlight, peak int
				aborted        bool
				completed      bool
				completedParts any
				fileOK         = true
			)
			failures := map[string][]int{}
			for k, v := range tc.PutFailures {
				failures[k] = append([]int(nil), v...)
			}
			drops := map[string]int{}
			for k, v := range tc.PutDrops {
				drops[k] = v
			}
			partCount := int((tc.SizeBytes + tc.PartSize - 1) / tc.PartSize)
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method == http.MethodPut {
					part := r.URL.Query().Get("part")
					mu.Lock()
					puts[part]++
					inFlight++
					peak = max(peak, inFlight)
					mu.Unlock()
					time.Sleep(5 * time.Millisecond)
					mu.Lock()
					inFlight--
					if drops[part] > 0 {
						drops[part]--
						mu.Unlock()
						if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
							_ = conn.Close()
						}
						return
					}
					var failure int
					if q := failures[part]; len(q) > 0 {
						failure, failures[part] = q[0], q[1:]
					}
					if failure == 0 {
						n, _ := strconv.Atoi(part)
						offset := int64(n-1) * tc.PartSize
						for i, b := range body {
							if int64(b) != (offset+int64(i))%251 {
								fileOK = false
							}
						}
						partBytes[part] = len(body)
					}
					mu.Unlock()
					if failure != 0 {
						if tc.PutRetryAfter != "" {
							w.Header().Set("Retry-After", tc.PutRetryAfter)
						}
						w.WriteHeader(failure)
						return
					}
					w.Header().Set("ETag", fmt.Sprintf("%q", "etag-"+part))
					w.WriteHeader(http.StatusOK)
					return
				}
				var req struct {
					OperationName string         `json:"operationName"`
					Variables     map[string]any `json:"variables"`
				}
				_ = json.Unmarshal(body, &req)
				var data map[string]any
				switch req.OperationName {
				case "CreateVodUpload":
					result := tc.CreateResult
					if result == nil {
						parts := []any{}
						for i := 1; i <= partCount; i++ {
							parts = append(parts, map[string]any{"partNumber": i, "presignedUrl": fmt.Sprintf("%s/s3?part=%d&X-Amz-Signature=%s", srv.URL, i, fx.PresignedSignature)})
						}
						result = map[string]any{"__typename": "VodUploadSession", "id": "upload-1", "artifactId": "artifact-1", "artifactHash": "hash", "playbackId": "play", "partSize": tc.PartSize, "parts": parts, "expiresAt": "2026-09-20T14:03:27Z"}
					}
					data = map[string]any{"createVodUpload": result}
				case "CompleteVodUpload":
					mu.Lock()
					completed = true
					completedParts = req.Variables["input"].(map[string]any)["parts"]
					mu.Unlock()
					result := tc.CompleteResult
					if result == nil {
						result = fixtureVodAsset
					}
					data = map[string]any{"completeVodUpload": result}
				case "AbortVodUpload":
					mu.Lock()
					aborted = true
					mu.Unlock()
					data = map[string]any{"abortVodUpload": map[string]any{"__typename": "DeleteSuccess", "success": true, "deletedId": "upload-1", "pending": nil}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer srv.Close()

			c, err := NewClient(ClientOptions{URL: srv.URL + "/graphql", Token: "t", DisableServerCheck: true})
			if err != nil {
				t.Fatal(err)
			}
			c.sleep = func(context.Context, time.Duration) error { return nil }
			file := make([]byte, tc.SizeBytes)
			for i := range file {
				file[i] = byte(i % 251)
			}
			asset, err := UploadVod(context.Background(), c, bytes.NewReader(file), tc.SizeBytes, UploadVodOptions{Filename: "file.mp4", Concurrency: tc.Concurrency, PartAttempts: tc.PartAttempts})
			if tc.Expect.OK {
				if err != nil {
					t.Fatalf("upload failed: %v", err)
				}
				if asset.GetId() != "vod-1" {
					t.Errorf("asset id = %s", asset.GetId())
				}
			} else {
				checkError(t, err, tc.Expect.Error)
				if tc.Expect.ErrorOmits != "" {
					for e := err; e != nil; e = errors.Unwrap(e) {
						if strings.Contains(e.Error(), tc.Expect.ErrorOmits) {
							t.Errorf("error %T %q carries %q", e, e.Error(), tc.Expect.ErrorOmits)
						}
					}
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.Expect.Puts != nil && !jsonEqual(puts, tc.Expect.Puts) {
				t.Errorf("puts = %v, want %v", puts, tc.Expect.Puts)
			}
			if tc.Expect.PartBytes != nil && !jsonEqual(partBytes, tc.Expect.PartBytes) {
				t.Errorf("part bytes = %v, want %v", partBytes, tc.Expect.PartBytes)
			}
			if tc.Expect.CompletedParts != nil && !jsonEqual(completedParts, tc.Expect.CompletedParts) {
				t.Errorf("completed parts = %v, want %v", completedParts, tc.Expect.CompletedParts)
			}
			if tc.Expect.Completed != nil && completed != *tc.Expect.Completed {
				t.Errorf("completed = %v, want %v", completed, *tc.Expect.Completed)
			}
			if tc.Expect.MaxConcurrentPuts > 0 && peak > tc.Expect.MaxConcurrentPuts {
				t.Errorf("peak concurrent PUTs = %d, want at most %d", peak, tc.Expect.MaxConcurrentPuts)
			}
			if aborted != tc.Expect.Aborted {
				t.Errorf("aborted = %v, want %v", aborted, tc.Expect.Aborted)
			}
			if !fileOK {
				t.Error("a part carried the wrong bytes")
			}
		})
	}
}
