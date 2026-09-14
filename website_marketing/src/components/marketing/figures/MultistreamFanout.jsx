import Figure from "./Figure";

/* One ingest fanning out to every push target from the origin node itself. */

const TARGETS = [
  { name: "Twitch" },
  { name: "YouTube" },
  { name: "Kick" },
  { name: "Facebook" },
  { name: "X" },
  { name: "custom RTMP / SRT", dim: true },
];

const ORIGIN = { x: 304, y: 112, w: 190, h: 72 };
const SOURCE = { x: 4, y: 118, w: 170, h: 60 };
const TARGET = { x: 590, w: 206, h: 40, stride: 54, y0: 8 };

const targetY = (index) => TARGET.y0 + index * TARGET.stride;

const MultistreamFanout = ({
  caption = "One ingest, every destination, pushed straight from the origin. Targets start and stop with the stream.",
  spot = false,
  className,
}) => {
  const originCy = ORIGIN.y + ORIGIN.h / 2;
  const originRight = ORIGIN.x + ORIGIN.w + 4;

  return (
    <Figure
      label="An encoder feeding a MistServer origin node, which pushes the stream to Twitch, YouTube, Kick, Facebook, X and a custom RTMP or SRT endpoint at the same time"
      caption={caption}
      viewBox="0 0 800 332"
      stackViewBox="0 0 400 560"
      spot={spot}
      className={className}
      stack={
        <>
          <g className="node you">
            <rect x="100" y="8" width="200" height="46" rx="12" />
            <text className="sub" x="200" y="31">
              your encoder
            </text>
          </g>
          <path className="edge" d="M200 58 L200 86" />
          <g className="node hot">
            <rect x="80" y="92" width="240" height="64" rx="16" />
            <text x="200" y="114">
              MistServer
            </text>
            <text className="sub" x="200" y="138">
              origin node
            </text>
          </g>
          <path className="edge" d="M200 160 L200 188" />
          {TARGETS.map((target, index) => (
            <g key={target.name} className={`node ${target.dim ? "dim" : ""}`}>
              <rect x="6" y={196 + index * 52} width="388" height="42" rx="12" />
              <text className="sub" x="200" y={217 + index * 52}>
                {target.name}
              </text>
            </g>
          ))}
          <text className="label" x="200" y="536">
            start and stop with the stream
          </text>
        </>
      }
    >
      <g className="node you">
        <rect x={SOURCE.x} y={SOURCE.y} width={SOURCE.w} height={SOURCE.h} rx="12" />
        <text className="sub" x={SOURCE.x + SOURCE.w / 2} y={SOURCE.y + SOURCE.h / 2}>
          your encoder
        </text>
      </g>
      <path
        className="edge"
        d={`M${SOURCE.x + SOURCE.w + 4} ${originCy} L${ORIGIN.x - 4} ${originCy}`}
      />

      <g className="node hot">
        <rect x={ORIGIN.x} y={ORIGIN.y} width={ORIGIN.w} height={ORIGIN.h} rx="16" />
        <text x={ORIGIN.x + ORIGIN.w / 2} y={ORIGIN.y + 26}>
          MistServer
        </text>
        <text className="sub" x={ORIGIN.x + ORIGIN.w / 2} y={ORIGIN.y + 50}>
          origin node
        </text>
      </g>

      {TARGETS.map((target, index) => {
        const y = targetY(index);
        const cy = y + TARGET.h / 2;
        return (
          <g key={target.name}>
            <path
              className={`edge ${target.dim ? "dim" : ""}`}
              d={`M${originRight} ${originCy} C 540 ${originCy}, 544 ${cy}, ${TARGET.x - 4} ${cy}`}
            />
            <g className={`node ${target.dim ? "dim" : ""}`}>
              <rect x={TARGET.x} y={y} width={TARGET.w} height={TARGET.h} rx="12" />
              <text className="sub" x={TARGET.x + TARGET.w / 2} y={cy}>
                {target.name}
              </text>
            </g>
          </g>
        );
      })}

      <text className="label left" x={SOURCE.x} y={SOURCE.y + SOURCE.h + 28}>
        no relay in between
      </text>
      <text className="label" x={ORIGIN.x + ORIGIN.w / 2} y={ORIGIN.y + ORIGIN.h + 24}>
        pushed from here
      </text>
    </Figure>
  );
};

export default MultistreamFanout;
