import { usePlayground } from "@/context/usePlayground";
import type { FwThemePreset } from "@livepeer-frameworks/player-core";
import { getAvailableThemes, getThemeDisplayName } from "@livepeer-frameworks/player-core";

type HeaderProps = {
  onToggleDrawer: () => void;
};

export function Header({ onToggleDrawer }: HeaderProps) {
  const { connectionStatus, theme, setTheme } = usePlayground();

  const statusText =
    connectionStatus === "connected"
      ? "Connected"
      : connectionStatus === "failed"
        ? "Failed to connect"
        : "";

  return (
    <header className="header-bar">
      <div className="flex items-center gap-3 px-6 py-4">
        <button
          className="config-drawer-toggle"
          onClick={onToggleDrawer}
          aria-label="Toggle config panel"
        >
          <svg
            width="20"
            height="20"
            viewBox="0 0 20 20"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
          >
            <path d="M3 5h14M3 10h14M3 15h14" />
          </svg>
        </button>
        <img src="/favicon.svg" alt="FrameWorks" className="h-8 w-8" />
        <h1 className="text-xl font-semibold">SDK Playground</h1>
        <span
          className="text-xs px-2 py-0.5 rounded"
          style={{ background: "hsl(var(--tn-orange) / 0.15)", color: "hsl(var(--tn-orange))" }}
        >
          StreamCrafter
        </span>
        <span
          className="text-xs px-2 py-0.5 rounded"
          style={{
            background: "hsl(var(--tn-teal) / 0.15)",
            color: "hsl(var(--tn-teal))",
          }}
        >
          Player
        </span>
        <select
          className="ml-4 rounded border border-white/10 bg-white/5 px-2 py-1 text-xs"
          value={theme}
          onChange={(e) => setTheme(e.target.value as FwThemePreset)}
        >
          {getAvailableThemes().map((preset) => (
            <option key={preset} value={preset}>
              {getThemeDisplayName(preset)}
            </option>
          ))}
        </select>
        <div className="ml-auto connection-status" data-status={connectionStatus}>
          <span className="connection-dot" data-status={connectionStatus} />
          <span>{statusText}</span>
        </div>
      </div>
    </header>
  );
}
