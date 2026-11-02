import { useId, useState, type ReactNode } from "react";
import {
  getAvailableThemes,
  getThemeDisplayName,
  type FwThemePreset,
} from "@livepeer-frameworks/player-core";
import { PlayPanel, type TesterSample } from "./PlayPanel";
import { PublishPanel } from "./PublishPanel";
import type { PlaySource } from "./source";
import { tabListKeyDown } from "./tabs";

export interface TesterLink {
  label: string;
  href: string;
}

export interface StreamTesterProps {
  /** FrameWorks gateway GraphQL URL. Enables playback IDs and stream-key publishing. */
  gatewayUrl?: string;
  /** Sources offered in the sample picker. */
  samples?: TesterSample[];
  /** Source the play tab starts with when the page URL shares none. */
  initialSource?: PlaySource;
  /** Keeps the last played source in localStorage under this key and restores it into the form. */
  storageKey?: string;
  /** Debug logging in the player and studio. */
  debug?: boolean;
  /** Guide for serving MistServer over HTTPS, linked from the Mist HTTPS note and mixed-content errors. */
  tlsHelpUrl?: string;
  /** Guide that explains where a WHIP URL comes from. */
  whipHelpUrl?: string;
  /** Next steps shown under the tester, such as the playback and ingest guides. */
  links?: TesterLink[];
  /** Theme for the player and studio; the tester offers a picker when this is uncontrolled. */
  theme?: FwThemePreset;
  onThemeChange?: (theme: FwThemePreset) => void;
  /** Rendered at the end of the tab bar. */
  toolbar?: ReactNode;
}

const TABS = ["play", "publish"] as const;
type Tab = (typeof TABS)[number];

export function StreamTester({
  gatewayUrl,
  samples = [],
  initialSource,
  storageKey,
  debug = false,
  tlsHelpUrl,
  whipHelpUrl,
  links = [],
  theme: themeProp,
  onThemeChange,
  toolbar,
}: StreamTesterProps) {
  const id = useId();
  const [tab, setTab] = useState<Tab>("play");
  // The publish panel stays mounted once opened so a broadcast survives a switch to Play.
  const [publishOpened, setPublishOpened] = useState(false);
  const [ownTheme, setOwnTheme] = useState<FwThemePreset>("default");
  const theme = themeProp ?? ownTheme;
  const [mist, setMist] = useState<{ baseUrl: string; streamName: string } | null>(null);

  const changeTheme = (next: FwThemePreset) => {
    setOwnTheme(next);
    onThemeChange?.(next);
  };

  const selectTab = (t: Tab) => {
    setTab(t);
    if (t === "publish") setPublishOpened(true);
  };

  const tabId = (t: Tab) => `${id}-${t}-tab`;
  const panelId = (t: Tab) => `${id}-${t}-panel`;

  return (
    <div className="fwt-root">
      <div className="fwt-toolbar">
        <div
          className="fwt-tabs"
          role="tablist"
          aria-label="Stream test"
          onKeyDown={tabListKeyDown(TABS, tab, selectTab)}
        >
          {TABS.map((t) => (
            <button
              key={t}
              id={tabId(t)}
              type="button"
              role="tab"
              aria-selected={tab === t}
              aria-controls={panelId(t)}
              tabIndex={tab === t ? 0 : -1}
              className="fwt-tabs__tab"
              onClick={() => selectTab(t)}
            >
              {t === "play" ? "Play" : "Publish"}
            </button>
          ))}
        </div>
        <div className="fwt-toolbar__end">
          <label className="fwt-field fwt-field--inline">
            <span className="fwt-label">Theme</span>
            <select
              className="fwt-select"
              value={theme}
              onChange={(e) => changeTheme(e.target.value as FwThemePreset)}
            >
              {getAvailableThemes().map((preset) => (
                <option key={preset} value={preset}>
                  {getThemeDisplayName(preset)}
                </option>
              ))}
            </select>
          </label>
          {toolbar}
        </div>
      </div>

      <div
        id={panelId("play")}
        role="tabpanel"
        aria-labelledby={tabId("play")}
        hidden={tab !== "play"}
      >
        <PlayPanel
          gatewayUrl={gatewayUrl}
          samples={samples}
          initialSource={initialSource}
          storageKey={storageKey}
          theme={theme}
          debug={debug}
          tlsHelpUrl={tlsHelpUrl}
          onMistSource={setMist}
        />
      </div>
      <div
        id={panelId("publish")}
        role="tabpanel"
        aria-labelledby={tabId("publish")}
        hidden={tab !== "publish"}
      >
        {publishOpened ? (
          <PublishPanel
            gatewayUrl={gatewayUrl}
            mist={mist}
            theme={theme}
            debug={debug}
            whipHelpUrl={whipHelpUrl}
            tlsHelpUrl={tlsHelpUrl}
          />
        ) : null}
      </div>

      {links.length ? (
        <nav className="fwt-links" aria-label="Next steps">
          {links.map((link) => (
            <a key={link.href} href={link.href} className="fwt-link">
              {link.label}
            </a>
          ))}
        </nav>
      ) : null}
    </div>
  );
}
