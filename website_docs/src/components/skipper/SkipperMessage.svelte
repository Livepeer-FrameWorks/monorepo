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

  import DocsSkipperToolResult from "./DocsSkipperToolResult.svelte";

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

  let copyIdCounter = 0;

  function renderMarkdown(value: string) {
    const blocks: string[] = [];
    let working = value.replace(/```([\s\S]*?)```/g, (_match, code) => {
      const index = blocks.length;
      const id = `skipper-code-${copyIdCounter++}`;
      blocks.push(
        `<div class="docs-skipper-message__code-wrap"><pre class="docs-skipper-message__code" id="${id}"><code>${escapeHtml(code.trim())}</code></pre><button class="docs-skipper-message__copy" data-copy-target="${id}" type="button">Copy</button></div>`
      );
      return `__BLOCK_${index}__`;
    });

    working = escapeHtml(working);

    // Headings (### before ## before # to avoid greedy match)
    working = working.replace(
      /(?:^|\n)#### (.+)/g,
      '\n<h6 class="docs-skipper-message__heading">$1</h6>'
    );
    working = working.replace(
      /(?:^|\n)### (.+)/g,
      '\n<h5 class="docs-skipper-message__heading">$1</h5>'
    );
    working = working.replace(
      /(?:^|\n)## (.+)/g,
      '\n<h4 class="docs-skipper-message__heading">$1</h4>'
    );
    working = working.replace(
      /(?:^|\n)# (.+)/g,
      '\n<h3 class="docs-skipper-message__heading">$1</h3>'
    );

    working = working.replace(/`([^`]+)`/g, '<code class="docs-skipper-message__inline">$1</code>');
    working = working.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    working = working.replace(/\*([^*]+)\*/g, "<em>$1</em>");
    working = working.replace(
      /\[([^\]]+)\]\((https?:\/\/[^)]+)\)/g,
      '<a class="docs-skipper-message__link" href="$2" target="_blank" rel="noreferrer">$1</a>'
    );

    // Convert unordered list blocks (consecutive lines starting with - )
    working = working.replace(/(?:^|\n)((?:- .+(?:\n|$))+)/g, (_match, listBlock: string) => {
      const items = listBlock
        .split("\n")
        .filter((line) => line.startsWith("- "))
        .map((line) => `<li>${line.slice(2)}</li>`)
        .join("");
      return `<ul class="docs-skipper-message__list">${items}</ul>`;
    });

    // Convert ordered list blocks (consecutive lines starting with N. )
    working = working.replace(/(?:^|\n)((?:\d+\. .+(?:\n|$))+)/g, (_match, listBlock: string) => {
      const items = listBlock
        .split("\n")
        .filter((line) => /^\d+\. /.test(line))
        .map((line) => `<li>${line.replace(/^\d+\. /, "")}</li>`)
        .join("");
      return `<ol class="docs-skipper-message__list">${items}</ol>`;
    });

    working = working.replace(/\n/g, "<br />");

    blocks.forEach((block, index) => {
      working = working.replace(`__BLOCK_${index}__`, block);
    });

    return working;
  }

  function handleCopyClick(e: MouseEvent) {
    const btn = (e.target as HTMLElement).closest<HTMLButtonElement>("[data-copy-target]");
    if (!btn) return;
    const pre = document.getElementById(btn.dataset.copyTarget || "");
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

  {#if message.role === "assistant" && message.confidence === "best_guess" && message.content}
    <div class="docs-skipper-message__notice docs-skipper-message__notice--warning">
      Best guess — verify with primary documentation before acting.
    </div>
  {/if}

  {#if message.role === "assistant" && message.confidence === "unknown" && message.content}
    <div class="docs-skipper-message__notice docs-skipper-message__notice--muted">
      I could not validate a confident answer based on available docs.
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
    {:else}
      <div
        class={message.confidence === "best_guess"
          ? "docs-skipper-message__content docs-skipper-message__content--dim"
          : "docs-skipper-message__content"}
      >
        <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
        <div class="docs-skipper-message__prose" onclick={handleCopyClick}>
          {@html renderMarkdown(message.content)}
        </div>
      </div>
    {/if}
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
    {#each message.details as detail}
      <DocsSkipperToolResult {detail} />
    {/each}
  {/if}
</div>
