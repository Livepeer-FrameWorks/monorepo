package foghorncontrolpb

// ArchivedClipCoverageMode selects materialized chapter metadata for a
// service-authenticated, tenant-scoped historical clip lookup.
const ArchivedClipCoverageMode = "archive_clip_coverage"

// ArchivedClipCoverageTrailer distinguishes a materialized lookup from the
// empty virtual listing an older server can return for an unknown mode.
const ArchivedClipCoverageTrailer = "fw-archive-clip-coverage"
