import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("Worker path resolution", () => {
  it("supports source and dist worker paths in EncoderManager", () => {
    const encoderManagerPath = path.resolve(__dirname, "../src/core/EncoderManager.ts");
    const source = fs.readFileSync(encoderManagerPath, "utf8");

    expect(source).toContain("../workers/encoder.worker.mod.js");
    expect(source).toContain("../workers/encoder.worker.ts");
    expect(source).toContain("../../workers/encoder.worker.js");
    expect(source).toContain(
      "/node_modules/@livepeer-frameworks/streamcrafter-wc/dist/workers/encoder.worker.js"
    );
  });

  it("supports source and dist worker paths in WhipClient transform worker", () => {
    const whipClientPath = path.resolve(__dirname, "../src/core/WhipClient.ts");
    const source = fs.readFileSync(whipClientPath, "utf8");

    expect(source).toContain("../workers/rtcTransform.worker.mod.js");
    expect(source).toContain("../workers/rtcTransform.worker.ts");
    expect(source).toContain("../../workers/rtcTransform.worker.js");
    expect(source).toContain(
      "/node_modules/@livepeer-frameworks/streamcrafter-wc/dist/workers/rtcTransform.worker.js"
    );
  });

  it("threads optional configured worker URLs through IngestControllerV2", () => {
    const ingestControllerPath = path.resolve(__dirname, "../src/core/IngestControllerV2.ts");
    const source = fs.readFileSync(ingestControllerPath, "utf8");

    expect(source).toContain("this.config.workers?.compositor");
    expect(source).toContain("this.config.workers?.encoder");
    expect(source).toContain("this.config.workers?.rtcTransform");
  });
});
