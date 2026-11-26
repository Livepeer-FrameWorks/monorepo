package jobs

import (
	"context"
	"database/sql"
	"frameworks/api_balancing/internal/database/foghorndb"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"time"
)

type mediaRecoveryClient interface {
	GetMediaRecoveryStatus(context.Context, string) (*purserpb.MediaRecoveryStatus, error)
	CommitMediaRecoveryExpiry(context.Context, *purserpb.CommitMediaRecoveryExpiryRequest) (*purserpb.MediaRecoveryStatus, error)
}

func (j *RetentionJob) scanMediaRecovery(ctx context.Context) {
	if j.recoveryClient == nil || !j.recoveryMu.TryLock() {
		return
	}
	defer j.recoveryMu.Unlock()
	if j.recoveryCursor == "" {
		j.recoveryCursor = uuid.Nil.String()
	}
	tenants, err := foghorndb.New(j.db).ListMediaRecoveryTenants(ctx, j.recoveryCursor)
	if err != nil {
		j.logger.WithError(err).Warn("Media recovery tenant discovery failed")
		return
	}
	for _, tenant := range tenants {
		if ctx.Err() != nil {
			return
		}
		j.recoveryCursor = tenant
		tenantCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		j.expireMediaRecoveryTenant(tenantCtx, tenant)
		cancel()
	}
	if len(tenants) < 20 {
		j.recoveryCursor = uuid.Nil.String()
	}
}
func (j *RetentionJob) expireMediaRecoveryTenant(ctx context.Context, tenant string) {
	proof, err := j.recoveryClient.GetMediaRecoveryStatus(ctx, tenant)
	if err != nil || proof == nil {
		j.logger.WithError(err).WithField("tenant_id", tenant).Warn("Media recovery waits for fresh billing evidence")
		return
	}
	if proof.GetEligible() {
		proof, err = j.recoveryClient.CommitMediaRecoveryExpiry(ctx, &purserpb.CommitMediaRecoveryExpiryRequest{TenantId: tenant, ExpectedCutoffAt: proof.CutoffAt, ExpectedRecoveryUntil: proof.RecoveryUntil})
		if err != nil || proof == nil {
			j.logger.WithError(err).WithField("tenant_id", tenant).Warn("Media recovery expiry was not committed")
			return
		}
	}
	if proof.CommittedCutoffAt == nil {
		return
	}
	cutoff := proof.CommittedCutoffAt.AsTime()
	artifacts, err := foghorndb.New(j.db).ListMediaRecoveryArtifacts(ctx, foghorndb.ListMediaRecoveryArtifactsParams{TenantID: tenant, CutoffAt: cutoff})
	if err != nil {
		j.logger.WithError(err).Warn("Media recovery artifact discovery failed")
		return
	}
	for _, a := range artifacts {
		if ctx.Err() != nil {
			return
		}
		j.expireArtifactWithGuard(ctx, a.ArtifactHash, a.ArtifactType, a.StreamInternalName, sql.NullString{String: tenant, Valid: true}, sql.NullString{String: a.UserID, Valid: a.UserID != ""}, a.SizeBytes, a.RetentionUntil, func(tx *sql.Tx) (string, error) {
			return foghorndb.New(tx).ExpireMediaRecoveryArtifact(ctx, foghorndb.ExpireMediaRecoveryArtifactParams{TenantID: tenant, ArtifactHash: a.ArtifactHash, CutoffAt: cutoff})
		})
	}
}
