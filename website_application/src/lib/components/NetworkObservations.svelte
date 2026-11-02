<script lang="ts">
  import { onMount } from "svelte";
  import { LIVEPEER_EXPLORER_URL } from "@frameworks/site-config";
  import { getGraphqlHttpUrl } from "$lib/config";
  import { getAccessToken } from "$lib/stores/token";
  import "maplibre-gl/dist/maplibre-gl.css";
  import {
    networkAPIURL,
    type ExplorerViewport,
  } from "../../../../packages/map-core/network-explorer";
  let container: HTMLDivElement;
  let {
    diagnostics = false,
    operator = false,
    viewport,
    onQueryChange,
    onDiagnosticsChange,
  }: {
    diagnostics?: boolean;
    operator?: boolean;
    viewport?: ExplorerViewport;
    onQueryChange?: (query: URLSearchParams) => void;
    onDiagnosticsChange?: (enabled: boolean) => void;
  } = $props();
  onMount(() => {
    let disposed = false;
    let stop: undefined | (() => void);
    import("../../../../packages/map-core/network-dashboard").then(({ mountNetworkDashboard }) => {
      if (!disposed) {
        stop = mountNetworkDashboard(container, {
          graphqlURL: getGraphqlHttpUrl(),
          viewport,
          onQueryChange,
          livepeerExplorerURL: LIVEPEER_EXPLORER_URL,
          apiURL: networkAPIURL(
            diagnostics ? "/v1/network/livepeer/diagnostics" : "/v1/network/livepeer",
            getGraphqlHttpUrl(),
            window.location.origin
          ),
          headers: diagnostics
            ? () => ({ Authorization: `Bearer ${getAccessToken() ?? ""}` })
            : undefined,
          operatorDiagnostics:
            operator && onDiagnosticsChange
              ? { enabled: diagnostics, onToggle: onDiagnosticsChange }
              : undefined,
        });
      }
    });
    return () => {
      disposed = true;
      stop?.();
    };
  });
</script>

<div bind:this={container}></div>
