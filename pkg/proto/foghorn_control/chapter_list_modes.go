package foghorncontrolpb

// ArchivedClipCoverageMode selects materialized chapter metadata for a
// service-authenticated, tenant-scoped historical clip lookup.
const ArchivedClipCoverageMode = "archive_clip_coverage"

// ArchivedClipCoverageTrailer distinguishes a materialized lookup from the
// empty virtual listing an older server can return for an unknown mode.
const ArchivedClipCoverageTrailer = "fw-archive-clip-coverage"

// ArchivedClipCoverageVersion identifies batch results with a DVR parent for
// each chapter. Older Foghorn responses cannot prove coverage for a batch.
const ArchivedClipCoverageVersion = "2"

// ArchivedClipCoverageBatchSize bounds the DVR IDs in an internal lookup.
const ArchivedClipCoverageBatchSize = 128
