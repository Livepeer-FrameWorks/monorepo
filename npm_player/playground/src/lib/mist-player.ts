/**
 * Dynamic MistServer embed player loader.
 * Injects /player.js from the MistServer HTTP endpoint at runtime,
 * then exposes mount/destroy lifecycle matching LSP's player_mist.js.
 */

let loadPromise: Promise<void> | null = null;

export function loadMistPlayerScript(viewerBase: string): Promise<void> {
  if (loadPromise) return loadPromise;

  const src = `${viewerBase}/player.js`;
  const existing = document.querySelector(`script[src="${src}"]`);
  if (existing && typeof (window as any).mistPlay === "function") {
    loadPromise = Promise.resolve();
    return loadPromise;
  }

  loadPromise = new Promise<void>((resolve, reject) => {
    const script = document.createElement("script");
    script.src = src;
    script.onload = () => resolve();
    script.onerror = () => {
      loadPromise = null;
      reject(new Error(`Failed to load MistServer player from ${src}`));
    };
    document.head.appendChild(script);
  });

  return loadPromise;
}

export function mountMistPlayer(container: HTMLElement, streamName: string, host: string): void {
  const mistPlay = (window as any).mistPlay;
  if (typeof mistPlay !== "function") {
    throw new Error("mistPlay not available — player.js not loaded");
  }
  mistPlay(streamName, {
    target: container,
    host,
    controls: true,
    skin: "dev",
  });
}

export function destroyMistPlayer(container: HTMLElement): void {
  const mv = (container as any).MistVideo;
  if (mv && typeof mv.unload === "function") {
    mv.unload();
  }
  while (container.firstChild) {
    container.removeChild(container.firstChild);
  }
  loadPromise = null;
}
