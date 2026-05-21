<script lang="ts">
  import { renderSkipperMarkdown } from "../../lib/skipperMarkdown";

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
  }

  import DocsSkipperToolResult from "./DocsSkipperToolResult.svelte";

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

  function hasBlockSources(): boolean {
    return !!message.blocks?.some((block) => block.sources?.length);
  }

  function isAbsoluteHttpUrl(value: string) {
    try {
      const url = new URL(value);
      return url.protocol === "http:" || url.protocol === "https:";
    } catch {
      return false;
    }
  }

  function handleCopyClick(e: MouseEvent) {
    const btn = (e.target as HTMLElement).closest<HTMLButtonElement>("[data-copy-index]");
    if (!btn) return;
    const pre = btn.closest(".docs-skipper-message__code-wrap")?.querySelector("pre");
    if (!pre) return;
    navigator.clipboard.writeText(pre.textContent || "");
    btn.textContent = "Copied!";
    setTimeout(() => {
      btn.textContent = "Copy";
    }, 1500);
  }

  function formatDetail(detail: SkipperDetail) {
    if (typeof detail.payload === "string") return detail.payload;
    return JSON.stringify(detail.payload, null, 2);
  }
</script>

<div class="docs-skipper-message">
  <div class="docs-skipper-message__meta">
    <span class="docs-skipper-message__role">
      {message.role === "assistant" ? "Skipper" : "You"}
    </span>
    {#if message.role === "assistant" && message.confidence && message.content}
      <span class="docs-skipper-message__confidence docs-skipper-message__confidence--tip">
        {confidenceLabels[message.confidence]}
        <span class="docs-skipper-message__tooltip">{confidenceTooltips[message.confidence]}</span>
      </span>
    {/if}
    {#if message.role === "assistant" && message.confidence === "sourced"}
      <span class="docs-skipper-message__confidence docs-skipper-message__confidence--external">
        External
      </span>
    {/if}
  </div>

  {#if message.role === "assistant" && message.confidence === "best_guess" && message.content}
    <div class="docs-skipper-message__notice docs-skipper-message__notice--warning">
      Best guess — verify with primary documentation before acting.
    </div>
  {/if}

  {#if message.role === "assistant" && message.confidence === "unknown" && message.content}
    <div class="docs-skipper-message__notice docs-skipper-message__notice--muted">
      Unverified — could not validate against available data.
    </div>
  {/if}

  <div
    class={message.role === "assistant"
      ? "docs-skipper-message__bubble docs-skipper-message__bubble--assistant"
      : "docs-skipper-message__bubble docs-skipper-message__bubble--user"}
  >
    {#if message.role === "assistant" && message.content === ""}
      <div class="docs-skipper-message__thinking">
        <span></span><span></span><span></span>
      </div>
    {:else if message.blocks && message.blocks.length > 1}
      <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
      <div class="docs-skipper-message__content" onclick={handleCopyClick}>
        {#each message.blocks as block, i}
          {#if i > 0}
            <hr class="docs-skipper-message__block-divider" />
          {/if}
          <span class="docs-skipper-message__block-badge docs-skipper-message__confidence--tip">
            {confidenceLabels[block.confidence]}
            <span class="docs-skipper-message__tooltip">{confidenceTooltips[block.confidence]}</span
            >
          </span>
          {#if block.confidence === "best_guess"}
            <div class="docs-skipper-message__notice docs-skipper-message__notice--warning">
              Best guess — verify with primary documentation before acting.
            </div>
          {/if}
          <div
            class={block.confidence === "best_guess"
              ? "docs-skipper-message__prose docs-skipper-message__content--dim"
              : "docs-skipper-message__prose"}
          >
            {@html renderSkipperMarkdown(block.content)}
          </div>
          {#if block.sources?.length}
            <ul class="docs-skipper-message__block-sources">
              {#each block.sources as source}
                <li>
                  {#if isAbsoluteHttpUrl(source.url)}
                    <a
                      class="docs-skipper-message__link"
                      href={source.url}
                      target="_blank"
                      rel="noreferrer"
                    >
                      {source.label || source.url}
                    </a>
                  {:else}
                    <span>{source.label}</span>
                  {/if}
                </li>
              {/each}
            </ul>
          {/if}
        {/each}
      </div>
    {:else}
      <div
        class={message.confidence === "best_guess"
          ? "docs-skipper-message__content docs-skipper-message__content--dim"
          : "docs-skipper-message__content"}
      >
        <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
        <div class="docs-skipper-message__prose" onclick={handleCopyClick}>
          {@html renderSkipperMarkdown(message.content)}
        </div>
      </div>
    {/if}
  </div>

  {#if message.role === "assistant" && message.citations?.length && !hasBlockSources()}
    <details class="docs-skipper-message__details">
      <summary class="docs-skipper-message__details-summary">
        Citations
        <span class="docs-skipper-message__details-count">{message.citations.length}</span>
      </summary>
      <ul class="docs-skipper-message__sources-list">
        {#each message.citations as citation}
          <li>
            {#if isAbsoluteHttpUrl(citation.url)}
              <a
                class="docs-skipper-message__link"
                href={citation.url}
                target="_blank"
                rel="noreferrer"
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

  {#if message.role === "assistant" && message.externalLinks?.length}
    <details class="docs-skipper-message__details">
      <summary class="docs-skipper-message__details-summary">
        External sources
        <span class="docs-skipper-message__details-count">{message.externalLinks.length}</span>
      </summary>
      <ul class="docs-skipper-message__sources-list">
        {#each message.externalLinks as link}
          <li>
            {#if isAbsoluteHttpUrl(link.url)}
              <a
                class="docs-skipper-message__link"
                href={link.url}
                target="_blank"
                rel="noreferrer"
              >
                {link.label || link.url}
              </a>
            {:else}
              <span>{link.label}</span>
            {/if}
          </li>
        {/each}
      </ul>
    </details>
  {/if}

  {#if message.role === "assistant" && message.details?.length}
    <details class="docs-skipper-message__details">
      <summary class="docs-skipper-message__details-summary">
        Tool details
        <span class="docs-skipper-message__details-count">{message.details.length}</span>
      </summary>
      <div class="docs-skipper-message__details-body">
        {#each message.details as detail}
          <DocsSkipperToolResult {detail} />
        {/each}
      </div>
    </details>
  {/if}
</div>
