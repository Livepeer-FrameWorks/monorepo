import { useCallback, useEffect, useMemo, useState } from "react";
import { Player } from "@livepeer-frameworks/player-react";
import { globalPlayerManager } from "@livepeer-frameworks/player-core";
import type { FwThemePreset, PlayerController } from "@livepeer-frameworks/player-core";
import {
  PLAY_PROTOCOLS,
  readShareQuery,
  resolvePlaySource,
  shareQuery,
  type PlayMode,
  type PlayProtocol,
  type PlaySource,
  type PlayerInput,
} from "./source";
import { DiagnosticsPanel } from "./Diagnostics";
import { MistPlayerFrame } from "./MistPlayerFrame";
import { CopyButton } from "./CopyButton";
import { EmbedSnippets } from "./EmbedSnippets";

export interface TesterSample {
  id: string;
  label: string;
  source: PlaySource;
}

interface PlayPanelProps {
  gatewayUrl?: string;
  samples: TesterSample[];
  initialSource?: PlaySource;
  theme: FwThemePreset;
  debug: boolean;
  tlsHelpUrl?: string;
  /** Called with the Mist base and name whenever a Mist source plays, for the publish tab. */
  onMistSource?: (mist: { baseUrl: string; streamName: string } | null) => void;
  storageKey?: string;
}

const MODE_LABELS: Record<PlayMode, string> = {
  url: "URL",
  mist: "MistServer",
  playback: "Playback ID",
};

type Draft = { url: string; baseUrl: string; streamName: string; playbackId: string };

const EMPTY_DRAFT: Draft = { url: "", baseUrl: "", streamName: "", playbackId: "" };

function draftFrom(source: PlaySource | undefined, base: Draft): Draft {
  if (!source) return base;
  if (source.mode === "url") return { ...base, url: source.url };
  if (source.mode === "mist")
    return { ...base, baseUrl: source.baseUrl, streamName: source.streamName };
  return { ...base, playbackId: source.playbackId };
}

function readStored(key: string | undefined): PlaySource | undefined {
  if (!key || typeof window === "undefined") return undefined;
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as PlaySource) : undefined;
  } catch {
    return undefined;
  }
}

function writeStored(key: string | undefined, source: PlaySource) {
  if (!key || typeof window === "undefined") return;
  try {
    window.localStorage.setItem(key, JSON.stringify(source));
  } catch {
    // storage can be unavailable (private mode, blocked site data)
  }
}

export function PlayPanel({
  gatewayUrl,
  samples,
  initialSource,
  theme,
  debug,
  tlsHelpUrl,
  onMistSource,
  storageKey,
}: PlayPanelProps) {
  const modes = useMemo<PlayMode[]>(
    () => (gatewayUrl ? ["playback", "url", "mist"] : ["url", "mist"]),
    [gatewayUrl]
  );
  const [start] = useState(() => {
    const shared = typeof window !== "undefined" ? readShareQuery(window.location.search) : null;
    return { shared, source: shared ?? readStored(storageKey) ?? initialSource };
  });
  const [mode, setMode] = useState<PlayMode>(start.source?.mode ?? modes[0]);
  const [draft, setDraft] = useState<Draft>(() => draftFrom(start.source, EMPTY_DRAFT));
  const [protocol, setProtocol] = useState<PlayProtocol>(start.source?.protocol ?? "auto");
  const [thumbnailUrl, setThumbnailUrl] = useState("");
  const [forcePlayer, setForcePlayer] = useState("auto");
  const [playing, setPlaying] = useState<{
    source: PlaySource;
    input: PlayerInput;
    key: number;
  } | null>(null);
  const [error, setError] = useState<{ text: string; tls?: boolean } | null>(null);
  const [busy, setBusy] = useState(false);
  const [engine, setEngine] = useState<"frameworks" | "mist">("frameworks");
  const [controller, setController] = useState<PlayerController | null>(null);

  const pageProtocol = typeof window !== "undefined" ? window.location.protocol : "https:";

  const play = useCallback(
    async (source: PlaySource) => {
      setBusy(true);
      setError(null);
      const resolved = await resolvePlaySource(source, { pageProtocol, gatewayUrl });
      setBusy(false);
      if ("error" in resolved) {
        setError({ text: resolved.error, tls: resolved.tls });
        return;
      }
      writeStored(storageKey, resolved.source);
      setPlaying((prev) => ({ ...resolved, key: (prev?.key ?? 0) + 1 }));
    },
    [gatewayUrl, pageProtocol, storageKey]
  );

  // A shared link plays on arrival; so does the host's initial source.
  useEffect(() => {
    if (start.source && (start.shared || !storageKey)) void play(start.source);
  }, []);

  const mist = playing?.input.mistUrl
    ? { baseUrl: playing.input.mistUrl, streamName: playing.input.contentId }
    : null;
  useEffect(() => {
    onMistSource?.(mist);
    if (!mist) setEngine("frameworks");
  }, [mist?.baseUrl, mist?.streamName]);

  const currentSource = (): PlaySource => {
    const p = protocol === "auto" ? undefined : protocol;
    if (mode === "url") return { mode, url: draft.url, protocol: p };
    if (mode === "mist")
      return { mode, baseUrl: draft.baseUrl, streamName: draft.streamName, protocol: p };
    return { mode, playbackId: draft.playbackId, protocol: p };
  };

  const pickSample = (id: string) => {
    const sample = samples.find((s) => s.id === id);
    if (!sample) return;
    setMode(sample.source.mode);
    setDraft((d) => draftFrom(sample.source, d));
    setProtocol(sample.source.protocol ?? "auto");
    void play(sample.source);
  };

  const shareLink = useMemo(() => {
    if (!playing || typeof window === "undefined") return null;
    const query = shareQuery(playing.source);
    if (!query) return null;
    return `${window.location.origin}${window.location.pathname}?${query}`;
  }, [playing]);

  // Players register on the first attach, so the list fills once something played.
  const playerChoices = useMemo(() => {
    const choices = new Map<string, string>();
    for (const p of globalPlayerManager.getRegisteredPlayers()) {
      choices.set(p.capability.shortname, p.capability.name);
    }
    return [...choices];
  }, [controller]);

  const options = useMemo(
    () =>
      playing
        ? {
            autoplay: true,
            muted: true,
            controls: true,
            devMode: true,
            debug,
            theme,
            gatewayUrl: playing.input.gatewayUrl,
            viewerProtocol: playing.input.viewerProtocol,
            mistUrl: playing.input.mistUrl,
            forceType: playing.input.forceType,
            forcePlayer: forcePlayer === "auto" ? undefined : forcePlayer,
          }
        : null,
    [playing, debug, theme, forcePlayer]
  );

  return (
    <div className="fwt-play">
      <form
        className="fwt-panel fwt-source"
        onSubmit={(event) => {
          event.preventDefault();
          void play(currentSource());
        }}
      >
        <div className="fwt-source__top">
          <div className="fwt-segmented" role="radiogroup" aria-label="Source type">
            {modes.map((m) => (
              <button
                key={m}
                type="button"
                role="radio"
                aria-checked={mode === m}
                className="fwt-segmented__item"
                onClick={() => setMode(m)}
              >
                {MODE_LABELS[m]}
              </button>
            ))}
          </div>
          {samples.length ? (
            <label className="fwt-field fwt-field--inline">
              <span className="fwt-label">Sample</span>
              <select
                className="fwt-select"
                value=""
                onChange={(event) => pickSample(event.target.value)}
              >
                <option value="">Choose a sample</option>
                {samples.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.label}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
        </div>

        <div className="fwt-source__fields">
          {mode === "url" ? (
            <label className="fwt-field fwt-field--grow">
              <span className="fwt-label">Media, WHEP or MistServer URL</span>
              <input
                className="fwt-input"
                type="url"
                required
                spellCheck={false}
                autoComplete="off"
                placeholder="https://example.com/live/index.m3u8"
                value={draft.url}
                onChange={(e) => setDraft({ ...draft, url: e.target.value })}
              />
            </label>
          ) : mode === "mist" ? (
            <>
              <label className="fwt-field fwt-field--grow">
                <span className="fwt-label">MistServer base URL</span>
                <input
                  className="fwt-input"
                  type="text"
                  required
                  spellCheck={false}
                  autoComplete="off"
                  placeholder="https://mist.example.com:4433"
                  value={draft.baseUrl}
                  onChange={(e) => setDraft({ ...draft, baseUrl: e.target.value })}
                />
              </label>
              <label className="fwt-field">
                <span className="fwt-label">Stream name</span>
                <input
                  className="fwt-input"
                  type="text"
                  required
                  spellCheck={false}
                  autoComplete="off"
                  placeholder="live"
                  value={draft.streamName}
                  onChange={(e) => setDraft({ ...draft, streamName: e.target.value })}
                />
              </label>
            </>
          ) : (
            <label className="fwt-field fwt-field--grow">
              <span className="fwt-label">FrameWorks playback ID</span>
              <input
                className="fwt-input"
                type="text"
                required
                spellCheck={false}
                autoComplete="off"
                placeholder="a1b2c3d4e5f6g7h8"
                value={draft.playbackId}
                onChange={(e) => setDraft({ ...draft, playbackId: e.target.value })}
              />
            </label>
          )}
          <label className="fwt-field">
            <span className="fwt-label">Protocol</span>
            <select
              className="fwt-select"
              value={protocol}
              onChange={(e) => setProtocol(e.target.value as PlayProtocol)}
            >
              {PLAY_PROTOCOLS.map((p) => (
                <option key={p} value={p}>
                  {p === "auto" ? "Auto" : p}
                </option>
              ))}
            </select>
          </label>
          <button type="submit" className="fwt-button" disabled={busy}>
            {busy ? "Checking…" : "Play"}
          </button>
        </div>

        {error ? (
          <p role="alert" className="fwt-alert fwt-alert--error">
            {error.text}
            {error.tls && tlsHelpUrl ? (
              <>
                {" "}
                <a href={tlsHelpUrl} target="_blank" rel="noreferrer">
                  Set up HTTPS
                </a>
              </>
            ) : null}
          </p>
        ) : null}
        {mode === "mist" && pageProtocol === "https:" ? (
          <p className="fwt-note">
            This page runs on HTTPS, so your MistServer must serve HTTPS too: enable its HTTPS
            output with a certificate, or put a TLS proxy such as Caddy in front of it.
            {tlsHelpUrl ? (
              <>
                {" "}
                <a href={tlsHelpUrl} target="_blank" rel="noreferrer">
                  How FrameWorks edges handle TLS
                </a>
              </>
            ) : null}
          </p>
        ) : null}
      </form>

      <div className="fwt-workspace">
        <div className="fwt-stage">
          {mist ? (
            <div
              className="fwt-segmented fwt-segmented--small"
              role="radiogroup"
              aria-label="Player"
            >
              <button
                type="button"
                role="radio"
                aria-checked={engine === "frameworks"}
                className="fwt-segmented__item"
                onClick={() => setEngine("frameworks")}
              >
                FrameWorks player
              </button>
              <button
                type="button"
                role="radio"
                aria-checked={engine === "mist"}
                className="fwt-segmented__item"
                onClick={() => setEngine("mist")}
              >
                MistPlayer
              </button>
            </div>
          ) : null}
          <div className="fwt-media">
            {playing && options && engine === "frameworks" ? (
              <Player
                key={`${playing.key}-${forcePlayer}`}
                ref={setController}
                contentId={playing.input.contentId}
                contentType={playing.input.contentType}
                endpoints={playing.input.endpoints}
                thumbnailUrl={thumbnailUrl || undefined}
                options={options}
              />
            ) : playing && mist && engine === "mist" ? (
              <MistPlayerFrame baseUrl={mist.baseUrl} streamName={mist.streamName} />
            ) : (
              <p className="fwt-overlay-message">
                Pick a sample or enter a source, then press Play.
              </p>
            )}
          </div>
          <div className="fwt-stage__bar">
            <details className="fwt-details">
              <summary>Player options</summary>
              <div className="fwt-details__body">
                <label className="fwt-field">
                  <span className="fwt-label">Player</span>
                  <select
                    className="fwt-select"
                    value={forcePlayer}
                    onChange={(e) => setForcePlayer(e.target.value)}
                  >
                    <option value="auto">Auto</option>
                    {playerChoices.map(([shortname, name]) => (
                      <option key={shortname} value={shortname}>
                        {name}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="fwt-field fwt-field--grow">
                  <span className="fwt-label">Thumbnail URL</span>
                  <input
                    className="fwt-input"
                    type="url"
                    spellCheck={false}
                    placeholder="https://example.com/poster.jpg"
                    value={thumbnailUrl}
                    onChange={(e) => setThumbnailUrl(e.target.value)}
                  />
                </label>
              </div>
            </details>
            {shareLink ? <CopyButton text={shareLink} label="Copy link" /> : null}
          </div>
        </div>
        <DiagnosticsPanel controller={engine === "frameworks" && playing ? controller : null} />
      </div>

      {playing ? <EmbedSnippets input={playing.input} /> : null}
    </div>
  );
}
