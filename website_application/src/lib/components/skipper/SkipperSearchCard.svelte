<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";

  interface SearchResult {
    title?: string;
    Title?: string;
    subject?: string;
    url?: string;
    URL?: string;
    snippet?: string;
    Snippet?: string;
    content?: string;
    Content?: string;
    matched_snippet?: string;
    score?: number;
    Score?: number;
    similarity?: number;
    Similarity?: number;
    relevance?: string;
    status?: string;
    created_at?: string;
  }

  interface Props {
    toolName: string;
    payload: Record<string, unknown>;
  }

  let { toolName, payload }: Props = $props();

  const isWeb = toolName === "search_web";
  const isSupport = toolName === "search_support_history";
  const BookIcon = getIconComponent("BookOpen");
  const GlobeIcon = getIconComponent("Globe");
  const MessageCircleIcon = getIconComponent("MessageCircle");

  const results: SearchResult[] = (() => {
    if (Array.isArray(payload.results)) return payload.results as SearchResult[];
    if (Array.isArray(payload.Results)) return payload.Results as SearchResult[];
    if (Array.isArray(payload)) return payload as unknown as SearchResult[];
    return [];
  })();

  function getTitle(r: SearchResult): string {
    return r.title || r.Title || r.subject || "Untitled";
  }

  function getUrl(r: SearchResult): string {
    return r.url || r.URL || "";
  }

  function getSnippet(r: SearchResult): string {
    const raw = r.snippet || r.Snippet || r.matched_snippet || r.content || r.Content || "";
    if (raw.length > 200) return raw.slice(0, 200) + "...";
    return raw;
  }

  function getScore(r: SearchResult): string | null {
    if (r.relevance) return r.relevance;
    const val = r.score ?? r.Score ?? r.similarity ?? r.Similarity ?? null;
    if (typeof val === "number") return val.toFixed(2);
    return null;
  }

  function getBadge(r: SearchResult): string | null {
    return r.status || null;
  }
</script>

<div class="rounded-lg border border-border bg-card text-sm">
  <div class="flex items-center gap-2 border-b border-border px-4 py-2.5">
    {#if isSupport}
      <MessageCircleIcon class="h-4 w-4 text-muted-foreground" />
      <span class="font-semibold text-foreground">Support History</span>
    {:else if isWeb}
      <GlobeIcon class="h-4 w-4 text-muted-foreground" />
      <span class="font-semibold text-foreground">Web Search</span>
    {:else}
      <BookIcon class="h-4 w-4 text-muted-foreground" />
      <span class="font-semibold text-foreground">Knowledge Base</span>
    {/if}
    {#if results.length > 0}
      <span
        class="rounded-full border border-border bg-muted/40 px-2 py-0.5 text-[10px] text-muted-foreground"
      >
        {results.length} result{results.length === 1 ? "" : "s"}
      </span>
    {/if}
  </div>

  {#if results.length === 0}
    <div class="px-4 py-3 text-xs text-muted-foreground">No results found.</div>
  {:else}
    <div class="divide-y divide-border">
      {#each results as result, i (i)}
        <div class="px-4 py-2.5">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="text-[10px] text-muted-foreground/50">{i + 1}.</span>
                {#if getUrl(result)}
                  <a
                    href={getUrl(result)}
                    target="_blank"
                    rel="external noreferrer"
                    class="truncate text-xs font-medium text-primary underline underline-offset-4 hover:text-primary/80"
                  >
                    {getTitle(result)}
                  </a>
                {:else}
                  <span class="truncate text-xs font-medium text-foreground">
                    {getTitle(result)}
                  </span>
                {/if}
                {#if getBadge(result)}
                  <span
                    class="rounded-full border border-border bg-muted/40 px-1.5 py-0.5 text-[9px] text-muted-foreground"
                  >
                    {getBadge(result)}
                  </span>
                {/if}
              </div>
              {#if getUrl(result)}
                <div class="mt-0.5 truncate text-[10px] text-muted-foreground/60">
                  {getUrl(result)}
                </div>
              {/if}
              {#if getSnippet(result)}
                <p class="mt-1 text-xs leading-relaxed text-muted-foreground">
                  {getSnippet(result)}
                </p>
              {/if}
            </div>
            {#if getScore(result) !== null}
              <span
                class="shrink-0 rounded border border-border bg-muted/40 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
              >
                {getScore(result)}
              </span>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>
