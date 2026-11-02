import Figure from "./Figure";

/* A viewer routed to a healthy edge, with the routing decision and the
   player's playback report both landing in analytics. FrameWorks edges are
   hot, a customer's edge is you, and an unhealthy edge is bad and skipped. */

const VIEWER = { x: 4, y: 120, w: 150, h: 64 };
const ROUTER = { x: 226, y: 104, w: 240, h: 96 };
const EDGES = [
  { tone: "hot", name: "FrameWorks edge", y: 40, edge: "" },
  { tone: "you", name: "your edge", y: 130, edge: "" },
  { tone: "bad", name: "unhealthy edge", y: 220, edge: "bad" },
];
const EDGE_BOX = { x: 560, w: 236, h: 50 };
const ANALYTICS = { x: 226, y: 252, w: 240, h: 56 };

const RoutingEvidence = ({
  caption = "Each viewer is sent to a healthy edge by location and load. The routing decision and the player's playback report land in the same analytics.",
  spot = false,
  className,
}) => {
  const viewerCy = VIEWER.y + VIEWER.h / 2;
  const routerCy = ROUTER.y + ROUTER.h / 2;
  const routerCx = ROUTER.x + ROUTER.w / 2;

  return (
    <Figure
      label="A viewer reaches the FrameWorks routing decision, which weighs location, load and health and sends the viewer to a FrameWorks edge or your own edge while skipping an unhealthy one; the decision and the viewer's startup and rebuffering report go to analytics"
      caption={caption}
      viewBox="0 0 800 324"
      stackViewBox="0 0 400 494"
      spot={spot}
      className={className}
      stack={
        <>
          <g className="node">
            <rect x="120" y="8" width="160" height="52" rx="12" />
            <text className="sub" x="200" y="34">
              viewer
            </text>
          </g>
          <path className="edge" d="M200 64 L200 94" />
          <g className="node hot">
            <rect x="60" y="100" width="280" height="88" rx="16" />
            <text x="200" y="130">
              routing
            </text>
            <text className="sub" x="200" y="160">
              location, load, health
            </text>
          </g>
          <path className="edge" d="M200 192 L200 220" />
          {EDGES.map((edge, index) => (
            <g key={edge.name} className={`node ${edge.tone}`}>
              <rect x="50" y={228 + index * 56} width="300" height="46" rx="12" />
              <text className="sub" x="200" y={251 + index * 56}>
                {edge.name}
              </text>
            </g>
          ))}
          <path className="edge dim" d="M200 398 L200 424" />
          <text className="label left" x="214" y="411">
            startup, rebuffering, route
          </text>
          <g className="node hot">
            <rect x="60" y="430" width="280" height="56" rx="12" />
            <text x="200" y="458">
              analytics
            </text>
          </g>
        </>
      }
    >
      <g className="node">
        <rect x={VIEWER.x} y={VIEWER.y} width={VIEWER.w} height={VIEWER.h} rx="12" />
        <text className="sub" x={VIEWER.x + VIEWER.w / 2} y={viewerCy}>
          viewer
        </text>
      </g>
      <path
        className="edge"
        d={`M${VIEWER.x + VIEWER.w + 4} ${routerCy} L${ROUTER.x - 4} ${routerCy}`}
      />

      <g className="node hot">
        <rect x={ROUTER.x} y={ROUTER.y} width={ROUTER.w} height={ROUTER.h} rx="16" />
        <text x={routerCx} y={ROUTER.y + 34}>
          routing
        </text>
        <text className="sub" x={routerCx} y={ROUTER.y + 64}>
          location, load, health
        </text>
      </g>

      {EDGES.map((edge) => {
        const cy = edge.y + EDGE_BOX.h / 2;
        return (
          <g key={edge.name}>
            <path
              className={`edge ${edge.edge}`}
              d={`M${ROUTER.x + ROUTER.w + 4} ${routerCy} C 516 ${routerCy}, 516 ${cy}, ${EDGE_BOX.x - 4} ${cy}`}
            />
            <g className={`node ${edge.tone}`}>
              <rect x={EDGE_BOX.x} y={edge.y} width={EDGE_BOX.w} height={EDGE_BOX.h} rx="12" />
              <text className="sub" x={EDGE_BOX.x + EDGE_BOX.w / 2} y={cy}>
                {edge.name}
              </text>
            </g>
          </g>
        );
      })}

      <path
        className="edge dim"
        d={`M${routerCx} ${ROUTER.y + ROUTER.h + 4} L${routerCx} ${ANALYTICS.y - 4}`}
      />
      <text className="label left" x={routerCx + 14} y={(ROUTER.y + ROUTER.h + ANALYTICS.y) / 2}>
        why this edge
      </text>
      <path
        className="edge dim"
        d={`M${VIEWER.x + VIEWER.w / 2} ${VIEWER.y + VIEWER.h + 4} C ${VIEWER.x + VIEWER.w / 2} ${ANALYTICS.y + 28}, ${ANALYTICS.x - 80} ${ANALYTICS.y + 28}, ${ANALYTICS.x - 4} ${ANALYTICS.y + 28}`}
      />
      <text className="label left" x={VIEWER.x} y={ANALYTICS.y + 62}>
        startup, rebuffering
      </text>

      <g className="node hot">
        <rect x={ANALYTICS.x} y={ANALYTICS.y} width={ANALYTICS.w} height={ANALYTICS.h} rx="12" />
        <text x={ANALYTICS.x + ANALYTICS.w / 2} y={ANALYTICS.y + ANALYTICS.h / 2}>
          analytics
        </text>
      </g>
    </Figure>
  );
};

export default RoutingEvidence;
