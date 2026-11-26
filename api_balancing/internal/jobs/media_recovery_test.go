package jobs

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"testing"
)

type recoveryProofFixture struct {
	proof   *purserpb.MediaRecoveryStatus
	err     error
	commits int
}

func (f *recoveryProofFixture) GetMediaRecoveryStatus(context.Context, string) (*purserpb.MediaRecoveryStatus, error) {
	return f.proof, f.err
}
func (f *recoveryProofFixture) CommitMediaRecoveryExpiry(context.Context, *purserpb.CommitMediaRecoveryExpiryRequest) (*purserpb.MediaRecoveryStatus, error) {
	f.commits++
	return nil, errors.New("funding was restored")
}
func TestMediaRecoveryCannotDeleteWithoutCommittedBillingReceipt(t *testing.T) {
	for _, tc := range []struct {
		name        string
		proof       *purserpb.MediaRecoveryStatus
		err         error
		wantCommits int
	}{
		{name: "unavailable", err: errors.New("billing offline")}, {name: "older server"}, {name: "protected", proof: &purserpb.MediaRecoveryStatus{}}, {name: "funded before expiry", proof: &purserpb.MediaRecoveryStatus{Eligible: true}, wantCommits: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			f := &recoveryProofFixture{proof: tc.proof, err: tc.err}
			j := NewRetentionJob(RetentionConfig{DB: db, Logger: logging.NewLogger(), RecoveryClient: f})
			j.expireMediaRecoveryTenant(t.Context(), "tenant")
			if f.commits != tc.wantCommits {
				t.Fatalf("unexpected expiry commit count %d", f.commits)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
