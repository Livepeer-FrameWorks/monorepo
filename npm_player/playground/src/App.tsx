import { useEffect, useState } from "react";
import {
  StreamTester,
  readPlayerThemeTokens,
  type PlayerThemeToken,
} from "@livepeer-frameworks/player-tester";
import type { FwThemePreset } from "@livepeer-frameworks/player-core";

// Shell tokens recoloured from the player theme, so the page matches the player.
const SHELL_TOKENS: Array<[PlayerThemeToken, string]> = [
  ["--fw-surface-deep", "--background"],
  ["--fw-surface", "--card"],
  ["--fw-text", "--foreground"],
  ["--fw-text-muted", "--muted-foreground"],
  ["--fw-surface-active", "--border"],
  ["--fw-accent", "--primary"],
  ["--fw-on-accent", "--primary-foreground"],
  ["--fw-danger", "--destructive"],
];

/** The default preset keeps the shell's own palette from index.css. */
function applyShellTheme(preset: FwThemePreset) {
  const root = document.documentElement;
  const theme = preset === "default" ? null : readPlayerThemeTokens(preset);
  for (const [fwToken, token] of SHELL_TOKENS) {
    const value = theme?.values[fwToken];
    if (value) root.style.setProperty(token, value);
    else root.style.removeProperty(token);
  }
  if (theme) root.style.setProperty("color-scheme", theme.light ? "light" : "dark");
  else root.style.removeProperty("color-scheme");
}

export default function App() {
  const [theme, setTheme] = useState<FwThemePreset>("default");
  useEffect(() => applyShellTheme(theme), [theme]);

  return (
    <div className="playground">
      <header className="playground__header">
        <img src="/favicon.svg" alt="" />
        <h1>SDK Playground</h1>
        <p>Player and StreamCrafter against a local MistServer or any stream URL.</p>
      </header>
      <StreamTester
        initialSource={{ mode: "mist", baseUrl: "http://localhost:8080", streamName: "live" }}
        storageKey="fw.playground.source"
        debug
        theme={theme}
        onThemeChange={setTheme}
      />
    </div>
  );
}
