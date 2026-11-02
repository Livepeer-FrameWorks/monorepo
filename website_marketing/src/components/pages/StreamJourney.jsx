import { useState } from "react";
import { Link } from "react-router";
import status from "@/data/status.json";
import { compactLabel, featureOf, statusKey } from "@/lib/featureStatus";

// Each chip names a registry feature; its label and colour come from
// status.json, so the journey cannot disagree with the showcase or the docs.
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
          "Push SRT, RTMP or WHIP, or pull RIST, HLS and MPEG-TS sources; physical and local sources are being productized.",
        features: [
          { label: "SRT · RTMP · WHIP push", id: "streams" },
          { label: "SCTE-35 passthrough", id: "scte35-cues" },
          { label: "SDI · NDI · devices", id: "device-discovery-contribution-inputs" },
        ],
      },
      {
        verb: "Shape it",
        title: "Live processing",
        detail:
          "Preserve the source, create renditions, add cue metadata, and place work where capacity exists.",
        features: [
          { label: "Transmux + passthrough", id: "streams" },
          { label: "ABR transcoding", id: "gateway-backed-transcoding" },
          { label: "Composition + SSAI", id: "composition" },
        ],
      },
      {
        verb: "Send it",
        title: "Delivery",
        detail:
          "Route each viewer to an eligible edge and fan the same live source out to external destinations.",
        features: [
          { label: "HLS · DASH · WebRTC", id: "playback" },
          { label: "Geo + health routing", id: "viewer-routing" },
          { label: "Multistream targets", id: "multistream-targets" },
        ],
      },
      {
        verb: "Keep it",
        title: "Media library",
        detail:
          "Turn the live buffer into durable recordings, chapters, clips, thumbnails, and replayable assets.",
        features: [
          { label: "24/7 DVR", id: "dvr-recordings" },
          { label: "Clips + VOD", id: "clips" },
          { label: "Scheduled programming", id: "scheduled-recordings" },
        ],
      },
      {
        verb: "Know it",
        title: "Operations",
        detail:
          "Connect media health, routing decisions, player experience, usage, and automation in one view.",
        features: [
          { label: "Routing telemetry", id: "viewer-routing" },
          { label: "Viewer QoE", id: "analytics" },
          { label: "Incidents + alerts", id: "incidents-and-alerts" },
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
          { label: "WHIP · RTMP · SRT", id: "streams" },
          { label: "StreamCrafter browser studio", id: "streamcrafter" },
          { label: "Remote guests", id: "conferencing" },
        ],
      },
      {
        verb: "Shape it",
        title: "Produce",
        detail:
          "Mix cameras, screens, and media files into scenes in the browser now; server-side production comes next.",
        features: [
          { label: "Scenes, layers, transitions", id: "streamcrafter" },
          { label: "StreamCrafter cloud production", id: "composition" },
          { label: "On-edge AI", id: "live-ai" },
        ],
      },
      {
        verb: "Send it",
        title: "Reach audiences",
        detail:
          "Serve your own player while pushing the same event to social and partner platforms.",
        features: [
          { label: "Player SDKs", id: "player-sdks" },
          { label: "Multistreaming", id: "multistream-targets" },
          { label: "Playback policies", id: "playback-access-control" },
        ],
      },
      {
        verb: "Keep it",
        title: "Publish the replay",
        detail:
          "Keep a rewind window live, preserve the event, and cut moments without a separate media pipeline.",
        features: [
          { label: "Live rewind", id: "dvr-recordings" },
          { label: "Recordings + clips", id: "clips" },
          { label: "Sprite previews", id: "thumbnails-and-previews" },
        ],
      },
      {
        verb: "Know it",
        title: "See the experience",
        detail: "Measure who watched, where they landed, and whether playback felt healthy.",
        features: [
          { label: "Viewer routing", id: "viewer-routing" },
          { label: "Startup + buffering", id: "analytics" },
          { label: "VOD retention", id: "analytics" },
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
          { label: "Stream keys", id: "stream-keys" },
          { label: "Direct browser ingest", id: "streamcrafter" },
          { label: "Pull-source lifecycle", id: "pull-streams" },
        ],
      },
      {
        verb: "Shape it",
        title: "Build the workflow",
        detail:
          "Use APIs and reusable components instead of binding product logic to one hosted video vendor.",
        features: [
          { label: "GraphQL API", id: "developer-api" },
          { label: "React · Svelte · Web Components", id: "player-sdks" },
          { label: "Backend SDKs", id: "backend-sdks" },
        ],
      },
      {
        verb: "Send it",
        title: "Control access",
        detail:
          "Resolve the right playback path and enforce public, signed, or application-defined access.",
        features: [
          { label: "Adaptive player", id: "player-sdks" },
          { label: "JWT + webhook auth", id: "playback-access-control" },
          { label: "DRM", id: "drm" },
        ],
      },
      {
        verb: "Keep it",
        title: "Manage assets",
        detail: "Give live recordings, uploads, clips, and retention one consistent lifecycle.",
        features: [
          { label: "DVR · VOD · clips", id: "vod" },
          { label: "Retention policy", id: "media-retention" },
          { label: "Upload hardening", id: "vod-upload-validation" },
        ],
      },
      {
        verb: "Know it",
        title: "Operate the product",
        detail:
          "Let people, software, and agents use the same controls and the same operational evidence.",
        features: [
          { label: "Dashboard · API · MCP", id: "agents-mcp" },
          { label: "Skipper diagnostics", id: "skipper" },
          { label: "Event webhooks", id: "outbound-webhooks" },
        ],
      },
    ],
  },
];

const resolveChip = (chip) => {
  const feature = featureOf(status, chip.id);
  return { ...chip, key: statusKey(feature), text: compactLabel(feature) };
};

// Legend entries for the chips in one workflow, in a fixed status order.
const LEGEND_ORDER = ["available", "expanding", "beta", "next", "later"];
const legendFor = (stages) => {
  const seen = new Map();
  for (const stage of stages) {
    for (const chip of stage.features) {
      if (!seen.has(chip.key)) seen.set(chip.key, chip.text);
    }
  }
  return LEGEND_ORDER.filter((key) => seen.has(key)).map((key) => ({ key, text: seen.get(key) }));
};

export default function StreamJourney() {
  const [activeId, setActiveId] = useState(workflows[0].id);
  const active = workflows.find((workflow) => workflow.id === activeId) ?? workflows[0];
  const stages = active.stages.map((stage) => ({
    ...stage,
    features: stage.features.map(resolveChip),
  }));
  const legend = legendFor(stages);

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
          {stages.map((stage, index) => (
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
                    <span className="stream-journey__status" data-status={feature.key}>
                      {feature.text}
                    </span>
                    <span>{feature.label}</span>
                  </li>
                ))}
              </ul>
            </li>
          ))}
        </ol>

        <div className="stream-journey__legend">
          <ul aria-label="Status legend">
            {legend.map((entry) => (
              <li key={entry.key}>
                <span className="stream-journey__status" data-status={entry.key}>
                  {entry.text}
                </span>
              </li>
            ))}
          </ul>
          <Link to="/showcase" className="stream-journey__legend-link">
            What ships today and what comes next <span aria-hidden="true">→</span>
          </Link>
        </div>

        <div className="stream-journey__control">
          <span>One control plane</span>
          <strong>Dashboard · GraphQL · CLI · MCP</strong>
          <span>Hosted · hybrid · your own edges</span>
        </div>
      </div>
    </div>
  );
}
