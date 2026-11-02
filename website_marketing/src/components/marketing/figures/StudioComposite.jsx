import Figure from "./Figure";

/* StreamCrafter in a browser tab: sources mixed into scenes, published over
   WHIP to FrameWorks ingest, then played and multistreamed from there. The
   studio runs in the customer's browser, so it carries the "you" tone. */

const SOURCES = ["camera", "screen share", "media file"];
const TRANSITIONS = ["cut", "fade", "slide"];

const WIDE = {
  source: { x: 24, w: 150, h: 46, y0: 76, stride: 60 },
  studio: { x: 236, y: 86, w: 284, h: 146 },
  chip: { y: 258, w: 84, h: 36, x0: 236, stride: 100 },
  ingest: { x: 600, y: 118, w: 196, h: 82 },
  out: { x: 600, y: 244, w: 196, h: 70 },
};

const StudioComposite = ({
  caption = "Sources are mixed into scenes inside the browser tab and published to FrameWorks over WHIP. There is no encoder to install.",
  spot = false,
  className,
}) => {
  const { source, studio, chip, ingest, out } = WIDE;
  const studioCy = studio.y + studio.h / 2;

  return (
    <Figure
      label="StreamCrafter in a browser tab mixing a camera, a screen share and a media file into scenes with cut, fade and slide transitions, publishing over WHIP to FrameWorks ingest, which serves viewers and multistream destinations"
      caption={caption}
      viewBox="0 0 800 336"
      stackViewBox="0 0 400 590"
      spot={spot}
      className={className}
      stack={
        <>
          <g className="layer">
            <rect x="4" y="24" width="392" height="330" rx="16" />
            <text className="left" x="20" y="48">
              your browser tab
            </text>
          </g>
          {SOURCES.map((name, index) => (
            <g key={name}>
              <g className="node you">
                <rect x={20 + index * 124} y="64" width="112" height="44" rx="12" />
                <text className="sub" x={76 + index * 124} y="86">
                  {name === "screen share" ? "screen" : name}
                </text>
              </g>
              <path className="edge" d={`M${76 + index * 124} 112 L${76 + index * 124} 144`} />
            </g>
          ))}
          <g className="node you">
            <rect x="20" y="150" width="360" height="124" rx="16" />
            <text x="200" y="182">
              StreamCrafter
            </text>
            <text className="sub" x="200" y="214">
              scenes and layers
            </text>
            <text className="sub" x="200" y="244">
              logo and text overlays
            </text>
          </g>
          {TRANSITIONS.map((name, index) => (
            <g key={name} className="chip">
              <rect x={20 + index * 125} y="292" width="110" height="36" rx="10" />
              <text x={75 + index * 125} y="310">
                {name}
              </text>
            </g>
          ))}
          <path className="edge" d="M200 358 L200 394" />
          <text className="label left" x="214" y="377">
            WHIP
          </text>
          <g className="node hot">
            <rect x="80" y="400" width="240" height="72" rx="16" />
            <text x="200" y="424">
              FrameWorks
            </text>
            <text className="sub" x="200" y="452">
              WHIP ingest
            </text>
          </g>
          <path className="edge" d="M200 476 L200 506" />
          <g className="node">
            <rect x="60" y="512" width="280" height="60" rx="12" />
            <text className="sub" x="200" y="542">
              viewers and multistream
            </text>
          </g>
        </>
      }
    >
      <g className="layer">
        <rect x="4" y="24" width="532" height="300" rx="16" />
        <text className="left" x="20" y="48">
          your browser tab
        </text>
      </g>

      {SOURCES.map((name, index) => {
        const y = source.y0 + index * source.stride;
        const cy = y + source.h / 2;
        const ty = studio.y + 43 + index * 30;
        return (
          <g key={name}>
            <g className="node you">
              <rect x={source.x} y={y} width={source.w} height={source.h} rx="12" />
              <text className="sub" x={source.x + source.w / 2} y={cy}>
                {name}
              </text>
            </g>
            <path
              className="edge"
              d={`M${source.x + source.w + 4} ${cy} C ${source.x + source.w + 32} ${cy}, ${studio.x - 32} ${ty}, ${studio.x - 4} ${ty}`}
            />
          </g>
        );
      })}

      <g className="node you">
        <rect x={studio.x} y={studio.y} width={studio.w} height={studio.h} rx="16" />
        <text x={studio.x + studio.w / 2} y={studio.y + 40}>
          StreamCrafter
        </text>
        <text className="sub" x={studio.x + studio.w / 2} y={studio.y + 82}>
          scenes and layers
        </text>
        <text className="sub" x={studio.x + studio.w / 2} y={studio.y + 112}>
          logo and text overlays
        </text>
      </g>

      {TRANSITIONS.map((name, index) => (
        <g key={name} className="chip">
          <rect
            x={chip.x0 + index * chip.stride}
            y={chip.y}
            width={chip.w}
            height={chip.h}
            rx="10"
          />
          <text x={chip.x0 + index * chip.stride + chip.w / 2} y={chip.y + chip.h / 2}>
            {name}
          </text>
        </g>
      ))}

      <path
        className="edge"
        d={`M${studio.x + studio.w + 4} ${studioCy} L${ingest.x - 4} ${studioCy}`}
      />
      <text className="label" x={(studio.x + studio.w + ingest.x) / 2} y={studioCy - 20}>
        WHIP
      </text>

      <g className="node hot">
        <rect x={ingest.x} y={ingest.y} width={ingest.w} height={ingest.h} rx="16" />
        <text x={ingest.x + ingest.w / 2} y={ingest.y + 28}>
          FrameWorks
        </text>
        <text className="sub" x={ingest.x + ingest.w / 2} y={ingest.y + 56}>
          WHIP ingest
        </text>
      </g>

      <path
        className="edge"
        d={`M${ingest.x + ingest.w / 2} ${ingest.y + ingest.h + 4} L${out.x + out.w / 2} ${out.y - 4}`}
      />
      <g className="node">
        <rect x={out.x} y={out.y} width={out.w} height={out.h} rx="12" />
        <text className="sub" x={out.x + out.w / 2} y={out.y + 24}>
          viewers and
        </text>
        <text className="sub" x={out.x + out.w / 2} y={out.y + 48}>
          multistream
        </text>
      </g>
    </Figure>
  );
};

export default StudioComposite;
