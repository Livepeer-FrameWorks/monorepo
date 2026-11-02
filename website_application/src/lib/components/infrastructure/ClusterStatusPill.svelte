<script lang="ts">
  import { GetNodesConnectionStore } from "$houdini";
  import {
    clusterStatusClass,
    clusterStatusLabel,
    isUnknownClusterStatus,
  } from "$lib/infrastructure/cluster-status";

  interface Props {
    healthStatus: string | null | undefined;
    /**
     * Set for a cluster the tenant owns. Node lists are owner-only, so the
     * count behind "Waiting for first edge" is looked up only for these.
     */
    ownedClusterId?: string | null;
  }

  let { healthStatus, ownedClusterId = null }: Props = $props();

  let nodeCount = $state<number | null>(null);

  $effect(() => {
    const clusterId = ownedClusterId;
    nodeCount = null;
    if (!clusterId || !isUnknownClusterStatus(healthStatus)) return;
    let cancelled = false;
    new GetNodesConnectionStore()
      .fetch({ variables: { clusterId, first: 1 }, policy: "NetworkOnly" })
      .then((result) => {
        const count = result.data?.nodesConnection?.totalCount;
        if (!cancelled && typeof count === "number") nodeCount = count;
      })
      .catch(() => {
        // Without a count the raw status stays on the pill.
      });
    return () => {
      cancelled = true;
    };
  });
</script>

<span
  class="inline-flex items-center px-2 py-0.5 rounded border text-xs font-medium {clusterStatusClass(
    healthStatus
  )}"
  data-testid="cluster-status"
>
  {clusterStatusLabel(healthStatus, nodeCount)}
</span>
