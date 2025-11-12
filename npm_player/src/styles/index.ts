let stylesInjected = false;
const STYLE_URL = "player.css";

/**
 * Ensures the compiled player stylesheet is imported once. When bundling with
 * tools that respect package `style` fields, simply importing the module is enough.
 * For host environments that do not, calling this helper will inject a link tag.
 */
export function ensurePlayerStyles(): void {
  if (typeof document === "undefined") return;
  if (stylesInjected) return;
  const existing = document.querySelector<HTMLLinkElement>('link[data-fw-player-style="true"]');
  if (existing) {
    stylesInjected = true;
    return;
  }

  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = getStylesheetUrl();
  link.setAttribute("data-fw-player-style", "true");
  document.head.appendChild(link);
  stylesInjected = true;
}

/**
 * For SSR-first apps, inject the CSS manually and mark it as applied.
 */
export function injectPlayerStyles(href?: string): void {
  if (typeof document === "undefined") return;
  stylesInjected = true;
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = href ?? getStylesheetUrl();
  link.setAttribute("data-fw-player-style", "true");
  document.head.appendChild(link);
}

function getStylesheetUrl(): string {
  const current = typeof document !== "undefined" ? document.currentScript : null;
  const src = current && "src" in current ? (current as HTMLScriptElement).src : undefined;
  if (!src) return STYLE_URL;
  try {
    const url = new URL(src);
    url.pathname = url.pathname.replace(/\/[^/]*$/, `/${STYLE_URL}`);
    return url.toString();
  } catch {
    return STYLE_URL;
  }
}

// Auto-inject in browser contexts when imported directly.
if (typeof document !== "undefined") {
  ensurePlayerStyles();
}

export default ensurePlayerStyles;
