import { useEffect, useState } from "react";
import type { PlayerController, StreamState } from "@livepeer-frameworks/player-core";

interface Snapshot {
  state: string;
  player: string | null;
  sourceType: string | null;
  sourceUrl: string | null;
  protocol: string | null;
  nodeId: string | null;
  live: boolean;
  resolution: string | null;
  bitrate: number | null;
  bufferAhead: number | null;
  latency: number | null;
  stalls: number | null;
  frameDropRate: number | null;
  tracks: ReturnType<PlayerController["getTracks"]>;
  stream: StreamState | null;
}

export interface LogEntry {
  at: number;
  text: string;
  tone?: "error" | "warn";
}

const LOG_LIMIT = 80;

function snapshot(controller: PlayerController): Snapshot {
  const video = controller.getVideoElement();
  const quality = controller.getPlaybackQuality();
  const primary = controller.getEndpoints()?.primary;
  return {
    state: controller.getState(),
    player: controller.getCurrentPlayerInfo()?.name ?? null,
    sourceType: controller.getCurrentSourceInfo()?.type ?? null,
    sourceUrl: controller.getCurrentSourceInfo()?.url ?? null,
    protocol: primary?.protocol ?? null,
    nodeId: primary?.nodeId ?? null,
    live: controller.isEffectivelyLive(),
    resolution: video?.videoWidth ? `${video.videoWidth}×${video.videoHeight}` : null,
    bitrate: quality?.bitrate ?? null,
    bufferAhead: quality?.bufferedAhead ?? null,
    latency: quality?.latency ?? null,
    stalls: quality?.stallCount ?? null,
    frameDropRate: quality?.frameDropRate ?? null,
    tracks: controller.getTracks(),
    stream: controller.getStreamState(),
  };
}

/** Follow a PlayerController: a stats snapshot every second plus an event log. */
export function useControllerDiagnostics(controller: PlayerController | null) {
  const [stats, setStats] = useState<Snapshot | null>(null);
  const [log, setLog] = useState<LogEntry[]>([]);

  useEffect(() => {
    setStats(null);
    setLog([]);
    if (!controller) return;
    const add = (text: string, tone?: LogEntry["tone"]) =>
      setLog((prev) => [...prev.slice(-(LOG_LIMIT - 1)), { at: Date.now(), text, tone }]);
    let lastStreamStatus: string | null = null;

    const unsubs = [
      controller.on("stateChange", ({ state }) => add(`State: ${state}`)),
      controller.on("playerSelected", ({ player, source }) =>
        add(`Selected ${player} for ${source.type}`)
      ),
      controller.on("protocolSwapped", (e) =>
        add(
          `Switched ${e.fromPlayer}/${e.fromProtocol} to ${e.toPlayer}/${e.toProtocol}: ${e.reason}`,
          "warn"
        )
      ),
      controller.on("qualityChanged", ({ fromLevel, toLevel }) =>
        add(`Quality ${fromLevel ? `${fromLevel} to ` : ""}${toLevel}`)
      ),
      controller.on("error", ({ error }) => add(`Error: ${error}`, "error")),
      controller.on("errorCleared", () => add("Error cleared")),
      controller.on("playbackFailed", (e) => add(`Playback failed: ${e.message}`, "error")),
      controller.on("autoplayResult", ({ status, reason }) =>
        add(`Autoplay ${status}${reason ? `: ${reason}` : ""}`)
      ),
      controller.on("streamStateChange", ({ state }) => {
        if (state.status === lastStreamStatus) return;
        lastStreamStatus = state.status;
        add(`Stream ${state.status}${state.message ? `: ${state.message}` : ""}`);
      }),
    ];

    const tick = () => setStats(snapshot(controller));
    tick();
    const interval = setInterval(tick, 1000);
    return () => {
      clearInterval(interval);
      unsubs.forEach((off) => off());
    };
  }, [controller]);

  return { stats, log };
}

const kbps = (bps: number | null) =>
  bps && bps > 0
    ? bps >= 1e6
      ? `${(bps / 1e6).toFixed(2)} Mbps`
      : `${Math.round(bps / 1e3)} kbps`
    : "—";
const seconds = (ms: number | null) =>
  ms != null && ms >= 0 ? `${(ms / 1000).toFixed(1)} s` : "—";

function Row({ label, value, title }: { label: string; value: string; title?: string }) {
  return (
    <div className="fwt-stat">
      <dt>{label}</dt>
      <dd title={title}>{value}</dd>
    </div>
  );
}

function mistTracks(stream: StreamState | null): string[] {
  const tracks = stream?.streamInfo?.meta?.tracks;
  if (!tracks) return [];
  return Object.values(tracks)
    .filter((t) => t.type !== "meta")
    .map((t) => {
      const shape = t.width ? ` ${t.width}×${t.height}` : t.channels ? ` ${t.channels}ch` : "";
      return `${t.type} ${t.codec}${shape}${t.bps ? ` · ${kbps(t.bps * 8)}` : ""}`;
    });
}

export function DiagnosticsPanel({ controller }: { controller: PlayerController | null }) {
  const { stats, log } = useControllerDiagnostics(controller);

  if (!controller || !stats) {
    return (
      <aside className="fwt-panel fwt-diagnostics" aria-label="Playback diagnostics">
        <h3 className="fwt-panel__title">Diagnostics</h3>
        <p className="fwt-muted">
          Start playback to see the player, protocol, bitrate, buffer and tracks.
        </p>
      </aside>
    );
  }

  const mist = mistTracks(stats.stream);
  return (
    <aside className="fwt-panel fwt-diagnostics" aria-label="Playback diagnostics">
      <h3 className="fwt-panel__title">Diagnostics</h3>
      <dl className="fwt-stats">
        <Row label="State" value={stats.state} />
        <Row label="Player" value={stats.player ?? "—"} />
        <Row label="Protocol" value={stats.protocol ?? "—"} />
        <Row label="Source" value={stats.sourceType ?? "—"} title={stats.sourceUrl ?? undefined} />
        <Row label="Content" value={stats.live ? "Live" : "On demand"} />
        <Row label="Resolution" value={stats.resolution ?? "—"} />
        <Row label="Bitrate" value={kbps(stats.bitrate)} />
        <Row label="Buffer" value={seconds(stats.bufferAhead)} />
        <Row label="Latency" value={stats.live ? seconds(stats.latency) : "—"} />
        <Row label="Stalls" value={stats.stalls != null ? String(stats.stalls) : "—"} />
        <Row
          label="Dropped frames"
          value={stats.frameDropRate != null ? `${stats.frameDropRate.toFixed(1)}%` : "—"}
        />
      </dl>

      {stats.tracks.length > 0 ? (
        <section>
          <h4 className="fwt-panel__subtitle">Player tracks</h4>
          <ul className="fwt-list">
            {stats.tracks.map((t) => (
              <li key={t.id} className={t.active ? "fwt-list__item--active" : undefined}>
                {t.kind} · {t.label}
                {t.active ? " (active)" : ""}
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {stats.stream ? (
        <section>
          <h4 className="fwt-panel__subtitle">MistServer stream</h4>
          <dl className="fwt-stats">
            <Row label="Status" value={stats.stream.status} />
            {stats.stream.message ? <Row label="Message" value={stats.stream.message} /> : null}
            {stats.stream.streamInfo?.type ? (
              <Row label="Type" value={stats.stream.streamInfo.type} />
            ) : null}
          </dl>
          {mist.length ? (
            <ul className="fwt-list">
              {mist.map((line, i) => (
                <li key={i}>{line}</li>
              ))}
            </ul>
          ) : null}
          {stats.stream.streamInfo?.source?.length ? (
            <>
              <h4 className="fwt-panel__subtitle">Outputs</h4>
              <ul className="fwt-list">
                {stats.stream.streamInfo.source.map((s, i) => (
                  <li key={`${s.type}-${i}`} title={s.url}>
                    {s.hrn ?? s.type}
                  </li>
                ))}
              </ul>
            </>
          ) : null}
        </section>
      ) : null}

      <section>
        <h4 className="fwt-panel__subtitle">Events</h4>
        <ol className="fwt-log" aria-live="off">
          {log
            .slice()
            .reverse()
            .map((entry, i) => (
              <li key={`${entry.at}-${i}`} data-tone={entry.tone}>
                <time>{new Date(entry.at).toLocaleTimeString()}</time> {entry.text}
              </li>
            ))}
        </ol>
      </section>
    </aside>
  );
}
