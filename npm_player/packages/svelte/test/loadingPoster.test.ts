import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("Svelte LoadingPoster", () => {
  it("keeps the spritesheet preroll one-shot and then swaps to the static poster", () => {
    const loadingPosterPath = path.resolve(__dirname, "../src/LoadingPoster.svelte");
    const source = fs.readFileSync(loadingPosterPath, "utf8");

    expect(source).toContain("const animationStartTimes = new Map<string, number>()");
    expect(source).toContain("const completedAnimations = new Set<string>()");
    expect(source).toContain("function animationKeyFor");
    expect(source).toContain("p.prerollKey ?? p.staticUrl ?? p.spriteJpgUrl");
    expect(source).toContain("let animationCompleted = $state(false)");
    expect(source).toContain("animationCompleted = true");
    expect(source).toContain("!(animationCompleted && staticSrc)");
    expect(source).toContain("function cueIndexFor");
    expect(source).not.toContain("tickIdx % Math.max");
  });

  it("crops with a small inset to avoid bleeding neighboring sprite tiles", () => {
    const loadingPosterPath = path.resolve(__dirname, "../src/LoadingPoster.svelte");
    const source = fs.readFileSync(loadingPosterPath, "utf8");

    expect(source).toContain("const CROP_INSET_PX = 0.5");
    expect(source).toContain("viewBox={`0 0 ${cueRect.viewW} ${cueRect.viewH}`}");
    expect(source).toContain("clipPath id={clipId}");
    expect(source).toContain("x={-cueRect.x}");
    expect(source).toContain("y={-cueRect.y}");
  });
});
