// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { renderLatencyChart, renderCheckTimeline } from "../../packages/map-core/network-charts";
import type { Cohort } from "../../packages/map-core/network-data";
const start = "2026-10-26T08:00:00Z";
const end = "2026-10-26T12:00:00Z";
const sample = (bucket: string, p95_ms: number): Cohort => ({
  bucket,
  p95_ms,
  p50_ms: 50,
  p99_ms: 200,
  kind: "attempt",
  resource_id: "one",
  ip: "192.0.2.1",
  region: "eu-west",
  profile_key: "720p",
  samples: 20,
  qualified_samples: 20,
  successes: 18,
  failures: 2,
  duration_samples: 20,
  p95_duration_ratio: 0.1,
  retries: 1,
  fallbacks: 0,
  deadline_misses: 1,
});
describe("public network charts", () => {
  it("labels both axes, uses a zero baseline and shows the selected sample", () => {
    const host = document.createElement("div");
    renderLatencyChart(
      host,
      [sample("2026-10-26T09:00:00Z", 150), sample("2026-10-26T10:00:00Z", 240)],
      { start, end, interval_seconds: 3600 }
    );
    expect(host.querySelector('[aria-label="Response time (ms)"]')).not.toBeNull();
    expect(host.querySelector('[aria-label="Time (local)"]')).not.toBeNull();
    expect(host.querySelectorAll(".chart-y-tick").length).toBeGreaterThanOrEqual(3);
    expect(host.querySelector(".chart-y-tick")?.textContent).toBe("0");
    expect(host.querySelectorAll(".chart-x-tick")).toHaveLength(3);
    const chart = host.querySelector("svg")!;
    chart.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    expect(host.querySelector(".chart-readout")?.textContent).toContain("150 ms");
    expect(host.querySelector(".chart-readout")?.textContent).toContain("20 samples");
    expect(host.querySelector(".chart-readout")?.textContent).toContain("eu-west");
  });
  it("keeps real discovery timings visible without wasting most of the value axis", () => {
    const host = document.createElement("div");
    renderLatencyChart(host, [sample("2026-10-26T09:00:00Z", 620)], {
      start,
      end,
      interval_seconds: 300,
    });
    const ticks = [...host.querySelectorAll(".chart-y-tick")];
    const ceiling = Number(ticks.at(-1)!.textContent!.replaceAll(",", ""));
    expect(ceiling).toBeGreaterThanOrEqual(620);
    expect(ceiling).toBeLessThanOrEqual(1000);
  });
  it("formats a real single-sample bucket without a plural label", () => {
    const host = document.createElement("div");
    renderLatencyChart(host, [{ ...sample("2026-10-26T09:00:00Z", 150), samples: 1 }], {
      start,
      end,
      interval_seconds: 300,
    });
    expect(host.querySelector(".chart-readout")?.textContent).toContain("1 sample ·");
    expect(host.querySelector(".chart-readout")?.textContent).not.toContain("1 samples");
  });
  it("leaves gaps in history and rejects invalid chart points", () => {
    const host = document.createElement("div");
    renderLatencyChart(host, [sample(start, 0), sample(end, 200), sample(end, NaN)], {
      start,
      end,
      interval_seconds: 300,
    });
    expect(host.querySelectorAll(".chart-series-line")).toHaveLength(0);
    expect(host.querySelectorAll(".chart-point")).toHaveLength(2);
    expect(host.innerHTML).not.toContain("NaN");
  });
  it("distinguishes mixed checks from passed checks and labels timeline times", () => {
    const host = document.createElement("div");
    renderCheckTimeline(host, [
      { start, end, samples: 10, successes: 9, failures: 1 },
      { start: end, end, samples: 0, successes: 0, failures: 0 },
    ]);
    expect(host.querySelector(".timeline .mixed")).not.toBeNull();
    expect(host.querySelector(".timeline .unknown")).not.toBeNull();
    expect(host.querySelectorAll(".timeline-axis span")).toHaveLength(3);
    const timeline = host.querySelector(".timeline")!;
    timeline.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    expect(host.querySelector(".chart-readout")?.textContent).toContain("9 passed");
    expect(host.querySelector(".chart-readout")?.textContent).toContain("1 failed");
  });
});
