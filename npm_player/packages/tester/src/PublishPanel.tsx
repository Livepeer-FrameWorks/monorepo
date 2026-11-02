import { useEffect, useState } from "react";
import { StreamCrafter } from "@livepeer-frameworks/streamcrafter-react";
import type { FwThemePreset } from "@livepeer-frameworks/player-core";
import { mistIngestUris, parseWhipUrl } from "./source";
import { CopyRow } from "./CopyButton";

type Renderer = "auto" | "webgpu" | "webgl" | "canvas2d";
type PublishMode = "whip" | "key";

type Target = { whipUrl: string } | { streamKey: string };

interface PublishPanelProps {
  gatewayUrl?: string;
  /** Mist stream from the play tab; its WHIP and encoder URIs are offered here. */
  mist: { baseUrl: string; streamName: string } | null;
  theme: FwThemePreset;
  debug: boolean;
  whipHelpUrl?: string;
  tlsHelpUrl?: string;
}

export function PublishPanel({
  gatewayUrl,
  mist,
  theme,
  debug,
  whipHelpUrl,
  tlsHelpUrl,
}: PublishPanelProps) {
  const [mode, setMode] = useState<PublishMode>("whip");
  const [whipInput, setWhipInput] = useState("");
  const [keyInput, setKeyInput] = useState("");
  const [renderer, setRenderer] = useState<Renderer>("auto");
  const [target, setTarget] = useState<Target | null>(null);
  const [error, setError] = useState<{ text: string; tls?: boolean } | null>(null);
  const [mounts, setMounts] = useState(0);

  const ingest = mist ? mistIngestUris(mist.baseUrl, mist.streamName) : null;

  useEffect(() => {
    if (ingest && !whipInput) setWhipInput(ingest.whip);
  }, [ingest?.whip]);

  const open = () => {
    setError(null);
    if (mode === "key") {
      const streamKey = keyInput.trim();
      if (!streamKey) {
        setError({ text: "Enter your stream key." });
        return;
      }
      setTarget({ streamKey });
    } else {
      const parsed = parseWhipUrl(whipInput, window.location.protocol);
      if ("error" in parsed) {
        setError({ text: parsed.error, tls: parsed.tls });
        return;
      }
      setTarget({ whipUrl: parsed.whipUrl });
    }
    setMounts((n) => n + 1);
  };

  return (
    <div className="fwt-publish">
      <form
        className="fwt-panel fwt-source"
        onSubmit={(event) => {
          event.preventDefault();
          open();
        }}
      >
        {gatewayUrl ? (
          <div className="fwt-segmented" role="radiogroup" aria-label="Publish target">
            <button
              type="button"
              role="radio"
              aria-checked={mode === "whip"}
              className="fwt-segmented__item"
              onClick={() => setMode("whip")}
            >
              WHIP URL
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={mode === "key"}
              className="fwt-segmented__item"
              onClick={() => setMode("key")}
            >
              FrameWorks stream key
            </button>
          </div>
        ) : null}
        <div className="fwt-source__fields">
          {mode === "whip" ? (
            <label className="fwt-field fwt-field--grow">
              <span className="fwt-label">WHIP URL</span>
              <input
                className="fwt-input"
                type="url"
                required
                spellCheck={false}
                autoComplete="off"
                placeholder="https://example.com/webrtc/my-stream-key"
                value={whipInput}
                onChange={(e) => setWhipInput(e.target.value)}
              />
            </label>
          ) : (
            <label className="fwt-field fwt-field--grow">
              <span className="fwt-label">Stream key</span>
              <input
                className="fwt-input"
                type="password"
                required
                spellCheck={false}
                autoComplete="off"
                value={keyInput}
                onChange={(e) => setKeyInput(e.target.value)}
              />
            </label>
          )}
          <label className="fwt-field">
            <span className="fwt-label">Renderer</span>
            <select
              className="fwt-select"
              value={renderer}
              onChange={(e) => {
                setRenderer(e.target.value as Renderer);
                setMounts((n) => n + 1);
              }}
            >
              <option value="auto">Auto</option>
              <option value="webgpu">WebGPU</option>
              <option value="webgl">WebGL</option>
              <option value="canvas2d">Canvas 2D</option>
            </select>
          </label>
          <button type="submit" className="fwt-button">
            Open studio
          </button>
        </div>
        <p className="fwt-note">
          {mode === "whip"
            ? "A WHIP URL usually contains your stream key. Treat it as a password: it is never put in a shared link."
            : "The studio asks the FrameWorks gateway for the ingest endpoint of this stream key."}
          {mode === "whip" && whipHelpUrl ? (
            <>
              {" "}
              <a href={whipHelpUrl} target="_blank" rel="noreferrer">
                Where to get a WHIP URL
              </a>
            </>
          ) : null}
        </p>
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
      </form>

      <div className="fwt-studio">
        {target ? (
          <StreamCrafter
            key={mounts}
            {...("whipUrl" in target
              ? { whipUrl: target.whipUrl }
              : { gatewayUrl, streamKey: target.streamKey })}
            initialProfile="broadcast"
            autoStartCamera={false}
            enableCompositor
            compositorConfig={{ renderer }}
            theme={theme}
            devMode={debug}
            debug={debug}
          />
        ) : (
          <p className="fwt-overlay-message">Enter where to publish, then open the studio.</p>
        )}
      </div>

      {ingest ? (
        <section className="fwt-panel">
          <h3 className="fwt-panel__title">Publish {mist?.streamName} from an encoder</h3>
          <div className="fwt-copy-list">
            <CopyRow label="RTMP" value={ingest.rtmp} />
            <CopyRow label="SRT" value={ingest.srt} />
            <CopyRow label="WHIP" value={ingest.whip} />
          </div>
        </section>
      ) : null}
    </div>
  );
}
