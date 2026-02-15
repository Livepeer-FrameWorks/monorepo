import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("SceneManager worker path resolution", () => {
  it("supports both source and published dist worker-relative paths", () => {
    const sceneManagerPath = path.resolve(__dirname, "../src/core/SceneManager.ts");
    const source = fs.readFileSync(sceneManagerPath, "utf8");

    expect(source).toContain("../workers/compositor.worker.ts");
    expect(source).toContain("../../workers/compositor.worker.js");
    expect(source).toContain(
      "/node_modules/@livepeer-frameworks/streamcrafter-wc/dist/workers/compositor.worker.js"
    );
    expect(source).toContain("this.workerUrl");
  });
});
