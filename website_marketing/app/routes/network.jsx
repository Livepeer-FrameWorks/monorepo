import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { NetworkMap } from "../../src/components/marketing/network/NetworkMap";
import {
  networkAPIURL,
  networkExplorerActions,
  networkTabKey,
  networkView,
  networkViewURL,
} from "../../../packages/map-core/network-explorer";
import "../../../packages/map-core/network-explorer.css";
import config from "../../src/config";
import { signupUrl } from "../../src/lib/siteLinks";
import { baseMeta } from "../seo";
import "maplibre-gl/dist/maplibre-gl.css";

export function meta() {
  return baseMeta("network");
}

function LivepeerView({ viewport, onQueryChange }) {
  const container = useRef(null);
  useEffect(() => {
    let disposed = false;
    let stop;
    import("../../../packages/map-core/network-dashboard").then(({ mountNetworkDashboard }) => {
      if (!disposed && container.current) {
        stop = mountNetworkDashboard(container.current, {
          apiURL: networkAPIURL("/v1/network/livepeer", config.gatewayUrl, window.location.origin),
          graphqlURL: config.gatewayUrl,
          livepeerExplorerURL: config.livepeerExplorerUrl,
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

const explorerActions = networkExplorerActions({
  startStreaming: signupUrl(config.appUrl, "network-explorer"),
  docsURL: config.docsUrl,
  showcase: "/showcase",
});

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
    <div className="network-explorer">
      <header className="fw-explorer-heading">
        <h1>Network explorer</h1>
        <p>Infrastructure, compute and the paths between them.</p>
      </header>
      <div role="tablist" aria-label="Network view" className="fw-explorer-tabs">
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
            className="fw-explorer-tab"
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
      <nav aria-label="Next steps" className="fw-explorer-actions">
        {explorerActions.map((action) =>
          action.href.startsWith("/") ? (
            <Link key={action.id} to={action.href} className="fw-explorer-action">
              {action.label}
            </Link>
          ) : (
            <a key={action.id} href={action.href} className="fw-explorer-action">
              {action.label}
            </a>
          )
        )}
      </nav>
    </div>
  );
}
