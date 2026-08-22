package commodoredb

import "database/sql"

// StreamConfigRow is the shared projection returned by the generated stream
// read variants. The variants differ only in filtering and pagination.
type StreamConfigRow struct {
	ID, InternalName, StreamKey, PlaybackID, Title, IngestMode                     string
	Description, SourceURIEnc, ActiveIngestClusterID, DVRChapterMode               sql.NullString
	IsRecordingEnabled, PullEnabled, MonitoringEnabled                             sql.NullBool
	CreatedAt, UpdatedAt                                                           sql.NullTime
	AllowedClusterIDs                                                              []string
	DVRChapterIntervalSeconds, DVRRetentionDaysOverride, ClipRetentionDaysOverride sql.NullInt32
}

func streamConfigRow(
	id, internalName, streamKey, playbackID, title string,
	description sql.NullString, recording sql.NullBool, createdAt, updatedAt sql.NullTime,
	ingestMode string, sourceURI sql.NullString, pullEnabled sql.NullBool,
	allowed []string, activeIngest, chapterMode sql.NullString, chapterInterval,
	dvrRetention, clipRetention sql.NullInt32, monitoring sql.NullBool,
) StreamConfigRow {
	return StreamConfigRow{
		ID: id, InternalName: internalName, StreamKey: streamKey, PlaybackID: playbackID,
		Title: title, Description: description, IsRecordingEnabled: recording,
		CreatedAt: createdAt, UpdatedAt: updatedAt, IngestMode: ingestMode,
		SourceURIEnc: sourceURI, PullEnabled: pullEnabled, AllowedClusterIDs: allowed,
		ActiveIngestClusterID: activeIngest, DVRChapterMode: chapterMode,
		DVRChapterIntervalSeconds: chapterInterval, DVRRetentionDaysOverride: dvrRetention,
		ClipRetentionDaysOverride: clipRetention, MonitoringEnabled: monitoring,
	}
}

func (r GetStreamConfigRow) Config() StreamConfigRow {
	return streamConfigRow(r.ID, r.InternalName, r.StreamKey, r.PlaybackID, r.Title, r.Description,
		r.IsRecordingEnabled, r.CreatedAt, r.UpdatedAt, r.IngestMode, r.SourceUriEnc, r.Enabled,
		r.AllowedClusterIds, r.ActiveIngestClusterID, r.DvrChapterMode, r.DvrChapterIntervalSeconds,
		r.DvrRetentionDaysOverride, r.ClipRetentionDaysOverride, r.MonitoringEnabled)
}

func (r GetStreamsConfigBatchRow) Config() StreamConfigRow {
	return streamConfigRow(r.ID, r.InternalName, r.StreamKey, r.PlaybackID, r.Title, r.Description,
		r.IsRecordingEnabled, r.CreatedAt, r.UpdatedAt, r.IngestMode, r.SourceUriEnc, r.Enabled,
		r.AllowedClusterIds, r.ActiveIngestClusterID, r.DvrChapterMode, r.DvrChapterIntervalSeconds,
		r.DvrRetentionDaysOverride, r.ClipRetentionDaysOverride, r.MonitoringEnabled)
}

func (r ListStreamsForwardRow) Config() StreamConfigRow {
	return streamConfigRow(r.ID, r.InternalName, r.StreamKey, r.PlaybackID, r.Title, r.Description,
		r.IsRecordingEnabled, r.CreatedAt, r.UpdatedAt, r.IngestMode, r.SourceUriEnc, r.Enabled,
		r.AllowedClusterIds, r.ActiveIngestClusterID, r.DvrChapterMode, r.DvrChapterIntervalSeconds,
		r.DvrRetentionDaysOverride, r.ClipRetentionDaysOverride, r.MonitoringEnabled)
}

func (r ListStreamsForwardAfterRow) Config() StreamConfigRow {
	return streamConfigRow(r.ID, r.InternalName, r.StreamKey, r.PlaybackID, r.Title, r.Description,
		r.IsRecordingEnabled, r.CreatedAt, r.UpdatedAt, r.IngestMode, r.SourceUriEnc, r.Enabled,
		r.AllowedClusterIds, r.ActiveIngestClusterID, r.DvrChapterMode, r.DvrChapterIntervalSeconds,
		r.DvrRetentionDaysOverride, r.ClipRetentionDaysOverride, r.MonitoringEnabled)
}

func (r ListStreamsBackwardRow) Config() StreamConfigRow {
	return streamConfigRow(r.ID, r.InternalName, r.StreamKey, r.PlaybackID, r.Title, r.Description,
		r.IsRecordingEnabled, r.CreatedAt, r.UpdatedAt, r.IngestMode, r.SourceUriEnc, r.Enabled,
		r.AllowedClusterIds, r.ActiveIngestClusterID, r.DvrChapterMode, r.DvrChapterIntervalSeconds,
		r.DvrRetentionDaysOverride, r.ClipRetentionDaysOverride, r.MonitoringEnabled)
}

func (r ListStreamsBackwardBeforeRow) Config() StreamConfigRow {
	return streamConfigRow(r.ID, r.InternalName, r.StreamKey, r.PlaybackID, r.Title, r.Description,
		r.IsRecordingEnabled, r.CreatedAt, r.UpdatedAt, r.IngestMode, r.SourceUriEnc, r.Enabled,
		r.AllowedClusterIds, r.ActiveIngestClusterID, r.DvrChapterMode, r.DvrChapterIntervalSeconds,
		r.DvrRetentionDaysOverride, r.ClipRetentionDaysOverride, r.MonitoringEnabled)
}
