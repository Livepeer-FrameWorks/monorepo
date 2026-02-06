<script lang="ts">
  export type SkipperConfidence = "verified" | "sourced" | "best_guess" | "unknown";

  export interface SkipperCitation {
    label: string;
    url: string;
  }

  export interface SkipperDetail {
    title?: string;
    payload: string | Record<string, unknown>;
  }

  export interface SkipperChatMessage {
    id: string;
    role: "user" | "assistant";
    content: string;
    confidence?: SkipperConfidence;
    citations?: SkipperCitation[];
    externalLinks?: SkipperCitation[];
    details?: SkipperDetail[];
  }

  interface Props {
    message: SkipperChatMessage;
  }

  let { message }: Props = $props();

  const confidenceLabels: Record<SkipperConfidence, string> = {
    verified: "Verified",
    sourced: "Sourced",
    best_guess: "Best guess",
    unknown: "Unknown",
  };

  function escapeHtml(value: string) {
    return value
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#039;");
  }

  function renderMarkdown(value: string) {
    const blocks: string[] = [];
    let working = value.replace(/```([\s\S]*?)```/g, (_match, code) => {
      const index = blocks.length;
      blocks.push(
        `<pre class=\"mt-3 overflow-x-auto rounded-md border border-border bg-muted/40 p-3 text-xs text-foreground\"><code>${escapeHtml(
          code.trim()
        )}</code></pre>`
      );
      return `__BLOCK_${index}__`;
    });

    working = escapeHtml(working);
    working = working.replace(
      /`([^`]+)`/g,
      '<code class="rounded bg-muted/60 px-1 py-0.5 text-xs">$1</code>'
    );
    working = working.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    working = working.replace(/\*([^*]+)\*/g, "<em>$1</em>");
    working = working.replace(
      /\[([^\]]+)\]\((https?:\/\/[^)]+)\)/g,
      '<a class="text-primary underline underline-offset-4 hover:text-primary/80" href="$2" target="_blank" rel="noreferrer">$1</a>'
    );
    working = working.replace(/\n/g, "<br />");

    blocks.forEach((block, index) => {
      working = working.replace(`__BLOCK_${index}__`, block);
    });

    return working;
  }

  function formatDetail(detail: SkipperDetail) {
    if (typeof detail.payload === "string") return detail.payload;
    return JSON.stringify(detail.payload, null, 2);
  }
</script>

<div class="flex flex-col gap-2" data-message-id={message.id}>
  <div class="flex items-center gap-2 text-xs uppercase tracking-[0.2em] text-muted-foreground">
    <span class="font-semibold">{message.role === "assistant" ? "Skipper" : "You"}</span>
    {#if message.role === "assistant" && message.confidence}
      <span
        class="rounded-full border border-border bg-muted/40 px-2 py-0.5 text-[10px] tracking-[0.16em]"
      >
        {confidenceLabels[message.confidence]}
      </span>
    {/if}
    {#if message.role === "assistant" && message.confidence === "sourced"}
      <span
        class="rounded-full border border-primary/40 bg-primary/10 px-2 py-0.5 text-[10px] font-semibold text-primary"
      >
        External
      </span>
    {/if}
  </div>

  {#if message.role === "assistant" && message.confidence === "best_guess"}
    <div class="rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-xs text-warning">
      Best guess — verify with primary data before acting.
    </div>
  {/if}

  {#if message.role === "assistant" && message.confidence === "unknown"}
    <div
      class="rounded-md border border-muted-foreground/30 bg-muted/50 px-3 py-2 text-xs text-muted-foreground"
    >
      I could not validate a confident answer based on available data.
    </div>
  {/if}

  <div
    class={message.role === "assistant"
      ? "rounded-xl border border-border bg-card px-4 py-3 text-sm text-foreground"
      : "rounded-xl bg-primary px-4 py-3 text-sm text-primary-foreground"}
  >
    <div class={message.confidence === "best_guess" ? "opacity-80" : "opacity-100"}>
      <div class="prose prose-sm max-w-none text-inherit prose-a:text-primary">
        <!-- eslint-disable-next-line svelte/no-at-html-tags -- renderMarkdown escapes input -->
        {@html renderMarkdown(message.content)}
      </div>
    </div>
  </div>

  {#if message.role === "assistant" && message.citations?.length}
    <div
      class="rounded-md border border-border bg-muted/30 px-3 py-2 text-xs text-muted-foreground"
    >
      <div class="font-semibold uppercase tracking-[0.16em] text-[10px]">Citations</div>
      <ul class="mt-2 space-y-1">
        {#each message.citations as citation (citation.url)}
          <li>
            <a
              class="text-primary underline underline-offset-4"
              href={citation.url}
              target="_blank"
              rel="external noreferrer"
            >
              {citation.label}
            </a>
          </li>
        {/each}
      </ul>
    </div>
  {/if}

  {#if message.role === "assistant" && message.externalLinks?.length}
    <div
      class="rounded-md border border-border bg-muted/30 px-3 py-2 text-xs text-muted-foreground"
    >
      <div class="font-semibold uppercase tracking-[0.16em] text-[10px]">External sources</div>
      <ul class="mt-2 space-y-1">
        {#each message.externalLinks as link (link.url)}
          <li>
            <a
              class="text-primary underline underline-offset-4"
              href={link.url}
              target="_blank"
              rel="external noreferrer"
            >
              {link.label}
            </a>
          </li>
        {/each}
      </ul>
    </div>
  {/if}

  {#if message.role === "assistant" && message.details?.length}
    <details
      class="rounded-md border border-border bg-muted/40 px-3 py-2 text-xs text-muted-foreground"
    >
      <summary class="cursor-pointer font-semibold uppercase tracking-[0.16em] text-[10px]">
        Details
      </summary>
      <div class="mt-2 space-y-2">
        {#each message.details as detail, i (i)}
          <div>
            {#if detail.title}
              <div class="font-semibold text-foreground">{detail.title}</div>
            {/if}
            <pre
              class="mt-1 whitespace-pre-wrap rounded-md border border-border bg-background/80 p-2 text-[11px] text-foreground">
{formatDetail(detail)}
            </pre>
          </div>
        {/each}
      </div>
    </details>
  {/if}
</div>
