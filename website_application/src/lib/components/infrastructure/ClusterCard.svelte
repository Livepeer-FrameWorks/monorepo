<script lang="ts">
  import { Card, CardContent } from "$lib/components/ui/card";
  import { Badge } from "$lib/components/ui/badge";

  // Minimal interface for cluster data that works with multiple query types
  interface ClusterData {
    readonly clusterId: string;
    readonly clusterName: string;
    readonly healthStatus: string;
    readonly createdAt: string | null;
  }

  interface Props {
    cluster: ClusterData;
    getStatusBadgeClass: (status: string | null | undefined) => string;
  }

  let { cluster, getStatusBadgeClass }: Props = $props();
</script>

<Card>
  <CardContent class="space-y-4">
    <div class="flex items-start justify-between">
      <div>
        <h3 class="text-lg font-semibold">{cluster.clusterName}</h3>
        <p class="text-sm text-muted-foreground">{cluster.clusterId}</p>
      </div>
      <Badge
        variant="outline"
        class="text-xs uppercase {getStatusBadgeClass(cluster.healthStatus)}"
      >
        {cluster.healthStatus}
      </Badge>
    </div>
    <div class="space-y-1 text-sm text-muted-foreground">
      <p>Created: {cluster.createdAt ? new Date(cluster.createdAt).toLocaleDateString() : "N/A"}</p>
    </div>
  </CardContent>
</Card>
