import Figure from "./Figure";

/* Who runs which part of the stack in each operating mode. Colour is the
   message: hot = FrameWorks runs it, you = you run it, dim = an external
   integration you own. Hybrid today means you run edge nodes and FrameWorks
   runs the control plane plus hosted burst capacity; self-hosted keeps
   S3-compatible storage and DNS as external integrations. */

const COLUMNS = ["Edge nodes", "Control plane", "Processing", "Storage"];

const ROWS = [
  {
    name: "Hosted",
    cells: [
      { tone: "hot", lines: ["Edge nodes"] },
      { tone: "hot", lines: ["Control plane"] },
      { tone: "hot", lines: ["Processing"] },
      { tone: "hot", lines: ["Storage"] },
    ],
  },
  {
    name: "Hybrid",
    cells: [
      { tone: "you", lines: ["your edges"], split: { tone: "hot", lines: ["hosted burst"] } },
      { tone: "hot", lines: ["Control plane"] },
      { tone: "hot", lines: ["Processing"] },
      { tone: "hot", lines: ["Storage"] },
    ],
  },
  {
    name: "Self-hosted",
    cells: [
      { tone: "you", lines: ["Edge nodes"] },
      { tone: "you", lines: ["Control plane"] },
      { tone: "you", lines: ["Processing"] },
      { tone: "dim", lines: ["your S3"] },
    ],
  },
];

/* Horizontal offsets leave room for each label at the 21px label size. */
const LEGEND = [
  { tone: "hot", text: "FrameWorks runs it", x: 0 },
  { tone: "you", text: "you run it", x: 262 },
  { tone: "dim", text: "external, yours", x: 436 },
];

const Node = ({ x, y, w, h, tone, lines }) => (
  <g className={`node ${tone}`}>
    <rect x={x} y={y} width={w} height={h} rx="12" />
    {lines.length === 1 ? (
      <text className="sub" x={x + w / 2} y={y + h / 2}>
        {lines[0]}
      </text>
    ) : (
      <>
        <text className="sub" x={x + w / 2} y={y + h / 2 - 11}>
          {lines[0]}
        </text>
        <text className="sub" x={x + w / 2} y={y + h / 2 + 12}>
          {lines[1]}
        </text>
      </>
    )}
  </g>
);

const Legend = ({ x, y, horizontal }) =>
  LEGEND.map((item, index) => {
    const ix = horizontal ? x + item.x : x;
    const iy = horizontal ? y : y + index * 30;
    return (
      <g key={item.tone}>
        <g className={`node ${item.tone}`}>
          <rect x={ix} y={iy} width="18" height="18" rx="4" />
        </g>
        <text className="label left" x={ix + 28} y={iy + 9}>
          {item.text}
        </text>
      </g>
    );
  });

/* Wide layout: one row per mode, one column per stack part. */
const WIDE = { x0: 170, stride: 158, boxW: 140, rowY: [14, 102, 234], rowH: [60, 104, 60] };

const wideCells = () =>
  ROWS.flatMap((row, r) => {
    const y = WIDE.rowY[r];
    const h = WIDE.rowH[r];
    return row.cells.map((cell, c) => {
      const x = WIDE.x0 + c * WIDE.stride;
      if (cell.split) {
        const half = (h - 12) / 2;
        return (
          <g key={`${row.name}-${c}`}>
            <Node x={x} y={y} w={WIDE.boxW} h={half} tone={cell.tone} lines={cell.lines} />
            <Node
              x={x}
              y={y + half + 12}
              w={WIDE.boxW}
              h={half}
              tone={cell.split.tone}
              lines={cell.split.lines}
            />
          </g>
        );
      }
      return (
        <Node
          key={`${row.name}-${c}`}
          x={x}
          y={y}
          w={WIDE.boxW}
          h={h}
          tone={cell.tone}
          lines={cell.lines}
        />
      );
    });
  });

/* Stacked layout: one group per mode, cells in a two-column grid. */
const STACK = { x0: 6, colW: 186, colGap: 16, boxH: 44, rowStride: 54, groupGap: 22 };

const stackGroups = () => {
  let y = 16;
  return ROWS.map((row) => {
    const cells = row.cells.flatMap((cell) =>
      cell.split ? [cell, { tone: cell.split.tone, lines: cell.split.lines }] : [cell]
    );
    const groupTop = y;
    const nodes = cells.map((cell, i) => {
      const col = i % 2;
      const line = Math.floor(i / 2);
      return (
        <Node
          key={`${row.name}-${i}`}
          x={STACK.x0 + col * (STACK.colW + STACK.colGap)}
          y={groupTop + 24 + line * STACK.rowStride}
          w={STACK.colW}
          h={STACK.boxH}
          tone={cell.tone}
          lines={cell.lines}
        />
      );
    });
    const lines = Math.ceil(cells.length / 2);
    y = groupTop + 24 + lines * STACK.rowStride + STACK.groupGap;
    return (
      <g key={row.name}>
        <text className="left" x={STACK.x0} y={groupTop}>
          {row.name}
        </text>
        {nodes}
      </g>
    );
  });
};

const DeploymentModes = ({
  caption = "Same control plane and dashboards in every row. Move between rows without rebuilding your setup.",
  spot = false,
  className,
}) => (
  <Figure
    label={`Three operating modes, hosted, hybrid and self-hosted, showing which of ${COLUMNS.join(", ")} FrameWorks runs and which you run`}
    caption={caption}
    viewBox="0 0 800 372"
    stackViewBox="0 0 400 640"
    spot={spot}
    className={className}
    stack={
      <>
        {stackGroups()}
        <Legend x={6} y={548} horizontal={false} />
      </>
    }
  >
    {ROWS.map((row, r) => (
      <text key={row.name} className="left" x="0" y={WIDE.rowY[r] + WIDE.rowH[r] / 2}>
        {row.name}
      </text>
    ))}
    {wideCells()}
    <Legend x={170} y={340} horizontal />
  </Figure>
);

export default DeploymentModes;
