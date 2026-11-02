import { useEffect, useRef, useState } from "react";

type MistWindow = Window & {
  mistPlay?: (stream: string, options: Record<string, unknown>) => void;
};

const scriptLoads = new Map<string, Promise<void>>();

/** Load MistServer's own player.js from the edge, once per base URL. */
function loadMistPlayer(baseUrl: string): Promise<void> {
  const src = `${baseUrl}/player.js`;
  const pending = scriptLoads.get(src);
  if (pending) return pending;
  const load = new Promise<void>((resolve, reject) => {
    const script = document.createElement("script");
    script.src = src;
    script.onload = () => resolve();
    script.onerror = () => {
      scriptLoads.delete(src);
      reject(new Error(`MistServer did not serve ${src}`));
    };
    document.head.appendChild(script);
  });
  scriptLoads.set(src, load);
  return load;
}

/** MistServer's reference player (skin "dev"), for comparison with the FrameWorks player. */
export function MistPlayerFrame({ baseUrl, streamName }: { baseUrl: string; streamName: string }) {
  const target = useRef<HTMLDivElement>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    const container = target.current;
    setError(null);
    loadMistPlayer(baseUrl)
      .then(() => {
        const mistPlay = (window as MistWindow).mistPlay;
        if (!active || !container) return;
        if (typeof mistPlay !== "function") throw new Error("player.js did not define mistPlay");
        mistPlay(streamName, { target: container, host: baseUrl, controls: true, skin: "dev" });
      })
      .catch((e: unknown) => {
        if (active) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      active = false;
      const video = (container as (HTMLDivElement & { MistVideo?: { unload?: () => void } }) | null)
        ?.MistVideo;
      video?.unload?.();
      container?.replaceChildren();
    };
  }, [baseUrl, streamName]);

  return (
    <div className="fwt-mistplayer">
      <div ref={target} className="fwt-mistplayer__target" />
      {error ? (
        <p role="alert" className="fwt-overlay-message">
          {error}
        </p>
      ) : null}
    </div>
  );
}
