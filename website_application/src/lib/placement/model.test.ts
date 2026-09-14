import { describe, expect, it } from "vitest";
import { copyRules, matchingPreset, presetRules } from "./model";

const presets = ["closest_available", "my_clusters_first", "my_clusters_only", "no_official"];

describe("placement presets", () => {
  it.each(presets)("recognizes the %s preset", (preset) => {
    expect(matchingPreset(presetRules(preset), presets)).toBe(preset);
  });

  it("does not label an edited preset as a simple choice", () => {
    const custom = copyRules(presetRules("my_clusters_first"))!;
    custom.preferences!.groups[0].maxDistanceKm = 750;

    expect(matchingPreset(custom, presets)).toBeNull();
  });

  it("does not treat inherited rules as a preset", () => {
    expect(matchingPreset(null, presets)).toBeNull();
  });
});
