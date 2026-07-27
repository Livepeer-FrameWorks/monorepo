package grpc

import (
	"context"
	"database/sql"

	"frameworks/api_balancing/internal/jobs"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
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
