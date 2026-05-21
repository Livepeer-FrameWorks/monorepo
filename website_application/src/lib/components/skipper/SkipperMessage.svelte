<script lang="ts">
  import { format } from "date-fns";
  import SkipperToolResult from "./SkipperToolResult.svelte";
  import { renderSkipperMarkdown } from "$lib/utils/skipperMarkdown";

  export type SkipperConfidence = "verified" | "sourced" | "best_guess" | "unknown";

  export interface SkipperCitation {
    label: string;
    url: string;
  }

  export interface SkipperDetail {
    title?: string;
    payload: string | Record<string, unknown>;
  }

  export interface SkipperConfidenceBlock {
    content: string;
    confidence: SkipperConfidence;
    sources?: SkipperCitation[];
  }

  export interface SkipperChatMessage {
    id: string;
    role: "user" | "assistant";
    content: string;
    confidence?: SkipperConfidence;
    citations?: SkipperCitation[];
    externalLinks?: SkipperCitation[];
    details?: SkipperDetail[];
    blocks?: SkipperConfidenceBlock[];
    createdAt?: string;
  }

  interface Props {
    message: SkipperChatMessage;
  }

  let { message }: Props = $props();

  const confidenceLabels: Record<SkipperConfidence, string> = {
    verified: "Verified",
    sourced: "Sourced",
    best_guess: "Best guess",
    unknown: "Unverified",
  };

  const confidenceTooltips: Record<SkipperConfidence, string> = {
    verified: "Answer grounded in indexed knowledge base content",
    sourced: "Answer includes external web sources",
    best_guess: "Inferred from general knowledge — verify before acting",
    unknown: "Could not validate against available data",
  };

  function isAbsoluteHttpUrl(value: string) {
    try {
      const url = new URL(value);
      return url.protocol === "http:" || url.protocol === "https:";
    } catch {
      return false;
    }
  }

  const citations = $derived(
    (message.citations ?? []).filter((item) => item.label || isAbsoluteHttpUrl(item.url))
  );
  const externalLinks = $derived(
    (message.externalLinks ?? []).filter((item) => isAbsoluteHttpUrl(item.url))
  );

  function handleClick(event: MouseEvent) {
    const target = event.target as HTMLElement;
    const copyButton = target.closest("[data-copy-index]") as HTMLElement | null;
    if (!copyButton) return;

    const pre = copyButton.parentElement?.querySelector("code");
    if (!pre) return;

    navigator.clipboard.writeText(pre.textContent ?? "");
  }
</script>

<div
  class="flex flex-col gap-2"
  role="presentation"
  data-message-id={message.id}
  onclick={handleClick}
>
  <div class="flex items-center gap-2 text-xs uppercase tracking-[0.2em] text-muted-foreground">
    <span class="font-semibold">{message.role === "assistant" ? "Skipper" : "You"}</span>
    {#if message.role === "assistant" && message.confidence}
      <span
        class="group/tip relative cursor-default rounded-full border border-border bg-muted/40 px-2 py-0.5 text-[10px] tracking-[0.16em]"
      >
        {confidenceLabels[message.confidence]}
        <span
          class="pointer-events-none absolute left-1/2 top-full z-50 mt-1.5 -translate-x-1/2 whitespace-nowrap rounded-md border border-border bg-popover px-2.5 py-1.5 text-[11px] normal-case tracking-normal text-popover-foreground shadow-md opacity-0 transition-opacity group-hover/tip:opacity-100"
        >
          {confidenceTooltips[message.confidence]}
        </span>
      </span>
    {/if}
    {#if message.role === "assistant" && message.confidence === "sourced"}
      <span
        class="rounded-full border border-primary/40 bg-primary/10 px-2 py-0.5 text-[10px] font-semibold text-primary"
      >
        External
      </span>
    {/if}
    {#if message.createdAt}
      <span class="ml-auto text-[10px] normal-case tracking-normal text-muted-foreground/60">
        {format(new Date(message.createdAt), "h:mm a")}
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
      Unverified — could not validate against available data.
    </div>
  {/if}

  <div
    class={message.role === "assistant"
      ? "rounded-xl border border-border bg-card px-4 py-3 text-sm text-foreground"
      : "rounded-xl bg-primary px-4 py-3 text-sm text-primary-foreground"}
  >
    {#if message.blocks && message.blocks.length > 1}
      {#each message.blocks as block, i (i)}
        {#if i > 0}
          <hr class="my-3 border-border/40" />
        {/if}
        <div>
          <div class="mb-1.5 flex items-center gap-2">
            <span
              class="group/tip relative cursor-default rounded-full border border-border bg-muted/40 px-2 py-0.5 text-[10px] tracking-[0.16em] uppercase text-muted-foreground"
            >
              {confidenceLabels[block.confidence]}
              <span
                class="pointer-events-none absolute left-1/2 top-full z-50 mt-1.5 -translate-x-1/2 whitespace-nowrap rounded-md border border-border bg-popover px-2.5 py-1.5 text-[11px] normal-case tracking-normal text-popover-foreground shadow-md opacity-0 transition-opacity group-hover/tip:opacity-100"
              >
                {confidenceTooltips[block.confidence]}
              </span>
            </span>
          </div>
          {#if block.confidence === "best_guess"}
            <div
              class="rounded-md border border-warning/40 bg-warning/10 px-3 py-1.5 text-xs text-warning mb-1.5"
            >
              Best guess — verify before acting.
            </div>
          {/if}
          <div class={block.confidence === "best_guess" ? "opacity-80" : "opacity-100"}>
            <div class="prose prose-sm max-w-none text-inherit prose-a:text-primary">
              <!-- eslint-disable-next-line svelte/no-at-html-tags -- renderMarkdown escapes input -->
              {@html renderSkipperMarkdown(block.content)}
            </div>
          </div>
        </div>
      {/each}
    {:else}
      <div class={message.confidence === "best_guess" ? "opacity-80" : "opacity-100"}>
        <div class="prose prose-sm max-w-none text-inherit prose-a:text-primary">
          <!-- eslint-disable-next-line svelte/no-at-html-tags -- renderMarkdown escapes input -->
          {@html renderSkipperMarkdown(message.content)}
        </div>
      </div>
    {/if}
  </div>

  {#if message.role === "assistant" && citations.length}
    <details
      class="rounded-md border border-border bg-muted/30 text-xs text-muted-foreground [&[open]>summary>.skipper-chevron]:rotate-90"
    >
      <summary
        class="flex cursor-pointer select-none items-center gap-2 px-3 py-2 font-semibold uppercase tracking-[0.16em] text-[10px]"
      >
        <svg
          class="skipper-chevron h-3 w-3 shrink-0 transition-transform"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2.5"
          stroke-linecap="round"
          stroke-linejoin="round"><path d="m9 18 6-6-6-6" /></svg
        >
        Citations
        <span
          class="rounded-full border border-border bg-muted/60 px-1.5 py-0.5 text-[9px] font-normal normal-case tracking-normal"
        >
          {citations.length}
        </span>
      </summary>
      <ul class="space-y-1 px-3 pb-2">
        {#each citations as citation, i (`${citation.url}-${i}`)}
          <li>
            {#if isAbsoluteHttpUrl(citation.url)}
              <a
                class="text-primary underline underline-offset-4"
                href={citation.url}
                target="_blank"
                rel="external noreferrer"
              >
                {citation.label || citation.url}
              </a>
            {:else}
              <span>{citation.label}</span>
            {/if}
          </li>
        {/each}
      </ul>
    </details>
  {/if}

  {#if message.role === "assistant" && externalLinks.length}
    <details
      class="rounded-md border border-border bg-muted/30 text-xs text-muted-foreground [&[open]>summary>.skipper-chevron]:rotate-90"
    >
      <summary
        class="flex cursor-pointer select-none items-center gap-2 px-3 py-2 font-semibold uppercase tracking-[0.16em] text-[10px]"
      >
        <svg
          class="skipper-chevron h-3 w-3 shrink-0 transition-transform"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2.5"
          stroke-linecap="round"
          stroke-linejoin="round"><path d="m9 18 6-6-6-6" /></svg
        >
        External sources
        <span
          class="rounded-full border border-border bg-muted/60 px-1.5 py-0.5 text-[9px] font-normal normal-case tracking-normal"
        >
          {externalLinks.length}
        </span>
      </summary>
      <ul class="space-y-1 px-3 pb-2">
        {#each externalLinks as link, i (`${link.url}-${i}`)}
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
    </details>
  {/if}

  {#if message.role === "assistant" && message.details?.length}
    <details
      class="rounded-md border border-border bg-muted/30 text-xs text-muted-foreground [&[open]>summary>.skipper-chevron]:rotate-90"
    >
      <summary
        class="flex cursor-pointer select-none items-center gap-2 px-3 py-2 font-semibold uppercase tracking-[0.16em] text-[10px]"
      >
        <svg
          class="skipper-chevron h-3 w-3 shrink-0 transition-transform"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2.5"
          stroke-linecap="round"
          stroke-linejoin="round"><path d="m9 18 6-6-6-6" /></svg
        >
        Tool details
        <span
          class="rounded-full border border-border bg-muted/60 px-1.5 py-0.5 text-[9px] font-normal normal-case tracking-normal"
        >
          {message.details.length}
        </span>
      </summary>
      <div class="space-y-2 px-3 pb-2">
        {#each message.details as detail, i (i)}
          <SkipperToolResult {detail} />
        {/each}
      </div>
    </details>
  {/if}
</div>
