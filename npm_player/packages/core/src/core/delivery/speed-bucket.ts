export type SpeedBucket = "low" | "normal" | "high";

export interface SpeedBucketInput {
  bucket: SpeedBucket;
  currentMs: number;
  desiredMs: number;
  speedDownThreshold: number;
  speedUpThreshold: number;
}

export function nextSpeedBucket(input: SpeedBucketInput): SpeedBucket {
  const lowThreshold = input.desiredMs * input.speedDownThreshold;
  const highThreshold = input.desiredMs * input.speedUpThreshold;

  if (input.bucket === "normal") {
    if (input.currentMs < lowThreshold) return "low";
    if (input.currentMs > highThreshold) return "high";
    return "normal";
  }

  if (input.bucket === "low") {
    return input.currentMs >= input.desiredMs ? "normal" : "low";
  }

  return input.currentMs <= input.desiredMs ? "normal" : "high";
}
