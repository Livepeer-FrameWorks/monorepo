package artifacts

// ThumbnailAssetLockNamespace is the shared transaction-lock namespace for
// every operation that can publish, purge, or project authority for an
// artifact's thumbnail state. All participants must use the same namespace so
// lifecycle absence checks and writes serialize across service replicas.
const ThumbnailAssetLockNamespace int32 = 0x746d626c // "tmbl"
