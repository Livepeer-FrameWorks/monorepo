import { forwardRef, useState, useEffect, useCallback, useRef } from "react";
import { cn } from "@/lib/utils";
import config from "../../../config";

const domain = config.domain || "frameworks.network";
const gqlEndpoint = config.gatewayUrl || "/graphql";

const badgeTooltips = {
  verified: "Confirmed from official FrameWorks documentation",
  sourced: "Cited from external documentation",
  best_guess: "Inferred from context — verify before acting",
};

const conversations = [
  {
    tab: "Manage",
    messages: [
      {
        role: "user",
        text: "I want to go live with a stream called Product Demo",
      },
      {
        role: "tool",
        tools: [{ name: "create_stream", label: "Created stream" }],
      },
      {
        role: "assistant",
        badge: "verified",
        text: 'Done — "Product Demo" is live-ready. You can go live right now using StreamCrafter in the dashboard, or connect an external encoder like OBS.',
      },
      {
        role: "user",
        text: "I'll use OBS",
      },
      {
        role: "assistant",
        badge: "verified",
        text: `In OBS go to Settings → Stream → Custom.\n\nServer: rtmp://ingest.${domain}/live\nStream Key: sk_live_████████████\n\nHit Start Streaming and you're live.`,
        code: true,
      },
      {
        role: "user",
        text: "I'm live! Can you clip the last 30 seconds?",
      },
      {
        role: "tool",
        tools: [{ name: "create_clip", label: "Clipped 30s" }],
      },
      {
        role: "assistant",
        badge: "verified",
        text: 'Clipped! "Product Demo — Clip 1" is saved to your recordings. Share it directly or download from the stream page.',
      },
    ],
    source: "FrameWorks API, Stream Management",
  },
  {
    tab: "Troubleshoot",
    messages: [
      {
        role: "user",
        text: "My stream health page is showing a high rebuffer ratio — what's going on?",
      },
      {
        role: "tool",
        tools: [{ name: "diagnose_rebuffering", label: "Analyzed rebuffering" }],
      },
      {
        role: "assistant",
        badge: "verified",
        text: "Your ingest bitrate is 6 Mbps but you don't have transcoding enabled, so viewers on slower connections can't keep up. Enable Adaptive Bitrate in your stream settings to add lower-quality renditions automatically.",
      },
      {
        role: "user",
        text: "Done. Anything else I should check?",
      },
      {
        role: "tool",
        tools: [{ name: "search_knowledge", label: "Searched knowledge base" }],
      },
      {
        role: "assistant",
        badge: "sourced",
        text: "Your keyframe interval is set to 4 seconds — drop it to 2s in OBS under Settings → Output → Keyframe Interval. HLS and DASH split segments on keyframes, so 4s intervals mean 4s minimum latency and slower recovery after a stall. At 2s you halve both.",
      },
    ],
    source: "FrameWorks Docs, OBS Knowledge Base",
  },
  {
    tab: "Build",
    messages: [
      {
        role: "user",
        text: "I need to create streams from my Python backend",
      },
      {
        role: "tool",
        tools: [
          { name: "introspect_schema", label: "Read API schema" },
          { name: "generate_query", label: "Generated createStream mutation" },
        ],
      },
      {
        role: "assistant",
        badge: "verified",
        text: `mutation CreateStream($in: CreateStreamInput!) {\n  createStream(input: $in) {\n    id\n    streamKey\n    playbackId\n    rtmpUrl\n  }\n}`,
        code: true,
      },
      {
        role: "user",
        text: "Give me a working snippet I can drop in",
      },
      {
        role: "assistant",
        badge: "best_guess",
        text: `import httpx\n\nresp = httpx.post(\n  "${gqlEndpoint}",\n  headers={"Authorization": "Bearer sk_..."},\n  json={\n    "query": CREATE_STREAM,\n    "variables": {"in": {"name": "My Stream"}}\n  }\n)\nprint(resp.json()["data"]["createStream"]["streamKey"])`,
        code: true,
      },
    ],
    source: "FrameWorks GraphQL Schema, API Reference",
  },
];

const ROTATE_MS = 10000;

const ToolCheckIcon = () => (
  <svg
    className="skipper-preview__tool-icon"
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
    width="10"
    height="10"
    aria-hidden="true"
  >
    <path d="M3 8.5l3.5 3.5 6.5-8" />
  </svg>
);

const CitationIcon = () => (
  <svg
    className="skipper-preview__citation-icon"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
    width="12"
    height="12"
    aria-hidden="true"
  >
    <path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20" />
    <path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z" />
  </svg>
);

const SkipperConversationPreview = forwardRef(({ className, ...props }, ref) => {
  const [active, setActive] = useState(0);
  const [paused, setPaused] = useState(false);
  const genRef = useRef(0);
  const [progressKey, setProgressKey] = useState("0-0");

  const advance = useCallback(() => {
    setActive((prev) => {
      const next = (prev + 1) % conversations.length;
      setProgressKey(`${next}-${genRef.current}`);
      return next;
    });
  }, []);

  const handlePause = useCallback(() => setPaused(true), []);

  const handleResume = useCallback(() => {
    genRef.current += 1;
    setProgressKey(`${active}-${genRef.current}`);
    setPaused(false);
  }, [active]);

  const handleTabClick = useCallback((i) => {
    genRef.current += 1;
    setActive(i);
    setProgressKey(`${i}-${genRef.current}`);
    setPaused(true);
  }, []);

  useEffect(() => {
    if (paused) return;
    const id = setInterval(advance, ROTATE_MS);
    return () => clearInterval(id);
  }, [paused, advance]);

  const convo = conversations[active];

  return (
    <div
      ref={ref}
      className={cn("skipper-preview", className)}
      onMouseEnter={handlePause}
      onMouseLeave={handleResume}
      {...props}
    >
      <div className="skipper-preview__header">
        <div className="skipper-preview__header-left">
          <span className="skipper-preview__dot" aria-hidden="true" />
          <span className="skipper-preview__name">Skipper</span>
        </div>
        <div className="skipper-preview__tabs" role="tablist">
          {conversations.map((c, i) => (
            <button
              key={c.tab}
              role="tab"
              aria-selected={i === active}
              className={cn("skipper-preview__tab", i === active && "skipper-preview__tab--active")}
              onClick={() => handleTabClick(i)}
            >
              {c.tab}
            </button>
          ))}
        </div>
      </div>

      <div className="skipper-preview__body" role="tabpanel">
        {convo.messages.map((msg, i) =>
          msg.role === "tool" ? (
            <div key={i} className="skipper-preview__tool-group">
              {msg.tools.map((t, j) => (
                <div key={j} className="skipper-preview__tool-step">
                  <ToolCheckIcon />
                  <span>{t.label}</span>
                </div>
              ))}
            </div>
          ) : (
            <div
              key={i}
              className={cn("skipper-preview__bubble", `skipper-preview__bubble--${msg.role}`)}
            >
              {msg.badge && (
                <span
                  className={cn("skipper-preview__badge", `skipper-preview__badge--${msg.badge}`)}
                  data-tooltip={badgeTooltips[msg.badge]}
                >
                  {msg.badge.replace("_", " ")}
                </span>
              )}
              {msg.code ? (
                <pre className="skipper-preview__code">{msg.text}</pre>
              ) : (
                <p>{msg.text}</p>
              )}
            </div>
          )
        )}

        <div className="skipper-preview__citation">
          <CitationIcon />
          <span>{convo.source}</span>
        </div>
      </div>

      <div className="skipper-preview__footer">
        <div
          className="skipper-preview__input-mock"
          data-tooltip="Try Skipper in the Dashboard or Docs"
        >
          <span>Ask about streams, codecs, or diagnostics...</span>
        </div>
      </div>

      <div className="skipper-preview__progress">
        <div
          className="skipper-preview__progress-bar"
          style={{
            animationDuration: `${ROTATE_MS}ms`,
            animationPlayState: paused ? "paused" : "running",
          }}
          key={progressKey}
        />
      </div>
    </div>
  );
});

SkipperConversationPreview.displayName = "SkipperConversationPreview";

export default SkipperConversationPreview;
