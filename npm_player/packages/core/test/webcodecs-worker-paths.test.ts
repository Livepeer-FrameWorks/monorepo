import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("WebCodecs worker path resolution", () => {
  it("keeps source worker resolution bundleable for source consumers", () => {
    const playerPath = path.resolve(__dirname, "../src/players/WebCodecsPlayer/index.ts");
    const source = fs.readFileSync(playerPath, "utf8");

    expect(source).toContain("./worker/decoder.worker.ts");
    expect(source).toContain("this.workerUrl");
    expect(source).toContain('new URL("./worker/decoder.worker.ts", import.meta.url)');
    expect(source).not.toContain("../../../workers/decoder.worker.js");
    expect(source).not.toContain("/workers/decoder.worker.js");
    expect(source).toContain('type: "debugging", value: false');
  });

  it("rewrites the source worker URL for published package builds", () => {
    const rollupConfigPath = path.resolve(__dirname, "../rollup.config.js");
    const rollupConfig = fs.readFileSync(rollupConfigPath, "utf8");

    expect(rollupConfig).toContain("webCodecsWorkerUrlPlugin");
    expect(rollupConfig).toContain("./worker/decoder.worker.ts");
    expect(rollupConfig).toContain("../../../workers/decoder.worker.js");
  });
});
