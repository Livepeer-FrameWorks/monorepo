package handlers

import (
	"context"
	"encoding/json"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/networkobserver"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	common "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net/http"
	"strings"
	"sync"
	"time"
)

type NetworkReader interface {
	GetNetworkObservations(context.Context, *pb.NetworkObservationsRequest) (*pb.NetworkObservationsResponse, error)
}
type NetworkHandlers struct {
	JWTSecret                   []byte
	Logger                      logging.Logger
	Observer                    *networkobserver.Client
	Analytics                   NetworkReader
	TenantIDs                   []string
	Token, Network, Environment string
	mu                          sync.Mutex
	active                      int
}

func (h *NetworkHandlers) Public(c *gin.Context) { h.serve(c, false) }
func (h *NetworkHandlers) Diagnostics(c *gin.Context) {
	// A JWT alone grants no access to platform-wide customer correlation.
	claims, err := auth.ValidateInteractiveJWT(ctxkeys.GetJWTToken(c.Request.Context()), h.JWTSecret)
	allowed := err == nil && claims.HasRole("platform_operator")
	if h.Logger != nil {
		h.Logger.WithFields(logging.Fields{"audit": "platform_admin_read", "surface": "network_diagnostics", "operator_id": ctxkeys.GetUserID(c.Request.Context()), "allowed": allowed}).Info("Platform network diagnostics access")
	}
	if !allowed {
		c.JSON(403, gin.H{"error": "platform operator access required"})
		return
	}

	h.serve(c, true)
}
func (h *NetworkHandlers) serve(c *gin.Context, details bool) {
	if h.Observer == nil || h.Analytics == nil || len(h.TenantIDs) == 0 {
		c.JSON(503, gin.H{"error": "network observations are not configured"})
		return
	}
	for _, id := range h.TenantIDs {
		if _, err := uuid.Parse(id); err != nil {
			c.JSON(503, gin.H{"error": "network scope is not configured"})
			return
		}
	}
	h.mu.Lock()
	if h.active >= 8 {
		h.mu.Unlock()
		c.JSON(429, gin.H{"error": "network query capacity reached"})
		return
	}
	h.active++
	h.mu.Unlock()
	defer func() { h.mu.Lock(); h.active--; h.mu.Unlock() }()
	end := time.Now().UTC()
	start := end.Add(-24 * time.Hour)
	var err error
	if value := c.Query("start"); value != "" {
		start, err = time.Parse(time.RFC3339, value)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid start time"})
			return
		}
	}
	if value := c.Query("end"); value != "" {
		end, err = time.Parse(time.RFC3339, value)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid end time"})
			return
		}
	}
	if !end.After(start) || end.Sub(start) > 730*24*time.Hour {
		c.JSON(400, gin.H{"error": "time window must be within 730 days"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+h.Token))
	req := &pb.NetworkObservationsRequest{TenantIds: h.TenantIDs, Provider: "livepeer", NetworkId: h.Network, Environment: h.Environment, ResourceId: strings.ToLower(c.Query("resource")), ResolvedIp: c.Query("ip"), GatewayRegion: c.Query("region"), Interval: c.DefaultQuery("interval", "5m"), Limit: 5000, IncludeDetails: details, TimeRange: &common.TimeRange{Start: timestamppb.New(start), End: timestamppb.New(end)}}
	inventory, err := h.Observer.Inventory(ctx, c.Query("include_inactive") == "true")
	if err != nil {
		c.JSON(503, gin.H{"error": "network inventory temporarily unavailable"})
		return
	}
	// Never expose a namespace from a misconfigured coordinator deployment.
	if inventory.Network != h.Network || inventory.Environment != h.Environment {
		c.JSON(503, gin.H{"error": "network inventory namespace mismatch"})
		return
	}
	data := obs.History{GeneratedAt: time.Now().UTC(), Start: start, End: end, Cohorts: []obs.Cohort{}, Summaries: []obs.Cohort{}, Observations: []obs.Metric{}}
	historyStatus := "available"
	history, historyErr := h.Analytics.GetNetworkObservations(ctx, req)
	if historyErr != nil || history == nil || json.Unmarshal(history.Json, &data) != nil {
		historyStatus = "unavailable"
		if h.Logger != nil {
			h.Logger.WithError(historyErr).Warn("Network history unavailable; inventory remains visible")
		}
	}
	if !details {
		data.Details = nil
		for i := range data.Observations {
			data.Observations[i].ObserverID = ""
		}
		for i := range inventory.Endpoints {
			inventory.Endpoints[i].Checks = nil
			inventory.Endpoints[i].AssignedObservers = nil
		}
	}
	c.Header("Cache-Control", "public, max-age=15")
	if details {
		c.Header("Cache-Control", "no-store")
	}
	c.JSON(http.StatusOK, gin.H{"inventory": inventory, "history": data, "history_status": historyStatus})
}
