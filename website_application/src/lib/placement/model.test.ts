import { describe, expect, it } from "vitest";
import { copyRules, matchingPreset, presetRules, selectorLabel } from "./model";

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

describe("placement rule copies", () => {
  it("keeps node selectors so saving other edits does not drop them", () => {
    const rules = copyRules({
      schemaVersion: 1,
      constraints: {
        allow: { any: [{ clusterIds: ["owned"], nodeIds: ["edge-1"] }] },
        deny: [{ nodeIds: ["edge-2"] }],
      },
      preferences: null,
    })!;

    expect(rules.constraints.allow?.any[0].nodeIds).toEqual(["edge-1"]);
    expect(rules.constraints.deny?.[0].nodeIds).toEqual(["edge-2"]);
    expect(selectorLabel(rules.constraints.allow!.any[0])).toContain("1 selected node(s)");
  });
});
