import { describe, it, expect } from "vitest";
import { recordingNotice } from "./recording-notice";
const failed = {
  type: "RECORDING_REFUSED",
  timestamp: "2026-11-26T10:00:00Z",
  details: "storage limit reached",
};
describe("persistent recording notice", () => {
  it("preserves the last failure after the broadcast ends", () =>
    expect(
      recordingNotice(
        [{ type: "STREAM_END", timestamp: "2026-11-26T10:01:00Z" }, failed],
        true,
        false
      )?.state
    ).toBe("blocked"));
  it("does not carry an old failure into a new broadcast", () =>
    expect(recordingNotice([failed], true, true, "2026-11-26T11:00:00Z")?.state).toBe("pending"));
  it("clears on actual capture, not requested recording", () =>
    expect(
      recordingNotice(
        [{ type: "RECORDING_ACTIVE", timestamp: "2026-11-26T10:00:30Z" }, failed],
        true,
        true
      )?.state
    ).toBe("recording"));
  it("does not claim capture while status is unavailable", () =>
    expect(
      recordingNotice(
        [{ type: "RECORDING_ACTIVE", timestamp: "2026-11-26T10:00:30Z" }],
        true,
        true,
        null,
        true
      )?.state
    ).toBe("unknown"));
  it("honors disabled recording", () => expect(recordingNotice([failed], false, true)).toBeNull());
});
