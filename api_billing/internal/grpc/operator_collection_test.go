package grpc

import (
	"testing"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestLastGoodAdmissionDoesNotOutliveGrant(t *testing.T) {
	now := time.Now().UTC()
	cache := tenantAdmissionLastGood{now: func() time.Time { return now }}
	cache.store("tenant", &purserpb.GetTenantAdmissionStatusResponse{BillingModel: "postpaid", ValidUntil: timestamppb.New(now.Add(time.Second))})
	now = now.Add(2 * time.Second)
	if _, _, ok := cache.load("tenant"); ok {
		t.Fatal("expired trial remained admitted through the last-good cache")
	}
}
