package grpc

import (
	"math"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const oneGB = int64(1) << 30

func TestStorageCapDecisionUnlimited(t *testing.T) {
	if err := storageCapDecision(50*oneGB, 5*oneGB, 0); err != nil {
		t.Errorf("expected admit when limit=0 (unlimited), got %v", err)
	}
	if err := storageCapDecision(50*oneGB, 5*oneGB, -1); err != nil {
		t.Errorf("expected admit when limit<=0, got %v", err)
	}
}

func TestStorageCapDecisionBelowCap(t *testing.T) {
	// 5 GB used, adding 2 GB, limit 10 GB → admit
	if err := storageCapDecision(5*oneGB, 2*oneGB, 10*oneGB); err != nil {
		t.Errorf("expected admit (5+2 < 10), got %v", err)
	}
}

func TestStorageCapDecisionAdditionalWouldExceed(t *testing.T) {
	// 5 GB used, adding 6 GB, limit 10 GB → reject (5+6 > 10)
	err := storageCapDecision(5*oneGB, 6*oneGB, 10*oneGB)
	if err == nil {
		t.Fatal("expected rejection")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Errorf("expected ResourceExhausted, got %v", status.Code(err))
	}
}

func TestStorageCapDecisionAtCapRejectsAnyNewWrite(t *testing.T) {
	// 10 GB used, adding 0 (unknown size), limit 10 GB → reject (at cap)
	err := storageCapDecision(10*oneGB, 0, 10*oneGB)
	if err == nil {
		t.Fatal("at-cap must reject zero-size new write")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Errorf("expected ResourceExhausted, got %v", status.Code(err))
	}
}

func TestStorageCapDecisionUnderCapZeroSizeAdmits(t *testing.T) {
	// 5 GB used, adding 0 (DVR start / clip export), limit 10 GB → admit
	if err := storageCapDecision(5*oneGB, 0, 10*oneGB); err != nil {
		t.Errorf("expected admit (zero-size under cap), got %v", err)
	}
}

func TestStorageCapDecisionExactlyAtCapWithZeroSizeRejects(t *testing.T) {
	// Edge: current==limit. Any new write rejected.
	err := storageCapDecision(10*oneGB, 0, 10*oneGB)
	if err == nil {
		t.Fatal("current==limit must reject")
	}
}

func TestStorageCapDecisionWouldExceedExactlyAtCapRejects(t *testing.T) {
	// 5 GB used, adding 5 GB exactly to hit cap → admit (5+5 == 10, not >)
	if err := storageCapDecision(5*oneGB, 5*oneGB, 10*oneGB); err != nil {
		t.Errorf("expected admit when sum equals cap, got %v", err)
	}
	// 5 GB used, adding 5GB+1B → reject
	if err := storageCapDecision(5*oneGB, 5*oneGB+1, 10*oneGB); err == nil {
		t.Fatal("sum > cap by 1 byte must reject")
	}
}

func TestStorageCapCarriesPublicQuotaReason(t *testing.T) {
	err := storageCapDecision(11*oneGB, 0, 10*oneGB)
	st := status.Convert(err)
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.GetDomain() == "storage.frameworks.network" && info.GetReason() == "STORAGE_LIMIT_REACHED" {
			if info.GetMetadata()["used_bytes"] != "11811160064" || info.GetMetadata()["limit_bytes"] != "10737418240" {
				t.Fatalf("quota metadata=%v", info.GetMetadata())
			}
			return
		}
	}
	t.Fatalf("storage refusal has no typed STORAGE_LIMIT_REACHED reason: %v", err)
}

func TestStorageCapRejectsOverflowingDeclaredSize(t *testing.T) {
	if err := storageCapDecision(oneGB, math.MaxInt64, 10*oneGB); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("overflowing declared upload size admitted: %v", err)
	}
}
