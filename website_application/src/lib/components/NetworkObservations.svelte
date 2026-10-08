<script lang="ts">
  import { onMount } from "svelte";
  import { getGraphqlHttpUrl } from "$lib/config";
  import { getAccessToken } from "$lib/stores/token";
  import "maplibre-gl/dist/maplibre-gl.css";
  import type { ExplorerViewport } from "../../../../packages/map-core/network-explorer";
  let container: HTMLDivElement;
  let {
    diagnostics = false,
    viewport,
    onQueryChange,
  }: {
    diagnostics?: boolean;
    viewport?: ExplorerViewport;
    onQueryChange?: (query: URLSearchParams) => void;
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
          apiURL: new URL(
            diagnostics ? "/v1/network/livepeer/diagnostics" : "/v1/network/livepeer",
            getGraphqlHttpUrl() || window.location.origin
          ).toString(),
          headers: diagnostics
            ? () => ({ Authorization: `Bearer ${getAccessToken() ?? ""}` })
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
