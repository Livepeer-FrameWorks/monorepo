-- Durable multipart completion descriptor for stranded-'completing' VOD recovery.
--
-- foghorn.vod_metadata.vod_completion_descriptor: the full S3 CompleteMultipartUpload input
-- (s3 key, upload id, ordered [{part_number, etag}]), persisted ATOMICALLY with the
-- 'uploading'->'completing' claim in CompleteVodUpload. The completing-VOD recovery job reads it to
-- RETRY CompleteMultipartUpload idempotently, so a crash BEFORE the client's completion call converges
-- a still-valid multipart upload to the finished object instead of marking it failed. A 'completing'
-- row is never left without this descriptor persisted.
ALTER TABLE foghorn.vod_metadata
  ADD COLUMN IF NOT EXISTS vod_completion_descriptor JSONB;
