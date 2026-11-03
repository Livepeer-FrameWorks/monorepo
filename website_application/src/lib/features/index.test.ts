import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { findFeature, horizonLabel, roadmapHref } from "./index";

describe("feature status details", () => {
  it("exposes the beta flag, note and roadmap section the registry declares", () => {
    const studio = findFeature("streamcrafter");
    expect(studio?.maturity).toBe("beta");
    expect(studio?.note).toMatch(/breaking changes/);
    expect(roadmapHref(studio!, "https://docs.example/")).toBe(
      "https://docs.example/roadmap#streamcrafter-browser-studio"
    );
    expect(horizonLabel(studio?.horizon)).toBe("Shipping today");
  });

  it("has no roadmap link for a feature without a roadmap section", () => {
    expect(roadmapHref({}, "https://docs.example")).toBeUndefined();
    expect(horizonLabel(undefined)).toBeUndefined();
  });

  it("names each horizon as the generated marketing status does", () => {
    const status = JSON.parse(
      readFileSync(
        resolve(import.meta.dirname, "../../../../website_marketing/src/data/status.json"),
        "utf8"
      )
    ) as { tiers: { horizon: "now" | "next" | "later"; label: string }[] };
    for (const tier of status.tiers) {
      expect(horizonLabel(tier.horizon)).toBe(tier.label);
    }
  });
});
