import { describe, expect, it } from "vitest";
import { PROCESSING_WATCH_LIMIT_MS, processingWatchVerdict } from "./processing-watch";

const started = 1_000_000;

describe("upload processing watch", () => {
  it("keeps polling while the upload is processing inside the limit", () => {
    const verdict = processingWatchVerdict(
      { __typename: "VodUploadStatus", state: "PROCESSING" },
      started,
      started + 60_000
    );
    expect(verdict).toEqual({ kind: "continue" });
  });

  it("keeps polling through a transient fetch failure inside the limit", () => {
    expect(processingWatchVerdict(null, started, started + 5_000)).toEqual({ kind: "continue" });
  });

  it("finishes on READY", () => {
    expect(
      processingWatchVerdict({ __typename: "VodUploadStatus", state: "READY" }, started, started)
    ).toEqual({ kind: "ready" });
  });

  it.each(["FAILED", "DELETED", "EXPIRED"])("stops as failed on %s", (state) => {
    const verdict = processingWatchVerdict(
      { __typename: "VodUploadStatus", state },
      started,
      started
    );
    expect(verdict.kind).toBe("failed");
  });

  it.each([
    ["NotFoundError", /no longer exists/],
    ["AuthError", /sign in/i],
    ["ValidationError", /rejected/],
  ])("stops on %s with its own message", (typename, message) => {
    const verdict = processingWatchVerdict(
      { __typename: typename, message: "detail" },
      started,
      started
    );
    expect(verdict.kind).toBe("stopped");
    if (verdict.kind === "stopped") expect(verdict.message).toMatch(message);
  });

  it("stops once processing outlasts the limit", () => {
    const verdict = processingWatchVerdict(
      { __typename: "VodUploadStatus", state: "PROCESSING" },
      started,
      started + PROCESSING_WATCH_LIMIT_MS
    );
    expect(verdict.kind).toBe("stopped");
    if (verdict.kind === "stopped") expect(verdict.message).toMatch(/30 minutes/);
  });

  it("stops after the limit even while every fetch fails", () => {
    const verdict = processingWatchVerdict(null, started, started + PROCESSING_WATCH_LIMIT_MS + 1);
    expect(verdict.kind).toBe("stopped");
  });
});
