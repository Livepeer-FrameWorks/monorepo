import { useState } from "react";

const STATUS_LABELS = {
  available: "Available",
  expanding: "Expanding",
  next: "Next",
};

const workflows = [
  {
    id: "broadcast",
    label: "Broadcast & linear",
    summary:
      "Move a contribution feed through cueing, processing, regional delivery, archive, and operational telemetry.",
    stages: [
      {
        verb: "Bring it in",
        title: "Contribution",
        detail:
          "SRT, RIST, RTMP, HLS and MPEG-TS today; physical and local sources are being productized.",
        features: [
          { label: "SRT · RIST · MPEG-TS", status: "available" },
          { label: "SCTE-35 + HLS I/O", status: "expanding" },
          { label: "SDI · NDI · devices", status: "next" },
        ],
      },
      {
        verb: "Shape it",
        title: "Live processing",
        detail:
          "Preserve the source, create renditions, add cue metadata, and place work where capacity exists.",
        features: [
          { label: "Transmux + passthrough", status: "available" },
          { label: "ABR transcoding", status: "expanding" },
          { label: "Composition + SSAI", status: "next" },
        ],
      },
      {
        verb: "Send it",
        title: "Delivery",
        detail:
          "Route each viewer to an eligible edge and fan the same live source out to external destinations.",
        features: [
          { label: "HLS · DASH · WebRTC", status: "available" },
          { label: "Geo + health routing", status: "available" },
          { label: "Multistream targets", status: "available" },
        ],
      },
      {
        verb: "Keep it",
        title: "Media library",
        detail:
          "Turn the live buffer into durable recordings, chapters, clips, thumbnails, and replayable assets.",
        features: [
          { label: "24/7 DVR", status: "available" },
          { label: "Clips + VOD", status: "available" },
          { label: "Scheduled programming", status: "next" },
        ],
      },
      {
        verb: "Know it",
        title: "Operations",
        detail:
          "Connect media health, routing decisions, player experience, usage, and automation in one view.",
        features: [
          { label: "Routing telemetry", status: "available" },
          { label: "Viewer QoE", status: "expanding" },
          { label: "Incidents + alerts", status: "next" },
        ],
      },
    ],
  },
  {
    id: "events",
    label: "Live events",
    summary:
      "Produce from a browser or encoder, reach every destination, and leave behind a recording viewers can replay.",
    stages: [
      {
        verb: "Bring it in",
        title: "Go live",
        detail: "Publish from StreamCrafter, OBS, a hardware encoder, or an upstream feed.",
        features: [
          { label: "WHIP · RTMP · SRT", status: "available" },
          { label: "Browser studio", status: "available" },
          { label: "Remote guests", status: "next" },
        ],
      },
      {
        verb: "Shape it",
        title: "Produce",
        detail:
          "Mix browser sources now and move heavier processing to the right media or compute node.",
        features: [
          { label: "Browser composition", status: "available" },
          { label: "Live renditions", status: "expanding" },
          { label: "On-edge AI", status: "next" },
        ],
      },
      {
        verb: "Send it",
        title: "Reach audiences",
        detail:
          "Serve your own player while pushing the same event to social and partner platforms.",
        features: [
          { label: "Player SDKs", status: "available" },
          { label: "Multistreaming", status: "available" },
          { label: "Playback policies", status: "available" },
        ],
      },
      {
        verb: "Keep it",
        title: "Publish the replay",
        detail:
          "Keep a rewind window live, preserve the event, and cut moments without a separate media pipeline.",
        features: [
          { label: "Live rewind", status: "available" },
          { label: "Recordings + clips", status: "available" },
          { label: "Sprite previews", status: "expanding" },
        ],
      },
      {
        verb: "Know it",
        title: "See the experience",
        detail: "Measure who watched, where they landed, and whether playback felt healthy.",
        features: [
          { label: "Live audience", status: "available" },
          { label: "Startup + buffering", status: "expanding" },
          { label: "VOD retention", status: "expanding" },
        ],
      },
    ],
  },
  {
    id: "platform",
    label: "Video products",
    summary:
      "Build streaming into an application without surrendering the media path, operational data, or deployment model.",
    stages: [
      {
        verb: "Bring it in",
        title: "Create",
        detail:
          "Give every creator or channel a managed input with API-controlled credentials and placement.",
        features: [
          { label: "Stream keys", status: "available" },
          { label: "Direct browser ingest", status: "available" },
          { label: "Pull-source lifecycle", status: "expanding" },
        ],
      },
      {
        verb: "Shape it",
        title: "Build the workflow",
        detail:
          "Use APIs and reusable components instead of binding product logic to one hosted video vendor.",
        features: [
          { label: "GraphQL API", status: "expanding" },
          { label: "React · Svelte · Web Components", status: "available" },
          { label: "Backend SDKs", status: "next" },
        ],
      },
      {
        verb: "Send it",
        title: "Control access",
        detail:
          "Resolve the right playback path and enforce public, signed, or application-defined access.",
        features: [
          { label: "Adaptive player", status: "available" },
          { label: "JWT + webhook auth", status: "available" },
          { label: "DRM", status: "next" },
        ],
      },
      {
        verb: "Keep it",
        title: "Manage assets",
        detail: "Give live recordings, uploads, clips, and retention one consistent lifecycle.",
        features: [
          { label: "DVR · VOD · clips", status: "available" },
          { label: "Retention policy", status: "available" },
          { label: "Upload hardening", status: "expanding" },
        ],
      },
      {
        verb: "Know it",
        title: "Operate the product",
        detail:
          "Let people, software, and agents use the same controls and the same operational evidence.",
        features: [
          { label: "Dashboard · API · MCP", status: "available" },
          { label: "Skipper diagnostics", status: "expanding" },
          { label: "Event webhooks", status: "available" },
        ],
      },
    ],
  },
];

export default function StreamJourney() {
  const [activeId, setActiveId] = useState(workflows[0].id);
  const active = workflows.find((workflow) => workflow.id === activeId) ?? workflows[0];

  return (
    <div className="stream-journey">
      <div className="stream-journey__tabs" role="tablist" aria-label="Example video workflows">
        {workflows.map((workflow) => (
          <button
            key={workflow.id}
            type="button"
            role="tab"
            aria-selected={active.id === workflow.id}
            aria-controls="stream-journey-panel"
            className="stream-journey__tab"
            onClick={() => setActiveId(workflow.id)}
          >
            {workflow.label}
          </button>
        ))}
      </div>

      <div id="stream-journey-panel" role="tabpanel" className="stream-journey__panel">
        <p className="stream-journey__summary">{active.summary}</p>
        <ol className="stream-journey__stages">
          {active.stages.map((stage, index) => (
            <li key={stage.verb} className="stream-journey__stage">
              <div className="stream-journey__stage-head">
                <span className="stream-journey__index">{String(index + 1).padStart(2, "0")}</span>
                <div>
                  <span className="stream-journey__verb">{stage.verb}</span>
                  <h3>{stage.title}</h3>
                </div>
              </div>
              <p>{stage.detail}</p>
              <ul className="stream-journey__features">
                {stage.features.map((feature) => (
                  <li key={feature.label}>
                    <span className="stream-journey__status" data-status={feature.status}>
                      {STATUS_LABELS[feature.status]}
                    </span>
                    <span>{feature.label}</span>
                  </li>
                ))}
              </ul>
            </li>
          ))}
        </ol>

        <div className="stream-journey__control">
          <span>One control plane</span>
          <strong>Dashboard · GraphQL · CLI · MCP</strong>
          <span>Hosted · hybrid · your own edges</span>
        </div>
      </div>
    </div>
  );
}
