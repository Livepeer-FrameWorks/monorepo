package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type deviceMetadataClient struct {
	commodore.Interface
	response *commodorepb.LookupDeviceAuthorizationResponse
}

func (c *deviceMetadataClient) LookupDeviceAuthorization(context.Context, *commodorepb.LookupDeviceAuthorizationRequest) (*commodorepb.LookupDeviceAuthorizationResponse, error) {
	return c.response, nil
}

func TestDeviceLookupReturnsStoredRequestMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC().Truncate(time.Second)
	for _, legacy := range []bool{false, true} {
		response := &commodorepb.LookupDeviceAuthorizationResponse{ClientId: "cli", Scope: "account", ExpiresAt: timestamppb.New(now.Add(10 * time.Minute))}
		if !legacy {
			response.DeviceLabel = "Original workstation"
			response.ClientVersion = "cli linux/arm64"
			response.RequestIp = "203.0.113.14"
			response.UserAgent = "original-agent"
			response.RequestedAt = timestamppb.New(now)
			response.Location = "Example City"
		}
		h := NewAuthHandlers(&deviceMetadataClient{response: response}, logging.NewLogger(), AuthConfig{})
		router := gin.New()
		router.POST("/device/lookup", h.DeviceLookup())
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/device/lookup", strings.NewReader(`{"user_code":"ABCD-EFGH","device_label":"browser override","request_ip":"198.51.100.22"}`)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("lookup status=%d body=%s", recorder.Code, recorder.Body)
		}
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if legacy {
			if body["requested_at"] != nil {
				t.Fatalf("legacy lookup invented request time: %+v", body)
			}
			continue
		}
		if body["device_label"] != "Original workstation" || body["client_version"] != "cli linux/arm64" || body["request_ip"] != "203.0.113.14" || body["user_agent"] != "original-agent" || body["location"] != "Example City" || body["requested_at"] != now.Format(time.RFC3339) {
			t.Fatalf("lookup replaced stored metadata with browser input: %+v", body)
		}
	}
}
