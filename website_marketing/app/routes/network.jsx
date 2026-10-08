import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { NetworkMap } from "../../src/components/marketing/network/NetworkMap";
import {
  networkTabKey,
  networkView,
  networkViewURL,
} from "../../../packages/map-core/network-explorer";
import config from "../../src/config";
import "maplibre-gl/dist/maplibre-gl.css";

export function meta() {
  return [
    { title: "Network explorer | FrameWorks" },
    {
      name: "description",
      content:
        "Explore FrameWorks topology and Livepeer discovery coverage, regional transcoding performance and pricing.",
    },
  ];
}

function LivepeerView({ viewport, onQueryChange }) {
  const container = useRef(null);
  useEffect(() => {
    let disposed = false;
    let stop;
    import("../../../packages/map-core/network-dashboard").then(({ mountNetworkDashboard }) => {
      if (!disposed && container.current) {
        stop = mountNetworkDashboard(container.current, {
          apiURL: new URL("/v1/network/livepeer", config.gatewayUrl).toString(),
          graphqlURL: config.gatewayUrl,
          viewport,
          onQueryChange,
        });
      }
    });
    return () => {
      disposed = true;
      stop?.();
    };
  }, [viewport, onQueryChange]);
  return <div ref={container} />;
}

export default function NetworkPage() {
  const [search] = useSearchParams();
  const navigate = useNavigate();
  const [viewport] = useState(() => ({}));
  const view = networkView(search);
  const onQueryChange = useCallback(
    (query) => {
      navigate(`/network?${query}`, { replace: true, preventScrollReset: true });
    },
    [navigate]
  );
  const choose = (next) => navigate(networkViewURL(search, next), { preventScrollReset: true });
  const onTabKey = (event) => {
    const next = networkTabKey(view, event.key);
    if (!next) return;
    event.preventDefault();
    choose(next);
    document.getElementById(`network-tab-${next}`)?.focus();
  };
  return (
    <div className="pt-24 pb-12 px-4 mx-auto max-w-7xl">
      <h1 className="text-3xl font-semibold mb-3">Network explorer</h1>
      <p className="text-muted-foreground mb-6">
        Explore our infrastructure and the Livepeer compute network.
      </p>
      <div role="tablist" aria-label="Network view" className="flex border border-border mb-6">
        {[
          ["frameworks", "FrameWorks"],
          ["livepeer", "Livepeer"],
        ].map(([key, label]) => (
          <button
            key={key}
            type="button"
            role="tab"
            id={`network-tab-${key}`}
            aria-selected={view === key}
            aria-controls="network-panel"
            tabIndex={view === key ? 0 : -1}
            onKeyDown={onTabKey}
            onClick={() => choose(key)}
            className={`flex-1 px-4 py-3 border-r border-border last:border-r-0 ${view === key ? "bg-brand-surface text-foreground" : "text-muted-foreground"}`}
          >
            {label}
          </button>
        ))}
      </div>
      <div role="tabpanel" id="network-panel" aria-labelledby={`network-tab-${view}`} tabIndex={0}>
        {view === "frameworks" ? (
          <NetworkMap viewport={viewport} />
        ) : (
          <LivepeerView viewport={viewport} onQueryChange={onQueryChange} />
        )}
      </div>
    </div>
  );
}
