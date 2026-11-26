package grpc

import (
	"database/sql"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/protobuf/types/known/timestamppb"
	"math"
	"time"
)

func paymentGraceSnapshot(model, status string, available int64, ends sql.NullTime, credit int64, recovery sql.NullTime) *purserpb.PaymentGrace {
	result := &purserpb.PaymentGrace{}
	if ends.Valid {
		result.EndsAt = timestamppb.New(ends.Time)
	}
	if recovery.Valid {
		result.MediaRecoveryUntil = timestamppb.New(recovery.Time)
	}
	if model != "prepaid" || status != "active" || !ends.Valid || !ends.Time.After(time.Now()) || credit <= 0 {
		return result
	}
	// Compare before adding: malformed/extreme balances cannot wrap into credit.
	if available <= -credit {
		return result
	}
	result.Active = available <= 0
	if available > math.MaxInt64-credit {
		result.RemainingCreditCents = credit
	} else {
		result.RemainingCreditCents = available + credit
	}
	if result.RemainingCreditCents > credit {
		result.RemainingCreditCents = credit
	}
	return result
}
