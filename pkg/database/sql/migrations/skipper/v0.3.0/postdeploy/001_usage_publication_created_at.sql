UPDATE skipper.skipper_usage
SET created_at = NOW()
WHERE created_at IS NULL;
