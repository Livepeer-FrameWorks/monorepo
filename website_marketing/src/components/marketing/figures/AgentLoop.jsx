import Figure from "./Figure";

/* An agent working through the FrameWorks MCP server on the same resources
   the dashboard controls. The agent is the customer's (you); the MCP server
   and the resources are FrameWorks (hot). */

const RESOURCES = ["streams", "recordings", "billing", "infrastructure"];

const AGENT = { x: 4, y: 133, w: 170, h: 72 };
const MCP = { x: 300, y: 121, w: 200, h: 96 };
const RESOURCE = { x: 600, w: 196, h: 44, y0: 60, stride: 58 };

const AgentLoop = ({
  caption = "Agents use the same tools and permissions as the dashboard. Sensitive and billable actions ask for confirmation first.",
  spot = false,
  className,
}) => {
  const cy = MCP.y + MCP.h / 2;

  return (
    <Figure
      label="Your agent connects to the FrameWorks MCP server, which acts on streams, recordings, billing and infrastructure, and asks for confirmation before sensitive actions"
      caption={caption}
      viewBox="0 0 800 300"
      stackViewBox="0 0 400 372"
      spot={spot}
      className={className}
      stack={
        <>
          <g className="node you">
            <rect x="80" y="8" width="240" height="64" rx="12" />
            <text x="200" y="32">
              your agent
            </text>
            <text className="sub" x="200" y="56">
              any MCP client
            </text>
          </g>
          <path className="edge both" d="M200 80 L200 108" />
          <text className="label left" x="214" y="94">
            MCP
          </text>
          <g className="node hot">
            <rect x="60" y="116" width="280" height="84" rx="16" />
            <text x="200" y="146">
              MCP server
            </text>
            <text className="sub" x="200" y="174">
              tools and resources
            </text>
          </g>
          <text className="label" x="200" y="222">
            sensitive actions ask first
          </text>
          <path className="edge" d="M200 236 L200 254" />
          {RESOURCES.map((name, index) => (
            <g key={name} className="node hot">
              <rect
                x={6 + (index % 2) * 200}
                y={262 + Math.floor(index / 2) * 56}
                width="188"
                height="46"
                rx="12"
              />
              <text
                className="sub"
                x={100 + (index % 2) * 200}
                y={285 + Math.floor(index / 2) * 56}
              >
                {name}
              </text>
            </g>
          ))}
        </>
      }
    >
      <g className="node you">
        <rect x={AGENT.x} y={AGENT.y} width={AGENT.w} height={AGENT.h} rx="12" />
        <text x={AGENT.x + AGENT.w / 2} y={AGENT.y + 26}>
          your agent
        </text>
        <text className="sub" x={AGENT.x + AGENT.w / 2} y={AGENT.y + 52}>
          any MCP client
        </text>
      </g>

      <path className="edge both" d={`M${AGENT.x + AGENT.w + 8} ${cy} L${MCP.x - 8} ${cy}`} />
      <text className="label" x={(AGENT.x + AGENT.w + MCP.x) / 2} y={cy - 22}>
        MCP
      </text>

      <g className="node hot">
        <rect x={MCP.x} y={MCP.y} width={MCP.w} height={MCP.h} rx="16" />
        <text x={MCP.x + MCP.w / 2} y={MCP.y + 34}>
          MCP server
        </text>
        <text className="sub" x={MCP.x + MCP.w / 2} y={MCP.y + 64}>
          tools, resources
        </text>
      </g>
      <text className="label" x={MCP.x + MCP.w / 2} y={MCP.y + MCP.h + 28}>
        sensitive actions
      </text>
      <text className="label" x={MCP.x + MCP.w / 2} y={MCP.y + MCP.h + 54}>
        ask first
      </text>

      {RESOURCES.map((name, index) => {
        const y = RESOURCE.y0 + index * RESOURCE.stride;
        const ry = y + RESOURCE.h / 2;
        return (
          <g key={name}>
            <path
              className="edge both"
              d={`M${MCP.x + MCP.w + 8} ${cy} C 552 ${cy}, 552 ${ry}, ${RESOURCE.x - 8} ${ry}`}
            />
            <g className="node hot">
              <rect x={RESOURCE.x} y={y} width={RESOURCE.w} height={RESOURCE.h} rx="12" />
              <text className="sub" x={RESOURCE.x + RESOURCE.w / 2} y={ry}>
                {name}
              </text>
            </g>
          </g>
        );
      })}
    </Figure>
  );
};

export default AgentLoop;
