import { useEffect, useState } from "react";
import { StreamTester } from "@livepeer-frameworks/player-tester";
import { resolveTheme, type FwThemePreset } from "@livepeer-frameworks/player-core";

// Shell tokens recoloured from the player theme, so the page matches the player.
const SHELL_TOKENS = [
  ["surfaceDeep", "--background"],
  ["surface", "--card"],
  ["text", "--foreground"],
  ["textMuted", "--muted-foreground"],
  ["surfaceActive", "--border"],
  ["accent", "--primary"],
  ["danger", "--destructive"],
] as const;

function applyShellTheme(preset: FwThemePreset) {
  const root = document.documentElement;
  const overrides = preset === "default" ? null : resolveTheme(preset);
  for (const [key, token] of SHELL_TOKENS) {
    const value = overrides?.[key];
    if (value) root.style.setProperty(token, value);
    else root.style.removeProperty(token);
  }
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
