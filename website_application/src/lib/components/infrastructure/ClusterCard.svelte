<script lang="ts">
  import { Card, CardContent } from "$lib/components/ui/card";
  import { Badge } from "$lib/components/ui/badge";

  interface Cluster {
    id?: string;
    name: string;
    region: string;
    status: string;
    nodes?: unknown[];
    createdAt: string;
  }

  interface Props {
    cluster: Cluster;
    getStatusBadgeClass: (status: string | null | undefined) => string;
  }

  let { cluster, getStatusBadgeClass }: Props = $props();
</script>

<Card>
  <CardContent class="space-y-3">
    <div class="flex items-start justify-between gap-3">
      <div>
        <h3 class="text-lg font-semibold">{cluster.name}</h3>
        <p class="text-sm text-tokyo-night-comment">
          {cluster.region}
        </p>
      </div>
      <Badge
        variant="outline"
        class="text-xs uppercase {getStatusBadgeClass(cluster.status)}"
      >
        {cluster.status}
      </Badge>
    </div>
    <div class="space-y-1 text-sm text-tokyo-night-comment">
      <p>Nodes: {cluster.nodes?.length || 0}</p>
      <p>Created: {new Date(cluster.createdAt).toLocaleDateString()}</p>
    </div>
  </CardContent>
</Card>
