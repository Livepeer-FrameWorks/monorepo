<script lang="ts">
  import { page } from "$app/stores";
  import { resolve } from "$app/paths";
  import { goto } from "$app/navigation";
  import { ArrowLeft, ExternalLink, ShieldAlert, PlayCircle, AlertCircle } from "lucide-svelte";
  import { Button } from "$lib/components/ui/button";
  import { getDocsSiteUrl } from "$lib/config";
  import {
    findFeature,
    SURFACE_KEYS,
    surfaceCell,
    type FeatureStatus,
    type SurfaceKey,
  } from "$lib/features";

  const slug = $derived($page.params.slug ?? "");
  const feature = $derived(findFeature(slug));

  const STATUS_CLASS: Record<FeatureStatus, string> = {
    shipped: "bg-success/15 text-success",
    partial: "bg-warning/15 text-warning",
    gap: "bg-destructive/15 text-destructive",
    roadmap: "bg-muted text-muted-foreground",
  };

  const STATUS_LABEL: Record<FeatureStatus, string> = {
    shipped: "Available",
    partial: "Expanding",
    gap: "Not available",
    roadmap: "Planned",
  };

  const SURFACE_LABEL: Record<SurfaceKey, string> = {
    graphql: "GraphQL API",
    mcp: "Agent tools",
    webapp: "Dashboard",
    docs: "Docs",
  };

  const docsBase = getDocsSiteUrl().replace(/\/$/, "");
  const SCHEME_URL_RE = /^[a-z][a-z0-9+.-]*:/i;

  function stripMdxSuffix(path: string) {
    return path.replace(/\.mdx?(?=([?#]|$))/i, "");
  }

  function docsPageHref(page: string) {
    const cleanPage = stripMdxSuffix(page.trim());
    if (SCHEME_URL_RE.test(cleanPage)) return cleanPage;
    return `${docsBase}/${cleanPage.replace(/^\/+/, "")}`;
  }

  function tryInPlayground(query: string) {
    const base = resolve("/developer/playground");
    // eslint-disable-next-line svelte/no-navigation-without-resolve -- base is already resolve()'d above; we're appending a query string
    goto(`${base}?query=${encodeURIComponent(query)}`);
  }

  function formatConfigLabel(key: string) {
    return key.replace(/_/g, " ");
  }

  function formatConfigValue(value: unknown) {
    if (value === true) return "Yes";
    if (value === false) return "No";
    if (value === "partial") return "Partial";
    return String(value);
  }
</script>

<svelte:head>
  <title>{feature?.name ?? "Feature"} - FrameWorks</title>
</svelte:head>

<div class="px-4 sm:px-6 lg:px-8 py-6 max-w-screen-xl mx-auto">
  <a
    href={resolve("/developer/features")}
    class="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-primary mb-4"
  >
    <ArrowLeft class="w-4 h-4" /> Back to features
  </a>

  {#if !feature}
    <div class="border border-border rounded-md p-8 text-center">
      <AlertCircle class="w-8 h-8 text-muted-foreground mx-auto mb-3" />
      <h2 class="text-lg font-semibold mb-1">Feature not found</h2>
      <p class="text-sm text-muted-foreground">
        No capability matches <code class="font-mono">{slug}</code>.
      </p>
    </div>
  {:else}
    <header class="mb-6">
      <div class="flex items-center gap-3 mb-2">
        <h1 class="text-2xl font-bold">{feature.name}</h1>
        <span class="text-xs px-1.5 py-0.5 rounded {STATUS_CLASS[feature.status]}">
          {STATUS_LABEL[feature.status]}
        </span>
        <span class="text-xs text-muted-foreground">— {feature.area}</span>
      </div>
      {#if feature.description}
        <p class="text-sm text-muted-foreground max-w-3xl">{feature.description}</p>
      {/if}
    </header>

    <!-- Surfaces -->
    <section class="grid grid-cols-1 md:grid-cols-2 gap-4 mb-8">
      {#each SURFACE_KEYS as s (s)}
        {@const cell = surfaceCell(feature, s)}
        <div class="border border-border rounded-md p-4">
          <div class="flex items-center justify-between mb-2">
            <h3 class="font-medium">{SURFACE_LABEL[s]}</h3>
            <span
              class="text-xs px-1.5 py-0.5 rounded {cell.required
                ? cell.filled
                  ? 'bg-success/15 text-success'
                  : 'bg-destructive/15 text-destructive'
                : 'bg-muted/40 text-muted-foreground'}"
            >
              {cell.required ? (cell.filled ? "Available" : "Not available") : "Not required"}
            </span>
          </div>
          {#if !cell.required && cell.reason}
            <p class="text-xs text-muted-foreground italic mb-2">{cell.reason}</p>
          {/if}
          {#if cell.sensitive}
            <p class="text-xs text-warning flex items-center gap-1 mb-2">
              <ShieldAlert class="w-3 h-3" /> Handles sensitive credentials or security-sensitive configuration
            </p>
          {/if}

          {#if s === "graphql"}
            {@const g = feature.surfaces.graphql}
            {#if g.mutations?.length}
              <div class="text-xs mb-2">
                <div class="text-muted-foreground mb-0.5">Mutations</div>
                <div class="font-mono space-y-0.5">
                  {#each g.mutations as m (m)}<div>{m}</div>{/each}
                </div>
              </div>
            {/if}
            {#if g.queries?.length}
              <div class="text-xs mb-2">
                <div class="text-muted-foreground mb-0.5">Queries</div>
                <div class="font-mono space-y-0.5">
                  {#each g.queries as q (q)}<div>{q}</div>{/each}
                </div>
              </div>
            {/if}
            {#if g.subscriptions?.length}
              <div class="text-xs">
                <div class="text-muted-foreground mb-0.5">Subscriptions</div>
                <div class="font-mono space-y-0.5">
                  {#each g.subscriptions as sub (sub)}<div>{sub}</div>{/each}
                </div>
              </div>
            {/if}
            {#if g.fields?.length}
              <div class="text-xs">
                <div class="text-muted-foreground mb-0.5">Fields</div>
                <div class="font-mono space-y-0.5">
                  {#each g.fields as field (field)}<div>{field}</div>{/each}
                </div>
              </div>
            {/if}
          {:else if s === "mcp"}
            {#if feature.surfaces.mcp.tools?.length}
              <div class="text-xs font-mono space-y-0.5">
                {#each feature.surfaces.mcp.tools as t (t)}<div>{t}</div>{/each}
              </div>
            {/if}
          {:else if s === "webapp"}
            {#if feature.surfaces.webapp.routes?.length}
              <div class="text-xs font-mono space-y-0.5">
                {#each feature.surfaces.webapp.routes as r (r)}<div>{r}</div>{/each}
              </div>
            {/if}
          {:else if s === "docs"}
            {#if feature.surfaces.docs.pages?.length}
              <div class="text-xs space-y-0.5">
                {#each feature.surfaces.docs.pages as p (p)}
                  <!-- eslint-disable svelte/no-navigation-without-resolve -->
                  <a
                    href={docsPageHref(p)}
                    target="_blank"
                    rel="noopener"
                    class="text-primary hover:underline inline-flex items-center gap-1"
                  >
                    {p}
                    <ExternalLink class="w-3 h-3" />
                  </a>
                  <!-- eslint-enable svelte/no-navigation-without-resolve -->
                {/each}
              </div>
            {/if}
          {/if}
        </div>
      {/each}
    </section>

    <!-- Configurability -->
    {#if feature.configurability}
      <section class="mb-8">
        <h3 class="text-xs uppercase tracking-wider text-muted-foreground mb-2">Configurability</h3>
        <div class="border border-border rounded-md p-4">
          <dl class="grid grid-cols-2 sm:grid-cols-3 gap-x-4 gap-y-2 text-xs">
            {#each Object.entries(feature.configurability) as [key, val] (key)}
              <div>
                <dt class="text-muted-foreground">{formatConfigLabel(key)}</dt>
                <dd class="font-mono">{formatConfigValue(val)}</dd>
              </div>
            {/each}
          </dl>
        </div>
      </section>
    {/if}

    <!-- Examples -->
    {#if feature.examples?.length}
      <section class="mb-8">
        <h3 class="text-xs uppercase tracking-wider text-muted-foreground mb-2">
          Try in playground
        </h3>
        <div class="space-y-3">
          {#each feature.examples as ex (ex.title)}
            <div class="border border-border rounded-md overflow-hidden">
              <div class="flex items-center justify-between px-3 py-2 bg-muted/40">
                <div class="text-sm font-medium">{ex.title}</div>
                <Button size="sm" variant="outline" onclick={() => tryInPlayground(ex.query)}>
                  <PlayCircle class="w-4 h-4 mr-1" /> Open in playground
                </Button>
              </div>
              <pre class="text-xs font-mono p-3 overflow-x-auto"><code>{ex.query}</code></pre>
            </div>
          {/each}
        </div>
      </section>
    {/if}
  {/if}
</div>
