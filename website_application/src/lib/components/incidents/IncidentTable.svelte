<script lang="ts">
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import {
    formatIncidentTime,
    incidentSeverityClass,
    incidentStatusClass,
    incidentStatusLabel,
    type IncidentRow,
  } from "$lib/incidents";
  import { getIconComponent } from "$lib/iconUtils";

  const ArrowRightIcon = getIconComponent("ArrowRight");

  let {
    incidents,
    hrefFor,
    showScope = false,
    clusterName = (clusterId: string) => clusterId,
  }: {
    incidents: readonly IncidentRow[];
    hrefFor: (id: string) => string;
    showScope?: boolean;
    clusterName?: (clusterId: string) => string;
  } = $props();
</script>

<!-- hrefFor is built with resolve() by the page. -->
<!-- eslint-disable svelte/no-navigation-without-resolve -->
<div class="overflow-x-auto">
  <Table>
    <TableHeader>
      <TableRow>
        <TableHead>Status</TableHead>
        <TableHead>Severity</TableHead>
        <TableHead>Incident</TableHead>
        {#if showScope}
          <TableHead>Scope</TableHead>
          <TableHead>Tenant</TableHead>
        {/if}
        <TableHead>Cluster</TableHead>
        <TableHead>Started</TableHead>
        <TableHead>Last alert</TableHead>
        <TableHead class="text-right">Firing alerts</TableHead>
        <TableHead class="text-right">Action</TableHead>
      </TableRow>
    </TableHeader>
    <TableBody>
      {#each incidents as incident (incident.id)}
        <TableRow>
          <TableCell>
            <Badge variant="outline" class="uppercase {incidentStatusClass(incident.status)}">
              {incidentStatusLabel(incident.status)}
            </Badge>
          </TableCell>
          <TableCell>
            <Badge variant="outline" class="uppercase {incidentSeverityClass(incident.severity)}">
              {incident.severity}
            </Badge>
          </TableCell>
          <TableCell class="min-w-[14rem]">
            <a class="font-medium text-foreground hover:underline" href={hrefFor(incident.id)}>
              {incident.title}
            </a>
            <div class="text-xs font-mono text-muted-foreground">{incident.alertname}</div>
          </TableCell>
          {#if showScope}
            <TableCell>
              <Badge variant="outline"
                >{incident.scope === "PLATFORM" ? "Platform" : "Tenant"}</Badge
              >
            </TableCell>
            <TableCell class="font-mono text-xs">{incident.tenantId ?? "—"}</TableCell>
          {/if}
          <TableCell>
            {#if incident.clusterId}
              <span title={incident.clusterId}>{clusterName(incident.clusterId)}</span>
            {:else}
              <span class="text-muted-foreground">—</span>
            {/if}
            {#if incident.region}
              <div class="text-xs text-muted-foreground">{incident.region}</div>
            {/if}
          </TableCell>
          <TableCell class="whitespace-nowrap">{formatIncidentTime(incident.startedAt)}</TableCell>
          <TableCell class="whitespace-nowrap">{formatIncidentTime(incident.lastAlertAt)}</TableCell
          >
          <TableCell class="text-right">{incident.firingAlertCount}</TableCell>
          <TableCell class="text-right">
            <Button
              variant="outline"
              size="sm"
              href={hrefFor(incident.id)}
              aria-label={`View incident: ${incident.title}`}
            >
              View incident
              <ArrowRightIcon class="size-4" aria-hidden="true" />
            </Button>
          </TableCell>
        </TableRow>
      {/each}
    </TableBody>
  </Table>
</div>
<!-- eslint-enable svelte/no-navigation-without-resolve -->
