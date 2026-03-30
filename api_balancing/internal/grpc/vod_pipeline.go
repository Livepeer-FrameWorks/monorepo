package grpc

import (
	"context"
	"database/sql"
	"time"

	"frameworks/api_balancing/internal/jobs"
	"frameworks/pkg/clients/decklog"
	"frameworks/pkg/logging"

	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

// vodPipeline manages the post-upload VOD processing lifecycle.
// After S3 multipart upload completes, a single "process" job is queued.
// The process+ wildcard stream handles everything: MistServer parses headers
// (metadata extraction), runs MistProc* (thumbnails/transcodes), and auto-pushes
// the merged output to disk. Metadata comes back in ProcessingJobResult.outputs.
var vodPipeline *VodPipeline

type VodPipeline struct {
	db            *sql.DB
	logger        logging.Logger
	decklogClient *decklog.BatchedClient
}

func InitVodPipeline(db *sql.DB, logger logging.Logger, decklogClient *decklog.BatchedClient) {
	vodPipeline = &VodPipeline{
		db:            db,
		logger:        logger,
		decklogClient: decklogClient,
	}
}

func GetVodPipeline() *VodPipeline {
	return vodPipeline
}

// StartPipeline queues a process job for a newly uploaded VOD.
// Called from CompleteVodUpload after S3 multipart is finalized.
func (p *VodPipeline) StartPipeline(ctx context.Context, tenantID, artifactHash, processesJSON string) error {
	_, err := jobs.InsertProcessingJob(ctx, p.db, tenantID, artifactHash, "process", nil, processesJSON)
	if err != nil {
		p.logger.WithError(err).WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"tenant_id":     tenantID,
		}).Error("Failed to insert process job")
		return err
	}

	p.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"tenant_id":     tenantID,
	}).Info("VOD processing pipeline started")
	return nil
}

// HandleJobResult processes a completed/failed processing job result.
// Metadata (codec, resolution, etc.) is extracted from outputs and stored
// in vod_metadata — MistServer provides this when the stream boots.
func (p *VodPipeline) HandleJobResult(ctx context.Context, jobID, resultStatus string, outputs map[string]string, errorMsg string) {
	var artifactHash, tenantID string
	err := p.db.QueryRowContext(ctx, `
		SELECT artifact_hash, tenant_id
		FROM foghorn.processing_jobs
		WHERE job_id = $1
	`, jobID).Scan(&artifactHash, &tenantID)
	if err != nil {
		p.logger.WithError(err).WithField("job_id", jobID).Error("Failed to look up processing job for pipeline")
		return
	}

	log := p.logger.WithFields(logging.Fields{
		"job_id":        jobID,
		"artifact_hash": artifactHash,
		"status":        resultStatus,
	})

	if resultStatus == "failed" {
		log.WithField("error", errorMsg).Error("Processing failed")
		p.markArtifactFailed(ctx, log, artifactHash, tenantID, errorMsg)
		return
	}

	// Populate vod_metadata from stream info returned by Helmsman
	if len(outputs) > 0 {
		p.updateVodMetadata(ctx, log, artifactHash, outputs)
	}

	p.markArtifactReady(ctx, log, artifactHash, tenantID)
	log.Info("VOD processing complete")
}

func (p *VodPipeline) updateVodMetadata(ctx context.Context, log *logrus.Entry, artifactHash string, outputs map[string]string) {
	_, err := p.db.ExecContext(ctx, `
		UPDATE foghorn.vod_metadata
		SET duration_ms = $2::integer,
		    resolution = $3,
		    video_codec = $4,
		    audio_codec = $5,
		    bitrate_kbps = $6::integer,
		    width = $7::integer,
		    height = $8::integer,
		    fps = $9::real,
		    audio_channels = $10::integer,
		    audio_sample_rate = $11::integer,
		    updated_at = NOW()
		WHERE artifact_hash = $1
	`,
		artifactHash,
		nullIfEmpty(outputs["duration_ms"]),
		nullIfEmpty(outputs["resolution"]),
		nullIfEmpty(outputs["video_codec"]),
		nullIfEmpty(outputs["audio_codec"]),
		nullIfEmpty(outputs["bitrate_kbps"]),
		nullIfEmpty(outputs["width"]),
		nullIfEmpty(outputs["height"]),
		nullIfEmpty(outputs["fps"]),
		nullIfEmpty(outputs["audio_channels"]),
		nullIfEmpty(outputs["audio_sample_rate"]),
	)
	if err != nil {
		log.WithError(err).Error("Failed to update vod_metadata")
	}
}

func (p *VodPipeline) markArtifactReady(ctx context.Context, log *logrus.Entry, artifactHash, tenantID string) {
	_, err := p.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = 'ready',
		    updated_at = NOW()
		WHERE artifact_hash = $1
	`, artifactHash)
	if err != nil {
		log.WithError(err).Error("Failed to mark artifact ready")
		return
	}

	log.Info("Artifact marked ready")

	if p.decklogClient != nil {
		vodData := &pb.VodLifecycleData{
			Status:      pb.VodLifecycleData_STATUS_COMPLETED,
			VodHash:     artifactHash,
			TenantId:    &tenantID,
			CompletedAt: proto.Int64(time.Now().Unix()),
		}
		go func() { _ = p.decklogClient.SendVodLifecycle(vodData) }()
	}
}

func (p *VodPipeline) markArtifactFailed(ctx context.Context, log *logrus.Entry, artifactHash, tenantID, errorMsg string) {
	_, err := p.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = 'failed',
		    updated_at = NOW()
		WHERE artifact_hash = $1
	`, artifactHash)
	if err != nil {
		log.WithError(err).Error("Failed to mark artifact as failed")
		return
	}

	log.Error("Artifact marked failed")

	if p.decklogClient != nil {
		errStr := errorMsg
		vodData := &pb.VodLifecycleData{
			Status:   pb.VodLifecycleData_STATUS_FAILED,
			VodHash:  artifactHash,
			TenantId: &tenantID,
			Error:    &errStr,
		}
		go func() {
			if err := p.decklogClient.SendVodLifecycle(vodData); err != nil {
				p.logger.WithError(err).Warn("Failed to send VOD failed lifecycle event")
			}
		}()
	}
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
