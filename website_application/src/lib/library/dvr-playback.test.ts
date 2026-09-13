import { describe, expect, it } from "vitest";
import { canPlayRollingDvr } from "./dvr-playback";

describe("rolling DVR playback", () => {
  it.each(["requested", "starting", "started", "recording"])(
    "plays %s only with media",
    (status) => {
      expect(canPlayRollingDvr(status, true)).toBe(true);
      expect(canPlayRollingDvr(status, false)).toBe(false);
    }
  );

  it.each(["stopping", "finalizing", "completed", "ready", "synced", "failed", "unknown"])(
    "does not mistake %s parent metadata for a playable archive",
    (status) => expect(canPlayRollingDvr(status, true)).toBe(false)
  );
});
