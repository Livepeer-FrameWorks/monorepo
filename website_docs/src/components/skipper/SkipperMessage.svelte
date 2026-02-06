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
        `<pre class=\"docs-skipper-message__code\"><code>${escapeHtml(code.trim())}</code></pre>`
      );
      return `__BLOCK_${index}__`;
    });

    working = escapeHtml(working);
    working = working.replace(/`([^`]+)`/g, '<code class="docs-skipper-message__inline">$1</code>');
    working = working.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    working = working.replace(/\*([^*]+)\*/g, "<em>$1</em>");
    working = working.replace(
      /\[([^\]]+)\]\((https?:\/\/[^)]+)\)/g,
      '<a class="docs-skipper-message__link" href="$2" target="_blank" rel="noreferrer">$1</a>'
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

<div class="docs-skipper-message">
  <div class="docs-skipper-message__meta">
    <span class="docs-skipper-message__role">
      {message.role === "assistant" ? "Skipper" : "You"}
    </span>
    {#if message.role === "assistant" && message.confidence}
      <span class="docs-skipper-message__confidence">
        {confidenceLabels[message.confidence]}
      </span>
    {/if}
    {#if message.role === "assistant" && message.confidence === "sourced"}
      <span class="docs-skipper-message__confidence docs-skipper-message__confidence--external">
        External
      </span>
    {/if}
  </div>

  {#if message.role === "assistant" && message.confidence === "best_guess"}
    <div class="docs-skipper-message__notice docs-skipper-message__notice--warning">
      Best guess — verify with primary documentation before acting.
    </div>
  {/if}

  {#if message.role === "assistant" && message.confidence === "unknown"}
    <div class="docs-skipper-message__notice docs-skipper-message__notice--muted">
      I could not validate a confident answer based on available docs.
    </div>
  {/if}

  <div
    class={message.role === "assistant"
      ? "docs-skipper-message__bubble docs-skipper-message__bubble--assistant"
      : "docs-skipper-message__bubble docs-skipper-message__bubble--user"}
  >
    <div
      class={message.confidence === "best_guess"
        ? "docs-skipper-message__content docs-skipper-message__content--dim"
        : "docs-skipper-message__content"}
    >
      <div class="docs-skipper-message__prose">
        {@html renderMarkdown(message.content)}
      </div>
    </div>
  </div>

  {#if message.role === "assistant" && message.citations?.length}
    <div class="docs-skipper-message__sources">
      <div class="docs-skipper-message__sources-title">Citations</div>
      <ul class="docs-skipper-message__sources-list">
        {#each message.citations as citation}
          <li>
            <a
              class="docs-skipper-message__link"
              href={citation.url}
              target="_blank"
              rel="noreferrer"
            >
              {citation.label}
            </a>
          </li>
        {/each}
      </ul>
    </div>
  {/if}

  {#if message.role === "assistant" && message.externalLinks?.length}
    <div class="docs-skipper-message__sources">
      <div class="docs-skipper-message__sources-title">External sources</div>
      <ul class="docs-skipper-message__sources-list">
        {#each message.externalLinks as link}
          <li>
            <a class="docs-skipper-message__link" href={link.url} target="_blank" rel="noreferrer">
              {link.label}
            </a>
          </li>
        {/each}
      </ul>
    </div>
  {/if}

  {#if message.role === "assistant" && message.details?.length}
    <details class="docs-skipper-message__details">
      <summary class="docs-skipper-message__details-summary">Details</summary>
      <div class="docs-skipper-message__details-body">
        {#each message.details as detail}
          <div>
            {#if detail.title}
              <div class="docs-skipper-message__details-title">{detail.title}</div>
            {/if}
            <pre class="docs-skipper-message__details-code">{formatDetail(detail)}</pre>
          </div>
        {/each}
      </div>
    </details>
  {/if}
</div>
