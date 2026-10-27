import type { Cohort } from "./network-data";

type Range = { start: string; end: string; interval_seconds: number };
export type CheckBucket = {
  start: string;
  end: string;
  samples: number;
  successes: number;
  failures: number;
};
const ns = "http://www.w3.org/2000/svg";
function element<K extends keyof HTMLElementTagNameMap>(tag: K, className: string, text?: string) {
  const result = document.createElement(tag);
  result.className = className;
  if (text !== undefined) result.textContent = text;
  return result;
}
function vector(tag: string, attributes: Record<string, string | number>, text?: string) {
  const result = document.createElementNS(ns, tag);
  for (const [key, value] of Object.entries(attributes)) result.setAttribute(key, String(value));
  if (text !== undefined) result.textContent = text;
  return result;
}
function count(value: number, noun: string) {
  return `${value.toLocaleString()} ${noun}${value === 1 ? "" : "s"}`;
}
function clock(value: number) {
  return new Date(value).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}
function date(value: number) {
  return new Date(value).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}
function stamp(value: number) {
  return `${date(value)}, ${clock(value)}`;
}
function niceScale(value: number) {
  const magnitude = 10 ** Math.floor(Math.log10(Math.max(1, value) / 4));
  const ratio = Math.max(1, value) / (4 * magnitude);
  const step =
    (ratio <= 1 ? 1 : ratio <= 2 ? 2 : ratio <= 2.5 ? 2.5 : ratio <= 5 ? 5 : 10) * magnitude;
  const steps = Math.ceil(Math.max(1, value) / step);
  return { limit: steps * step, steps };
}

export function renderLatencyChart(target: HTMLElement, cohorts: Cohort[], range: Range) {
  const start = Date.parse(range.start),
    end = Date.parse(range.end);
  const points = cohorts.filter(
    (c) =>
      Number.isFinite(c.p95_ms) &&
      c.p95_ms >= 0 &&
      Date.parse(c.bucket) >= start &&
      Date.parse(c.bucket) <= end &&
      c.qualified_samples > 0
  );
  if (!points.length || !Number.isFinite(start) || !Number.isFinite(end) || end <= start) {
    target.append(element("p", "evidence-note", "No response-time data for this selection."));
    return;
  }
  const figure = element("figure", "latency-figure");
  const svg = vector("svg", {
    viewBox: "0 0 340 212",
    class: "observation-chart",
    role: "group",
    tabindex: 0,
    "aria-label": "p95 response time chart. Use left and right arrows to inspect samples.",
  });
  svg.append(
    vector("title", {}, "p95 response time"),
    vector(
      "desc",
      {},
      "Response time in milliseconds over local time. Gaps mean no data. Use arrow keys to inspect samples."
    )
  );
  const left = 48,
    right = 322,
    top = 28,
    bottom = 154;
  const { limit, steps } = niceScale(Math.max(1, ...points.map((c) => c.p95_ms)));
  const x = (c: Cohort) => left + ((right - left) * (Date.parse(c.bucket) - start)) / (end - start);
  const y = (c: Cohort) => bottom - ((bottom - top) * c.p95_ms) / limit;
  const yAxis = vector("g", { "aria-label": "Response time (ms)" });
  yAxis.append(
    vector("text", { x: left, y: 15, class: "chart-axis-title" }, "Response time · p95 (ms)")
  );
  for (let i = 0; i <= steps; i++) {
    const value = (limit * i) / steps,
      position = bottom - ((bottom - top) * i) / steps;
    yAxis.append(
      vector("line", { x1: left, y1: position, x2: right, y2: position, class: "chart-grid" }),
      vector(
        "text",
        { x: left - 7, y: position + 4, "text-anchor": "end", class: "chart-y-tick" },
        value.toLocaleString(undefined, {
          maximumFractionDigits: 2,
          ...(value >= 10000 ? { notation: "compact" as const } : {}),
        })
      )
    );
  }
  const xAxis = vector("g", { "aria-label": "Time (local)" });
  xAxis.append(
    vector("path", { d: `M${left} ${top}V${bottom}H${right}`, class: "chart-axis-line" })
  );
  for (let i = 0; i <= 2; i++) {
    const value = start + ((end - start) * i) / 2,
      position = left + ((right - left) * i) / 2;
    const label = vector("text", {
      x: position,
      y: 173,
      class: "chart-x-tick",
      "text-anchor": i === 0 ? "start" : i === 2 ? "end" : "middle",
    });
    label.append(
      vector("tspan", { x: position }, clock(value)),
      vector("tspan", { x: position, dy: 15 }, date(value))
    );
    xAxis.append(
      vector("line", {
        x1: position,
        y1: bottom,
        x2: position,
        y2: bottom + 5,
        class: "chart-axis-line",
      }),
      label
    );
  }
  xAxis.append(
    vector(
      "text",
      { x: (left + right) / 2, y: 205, "text-anchor": "middle", class: "chart-axis-title" },
      "Time (local)"
    )
  );
  svg.append(yAxis, xAxis);
  const groups = new Map<string, Cohort[]>();
  for (const point of points) {
    const key = `${point.region || "Unknown region"} · ${point.kind === "discovery" ? "Discovery" : point.profile_key || "Unspecified profile"}`;
    groups.set(key, [...(groups.get(key) ?? []), point]);
  }
  const colors = [
    "#7aa2f7",
    "#9ece6a",
    "#e0af68",
    "#7dcfff",
    "#bb9af7",
    "#f7768e",
    "#ff9e64",
    "#c0caf5",
  ];
  const legend = element("div", "chart-key");
  const readout = element("div", "chart-readout");
  readout.setAttribute("role", "status");
  const guide = vector("line", {
    x1: left,
    x2: left,
    y1: top,
    y2: bottom,
    class: "chart-crosshair",
    visibility: "hidden",
  });
  svg.append(guide);
  const selections: Array<{ point: Cohort; label: string; color: string; dot: Element }> = [];
  let selected = -1;
  const inspect = (index: number) => {
    selected = (index + selections.length) % selections.length;
    for (const item of selections) item.dot.classList.remove("is-selected");
    const item = selections[selected],
      point = item.point;
    item.dot.classList.add("is-selected");
    guide.setAttribute("x1", String(x(point)));
    guide.setAttribute("x2", String(x(point)));
    guide.setAttribute("visibility", "visible");
    readout.replaceChildren(
      element(
        "strong",
        "",
        `${Math.round(point.p95_ms).toLocaleString()} ms · ${stamp(Date.parse(point.bucket))}`
      ),
      element(
        "span",
        "",
        `${item.label} · ${count(point.samples, "sample")} · ${point.failures.toLocaleString()} failed`
      )
    );
  };
  let seriesIndex = 0;
  for (const [label, values] of groups) {
    const color = colors[seriesIndex++ % colors.length];
    let previous: Cohort | undefined;
    for (const point of values.sort((a, b) => Date.parse(a.bucket) - Date.parse(b.bucket))) {
      if (
        previous &&
        Date.parse(point.bucket) - Date.parse(previous.bucket) <= range.interval_seconds * 1500
      )
        svg.append(
          vector("line", {
            x1: x(previous),
            y1: y(previous),
            x2: x(point),
            y2: y(point),
            stroke: color,
            class: "chart-series-line",
          })
        );
      const dot = vector("circle", {
        cx: x(point),
        cy: y(point),
        r: 3.5,
        fill: color,
        class: "chart-point",
      });
      dot.append(
        vector(
          "title",
          {},
          `${label}: ${Math.round(point.p95_ms)} ms · ${count(point.samples, "sample")} · ${stamp(Date.parse(point.bucket))}`
        )
      );
      const index = selections.length;
      selections.push({ point, label, color, dot });
      dot.addEventListener("mouseenter", () => inspect(index));
      dot.addEventListener("pointerdown", () => inspect(index));
      svg.append(dot);
      previous = point;
    }
    const key = element("span", "", label);
    key.style.color = color;
    legend.append(key);
  }
  svg.addEventListener("keydown", (event) => {
    const key = (event as KeyboardEvent).key;
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(key)) return;
    event.preventDefault();
    inspect(
      key === "Home"
        ? 0
        : key === "End"
          ? selections.length - 1
          : selected + (key === "ArrowRight" ? 1 : -1)
    );
  });
  const inspectAtPointer = (event: PointerEvent) => {
    const bounds = svg.getBoundingClientRect();
    const px = ((event.clientX - bounds.left) * 340) / bounds.width;
    const py = ((event.clientY - bounds.top) * 212) / bounds.height;
    if (px < left || px > right || py < top || py > bottom) return;
    let nearest = 0,
      distance = Infinity;
    selections.forEach((item, i) => {
      const next = (x(item.point) - px) ** 2 + (y(item.point) - py) ** 2;
      if (next < distance) {
        distance = next;
        nearest = i;
      }
    });
    inspect(nearest);
  };
  svg.addEventListener("pointermove", (event) => inspectAtPointer(event as PointerEvent));
  svg.addEventListener("pointerdown", (event) => inspectAtPointer(event as PointerEvent));
  svg.addEventListener("focus", () => {
    if (selected < 0) inspect(selections.length - 1);
  });
  inspect(selections.length - 1);
  figure.append(svg, legend, readout);
  target.append(figure);
}

export function renderCheckTimeline(target: HTMLElement, buckets: CheckBucket[]) {
  if (!buckets.length) return;
  const figure = element("figure", "check-figure");
  const timeline = element("div", "timeline");
  timeline.tabIndex = 0;
  timeline.setAttribute("role", "group");
  timeline.setAttribute(
    "aria-label",
    "Discovery checks over time. Use left and right arrows to inspect checks."
  );
  const readout = element("div", "chart-readout");
  readout.setAttribute("role", "status");
  const cells: HTMLElement[] = [];
  let selected = buckets.length - 1;
  const inspect = (index: number) => {
    selected = (index + buckets.length) % buckets.length;
    cells.forEach((cell, i) => cell.classList.toggle("is-selected", i === selected));
    const b = buckets[selected];
    readout.replaceChildren(
      element("strong", "", `${stamp(Date.parse(b.start))} – ${clock(Date.parse(b.end))}`),
      element(
        "span",
        "",
        b.samples
          ? `${b.successes} passed · ${b.failures} failed · ${count(b.samples, "check")}`
          : "No checks"
      )
    );
  };
  buckets.forEach((bucket, i) => {
    const state =
      bucket.successes > 0
        ? bucket.failures > 0
          ? "mixed"
          : "up"
        : bucket.failures > 0
          ? "unavailable"
          : "unknown";
    const cell = element("span", state);
    cell.title = `${stamp(Date.parse(bucket.start))}: ${bucket.successes} passed · ${bucket.failures} failed · ${count(bucket.samples, "check")}`;
    cell.addEventListener("mouseenter", () => inspect(i));
    cell.addEventListener("pointerdown", () => inspect(i));
    cells.push(cell);
    timeline.append(cell);
  });
  const start = Date.parse(buckets[0].start),
    end = Date.parse(buckets[buckets.length - 1].end);
  const axis = element("div", "timeline-axis");
  for (const value of [start, (start + end) / 2, end])
    axis.append(element("span", "", stamp(value)));
  const legend = element("div", "chart-key timeline-key");
  for (const [state, label] of [
    ["up", "Passed"],
    ["mixed", "Mixed"],
    ["unavailable", "Failed"],
    ["unknown", "No data"],
  ])
    legend.append(element("span", `check-${state}`, label));
  timeline.addEventListener("keydown", (event) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    inspect(
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? buckets.length - 1
          : selected + (event.key === "ArrowRight" ? 1 : -1)
    );
  });
  inspect(selected);
  figure.append(timeline, axis, legend, readout);
  target.append(figure);
}
