<script lang="ts">
  import { resolve } from "$app/paths";
  import { LayoutGrid, ShieldAlert } from "lucide-svelte";
  import {
    featuresByArea,
    flattenFeatures,
    PILLAR_ORDER,
    pillarLabel,
    SURFACE_KEYS,
    surfaceCell,
    type Feature,
    type FeatureStatus,
    type SurfaceKey,
  } from "$lib/features";

  // Families and subitems render in curated registry order, grouped by pillar.
  const grouped = featuresByArea();
  const areas = PILLAR_ORDER.filter((p) => grouped[p]?.length);

  const allFeatures = flattenFeatures();
  const totalFeatures = allFeatures.length;
  const shippedCount = allFeatures.filter((f) => f.status === "shipped").length;
  const partialCount = allFeatures.filter((f) => f.status === "partial").length;
  const gapCount = allFeatures.filter((f) => f.status === "gap").length;
  const roadmapCount = allFeatures.filter((f) => f.status === "roadmap").length;

  const SURFACE_LABEL: Record<SurfaceKey, string> = {
    graphql: "API",
    mcp: "MCP",
    cli: "CLI",
    webapp: "App",
    docs: "Docs",
  };

  const STATUS_LABEL: Record<FeatureStatus, string> = {
    shipped: "Available",
    partial: "Expanding",
    gap: "Not available",
    roadmap: "Planned",
  };

  const STATUS_CLASS: Record<FeatureStatus, string> = {
    shipped: "bg-success/15 text-success",
    partial: "bg-warning/15 text-warning",
    gap: "bg-destructive/15 text-destructive",
    roadmap: "bg-muted text-muted-foreground",
  };

  // A family row with no surfaces of its own still counts as covered when its
  // subitems fill the surface (family_surfaces is the generator-computed union).
  function viaSubitems(f: Feature, surface: SurfaceKey): boolean {
    return !(f.actual_surfaces[surface] ?? false) && (f.family_surfaces?.[surface] ?? false);
  }

  function cellClass(f: Feature, surface: SurfaceKey): string {
    const c = surfaceCell(f, surface);
    if (f.status === "roadmap" || !c.required) return "bg-muted/40 text-muted-foreground";
    if (c.filled) return "bg-success/15 text-success";
    if (viaSubitems(f, surface)) return "bg-success/10 text-success/70";
    return "bg-destructive/15 text-destructive";
  }

  function cellGlyph(f: Feature, surface: SurfaceKey): string {
    const c = surfaceCell(f, surface);
    if (f.status === "roadmap" || !c.required) return "—";
    if (c.filled || viaSubitems(f, surface)) return "✓";
    return "✗";
  }

  function cellTitle(f: Feature, surface: SurfaceKey): string {
    const c = surfaceCell(f, surface);
    if (f.status === "roadmap") return "planned";
    if (!c.required) return c.reason ? `not required — ${c.reason}` : "not required";
    if (c.filled) return c.sensitive ? "available; sensitive operation" : "available";
    if (viaSubitems(f, surface)) return "available via subitems";
    return "not available";
  }
</script>

<svelte:head>
  <title>Platform Features - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <div class="px-4 sm:px-6 lg:px-8 py-3 border-b border-border shrink-0">
    <div class="flex justify-between items-start gap-4">
      <div>
        <div class="flex items-center gap-3 mb-1">
          <LayoutGrid class="w-5 h-5 text-primary" />
          <h1 class="text-lg font-bold text-foreground">Platform features</h1>
        </div>
        <p class="text-sm text-muted-foreground max-w-3xl">
          Browse FrameWorks capabilities across the GraphQL API, agent tools, dashboard workflows,
          and docs. Open any feature for details — some include GraphQL examples you can run in the
          playground.
        </p>
      </div>
      <div class="flex flex-wrap gap-3 text-xs text-muted-foreground shrink-0 pt-1">
        <span
          ><strong class="text-foreground">{shippedCount}</strong>/{totalFeatures} available</span
        >
        <span>{partialCount} expanding</span>
        <span>{gapCount} unavailable</span>
        <span>{roadmapCount} planned</span>
      </div>
    </div>
  </div>

  <div class="flex-1 overflow-y-auto">
    <div class="px-4 sm:px-6 lg:px-8 py-6 max-w-screen-xl mx-auto">
      {#each areas as area (area)}
        <section class="mb-8">
          <h2 class="text-xs uppercase tracking-wider text-muted-foreground mb-2">
            {pillarLabel(area)}
          </h2>
          <div class="border border-border rounded-md overflow-hidden">
            <table class="w-full text-sm">
              <thead class="bg-muted/50 text-xs text-muted-foreground">
                <tr>
                  <th class="text-left px-3 py-2 font-medium">Feature</th>
                  <th class="text-left px-3 py-2 font-medium w-24">Status</th>
                  {#each SURFACE_KEYS as s (s)}
                    <th class="text-center px-2 py-2 font-medium w-14">{SURFACE_LABEL[s]}</th>
                  {/each}
                  <th class="text-left px-3 py-2 font-medium">What it unlocks</th>
                </tr>
              </thead>
              <tbody>
                {#each grouped[area].flatMap( (fam) => [{ f: fam, child: false }, ...(fam.subitems ?? []).map( (s) => ({ f: s, child: true }) )] ) as { f, child } (f.slug)}
                  <tr class="border-t border-border hover:bg-accent/30">
                    <td class="px-3 py-2" class:pl-8={child}>
                      <a
                        href={resolve(`/developer/features/${f.slug}`)}
                        class="text-primary hover:underline {child ? '' : 'font-medium'}"
                      >
                        {#if child}<span class="text-muted-foreground mr-1" aria-hidden="true"
                            >↳</span
                          >{/if}{f.name}
                      </a>
                      {#if f.kind === "foundation"}
                        <span
                          class="text-[10px] uppercase tracking-wide px-1 py-0.5 rounded bg-muted text-muted-foreground ml-1"
                          >Foundation</span
                        >
                      {/if}
                    </td>
                    <td class="px-3 py-2">
                      <span class="text-xs px-1.5 py-0.5 rounded {STATUS_CLASS[f.status]}">
                        {STATUS_LABEL[f.status]}
                      </span>
                    </td>
                    {#each SURFACE_KEYS as s (s)}
                      <td class="px-2 py-2 text-center">
                        <span
                          class="inline-flex items-center justify-center w-7 h-6 text-xs font-mono rounded {cellClass(
                            f,
                            s
                          )}"
                          title={cellTitle(f, s)}
                        >
                          {cellGlyph(f, s)}
                        </span>
                        {#if surfaceCell(f, s).sensitive}
                          <ShieldAlert
                            class="inline-block w-3 h-3 text-warning ml-0.5"
                            aria-label="sensitive"
                          />
                        {/if}
                      </td>
                    {/each}
                    <td class="px-3 py-2 text-xs text-muted-foreground">
                      {f.description ?? ""}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </section>
      {/each}

      <footer class="text-xs text-muted-foreground mt-8 pb-4">
        Planned items are included for orientation; available and expanding rows link to live
        platform surfaces.
      </footer>
    </div>
  </div>
</div>
