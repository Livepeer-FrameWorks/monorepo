<script lang="ts">
  import { tick, onMount, onDestroy } from "svelte";
  import SkipperMessage, {
    type SkipperChatMessage,
    type SkipperConfidence,
  } from "./SkipperMessage.svelte";
  import SkipperInput from "./SkipperInput.svelte";
  import { getSubscriptionClient, disposeSubscriptionClient } from "../../lib/graphql";

  const STORAGE_KEY = "skipper-docs-chat";
  const CONVO_KEY = "skipper-docs-convo";
  const SEEN_KEY = "skipper-docs-seen";
  const NUDGE_KEY = "skipper-docs-nudge-dismissed";

  const SKIPPER_CHAT_SUBSCRIPTION = `
    subscription SkipperChat($input: SkipperChatInput!) {
      skipperChat(input: $input) {
        ... on SkipperToken { content }
        ... on SkipperToolStartEvent { tool }
        ... on SkipperToolEndEvent { tool error }
        ... on SkipperMeta {
          confidence
          citations { label url }
          externalLinks { label url }
          details { title payload }
          blocks { content confidence sources { label url } }
        }
        ... on SkipperDone { conversationId tokensInput tokensOutput }
      }
    }
  `;

  let isOpen = $state(false);
  let isStreaming = $state(false);
  let isAuthenticated = $state(false);
  let draft = $state("");
  let messages = $state<SkipperChatMessage[]>([]);
  let activeTools = $state<string[]>([]);
  let toolError = $state("");
  let scrollRef = $state<HTMLDivElement | null>(null);
  let panelRef = $state<HTMLDivElement | null>(null);
  let hasSeen = $state(false);
  let conversationId = $state("");
  let showNudge = $state(false);
  let nudgeDismissed = $state(false);

  const starters = [
    { label: "Stream setup", prompt: "How do I set up a live stream?" },
    { label: "Viewer routing", prompt: "Explain how viewer routing works" },
    { label: "MCP tools", prompt: "What MCP tools are available?" },
  ];

  const toolLabels: Record<string, string> = {
    search_knowledge: "Searching knowledge base",
    search_web: "Searching the web",
    diagnose_rebuffering: "Diagnosing rebuffering",
    diagnose_buffer_health: "Diagnosing buffer health",
    diagnose_packet_loss: "Diagnosing packet loss",
    diagnose_routing: "Analyzing viewer routing",
    get_stream_health_summary: "Checking stream health",
    get_anomaly_report: "Detecting anomalies",
    introspect_schema: "Reading API schema",
    generate_query: "Generating query",
    execute_query: "Querying API",
    search_support_history: "Searching support history",
  };

  function handleAuthChange(event: Event) {
    const detail = (event as CustomEvent).detail;
    isAuthenticated = !!detail?.user;
  }

  let nudgeShowTimer: ReturnType<typeof setTimeout> | undefined;
  let nudgeHideTimer: ReturnType<typeof setTimeout> | undefined;

  onMount(() => {
    try {
      const saved = sessionStorage.getItem(STORAGE_KEY);
      if (saved) messages = JSON.parse(saved);
      conversationId = sessionStorage.getItem(CONVO_KEY) ?? "";
      hasSeen = sessionStorage.getItem(SEEN_KEY) === "1";
      nudgeDismissed = localStorage.getItem(NUDGE_KEY) === "1";
    } catch {}

    if (!nudgeDismissed && !hasSeen) {
      nudgeShowTimer = setTimeout(() => {
        showNudge = true;
        nudgeHideTimer = setTimeout(() => {
          dismissNudge();
        }, 8000);
      }, 2000);
    }

    window.addEventListener("docs-auth-change", handleAuthChange);
  });

  onDestroy(() => {
    clearTimeout(nudgeShowTimer);
    clearTimeout(nudgeHideTimer);
    if (typeof window !== "undefined") {
      window.removeEventListener("docs-auth-change", handleAuthChange);
    }
    disposeSubscriptionClient();
  });

  function persistMessages() {
    try {
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify(messages));
    } catch {}
  }

  function markSeen() {
    if (!hasSeen) {
      hasSeen = true;
      try {
        sessionStorage.setItem(SEEN_KEY, "1");
      } catch {}
    }
  }

  function dismissNudge() {
    showNudge = false;
    nudgeDismissed = true;
    clearTimeout(nudgeHideTimer);
    try {
      localStorage.setItem(NUDGE_KEY, "1");
    } catch {}
  }

  async function toggleOpen() {
    isOpen = !isOpen;
    if (isOpen) {
      markSeen();
      dismissNudge();
      await tick();
      panelRef?.querySelector("textarea")?.focus();
    }
  }

  function handleKeydown(event: KeyboardEvent) {
    if ((event.metaKey || event.ctrlKey) && event.key === "j") {
      event.preventDefault();
      toggleOpen();
    } else if (event.key === "Escape" && isOpen) {
      isOpen = false;
    }
  }

  function createId() {
    if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
      return crypto.randomUUID();
    }
    return `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }

  function updateMessage(id: string, update: Partial<SkipperChatMessage>) {
    messages = messages.map((message) => (message.id === id ? { ...message, ...update } : message));
  }

  async function scrollToBottom() {
    await tick();
    if (scrollRef) {
      scrollRef.scrollTop = scrollRef.scrollHeight;
    }
  }

  async function handleSend(content: string) {
    if (isStreaming) return;
    const userMessage: SkipperChatMessage = {
      id: createId(),
      role: "user",
      content,
    };
    messages = [...messages, userMessage];
    draft = "";

    const assistantId = createId();
    messages = [
      ...messages,
      {
        id: assistantId,
        role: "assistant",
        content: "",
        confidence: "best_guess",
      },
    ];

    await scrollToBottom();
    await streamResponse(assistantId, content);
    persistMessages();
  }

  function handleClear() {
    messages = [];
    activeTools = [];
    toolError = "";
    conversationId = "";
    try {
      sessionStorage.removeItem(STORAGE_KEY);
      sessionStorage.removeItem(CONVO_KEY);
    } catch {}
  }

  async function streamResponse(id: string, content: string) {
    isStreaming = true;
    activeTools = [];
    toolError = "";
    updateMessage(id, { confidence: "best_guess" });

    try {
      const client = getSubscriptionClient();
      const input: Record<string, unknown> = {
        message: content,
        pageUrl: window.location.pathname,
        mode: "DOCS",
      };
      if (conversationId) {
        input.conversationId = conversationId;
      }

      const subscription = client.iterate<{
        skipperChat: Record<string, unknown>;
      }>({
        query: SKIPPER_CHAT_SUBSCRIPTION,
        variables: { input },
      });

      for await (const result of subscription) {
        if (!result.data) continue;
        const event = result.data.skipperChat;

        if ("content" in event && typeof event.content === "string") {
          // SkipperToken
          activeTools = [];
          toolError = "";
          appendToken(id, event.content);
        } else if ("tool" in event && !("error" in event)) {
          // SkipperToolStartEvent (has tool, no error field)
          toolError = "";
          activeTools = [...activeTools, event.tool as string];
        } else if ("tool" in event && "error" in event) {
          // SkipperToolEndEvent
          activeTools = activeTools.filter((t) => t !== event.tool);
          if (event.error) {
            toolError = `${event.tool} failed`;
          }
        } else if ("confidence" in event) {
          // SkipperMeta
          activeTools = [];
          toolError = "";
          updateMessage(id, {
            confidence: event.confidence as SkipperConfidence,
            citations: event.citations as SkipperChatMessage["citations"],
            externalLinks: event.externalLinks as SkipperChatMessage["externalLinks"],
            details: event.details as SkipperChatMessage["details"],
            blocks: event.blocks as SkipperChatMessage["blocks"],
          });
        } else if ("conversationId" in event) {
          // SkipperDone
          activeTools = [];
          toolError = "";
          if (event.conversationId) {
            conversationId = event.conversationId as string;
            try {
              sessionStorage.setItem(CONVO_KEY, conversationId);
            } catch {}
          }
        }

        await scrollToBottom();
      }
    } catch {
      updateMessage(id, {
        content: "Unable to reach Skipper right now. Please try again soon.",
        confidence: "unknown",
      });
    } finally {
      isStreaming = false;
      activeTools = [];
      toolError = "";
    }
  }

  function appendToken(id: string, token: string) {
    const current = messages.find((message) => message.id === id)?.content ?? "";
    updateMessage(id, { content: `${current}${token}` });
  }

  function toolLabel(name: string) {
    return toolLabels[name] ?? `Using ${name.replaceAll("_", " ")}`;
  }

  function openAuthPanel() {
    window.dispatchEvent(new CustomEvent("docs-auth-open"));
  }
</script>

<svelte:window onkeydown={handleKeydown} />

<div class="docs-skipper-chat">
  {#if isOpen}
    <div
      class="docs-skipper-panel"
      role="dialog"
      aria-label="Skipper docs chat"
      bind:this={panelRef}
    >
      <div class="docs-skipper-panel__header">
        <div class="docs-skipper-panel__title">
          <svg
            class="docs-skipper-panel__icon"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
            width="14"
            height="14"
          >
            <circle cx="12" cy="12" r="10" /><polygon
              points="16.24 7.76 14.12 14.12 7.76 16.24 9.88 9.88 16.24 7.76"
            />
          </svg>
          Skipper
        </div>
        <div class="docs-skipper-panel__actions">
          {#if messages.length > 0}
            <button
              class="docs-skipper-panel__clear"
              onclick={handleClear}
              aria-label="Clear conversation"
            >
              Clear
            </button>
          {/if}
          <button
            class="docs-skipper-panel__close"
            onclick={() => {
              isOpen = false;
            }}
            aria-label="Close Skipper chat"
          >
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
              width="14"
              height="14"
            >
              <line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" />
            </svg>
          </button>
        </div>
      </div>

      <div class="docs-skipper-panel__body" bind:this={scrollRef}>
        {#if !isAuthenticated}
          <div class="docs-skipper-panel__empty">
            <div class="docs-skipper-panel__empty-text">
              Sign in to chat with Skipper about FrameWorks docs.
            </div>
            <button class="docs-skipper-panel__starter" onclick={openAuthPanel}> Sign in </button>
          </div>
        {:else if messages.length === 0}
          <div class="docs-skipper-panel__empty">
            <div class="docs-skipper-panel__empty-text">
              Ask Skipper about FrameWorks docs, streaming setup, or troubleshooting steps.
            </div>
            <div class="docs-skipper-panel__starters">
              {#each starters as starter}
                <button
                  class="docs-skipper-panel__starter"
                  onclick={() => handleSend(starter.prompt)}
                >
                  {starter.label}
                </button>
              {/each}
            </div>
          </div>
        {:else}
          {#each messages as message (message.id)}
            <div
              class={message.role === "user"
                ? "docs-skipper-panel__row docs-skipper-panel__row--user"
                : "docs-skipper-panel__row"}
            >
              <div class="docs-skipper-panel__message">
                <SkipperMessage {message} />
              </div>
            </div>
          {/each}
        {/if}
      </div>

      <div class="docs-skipper-panel__footer">
        {#if activeTools.length > 0}
          <div class="docs-skipper-panel__activity">
            <span class="docs-skipper-panel__activity-dot"></span>
            {toolLabel(activeTools[activeTools.length - 1])}
          </div>
        {:else if toolError}
          <div class="docs-skipper-panel__activity docs-skipper-panel__activity--error">
            {toolError}
          </div>
        {/if}
        <SkipperInput
          bind:value={draft}
          disabled={isStreaming || !isAuthenticated}
          onSend={handleSend}
        />
        <div class="docs-skipper-panel__hint">
          Searches knowledge base + web sources.
          <kbd class="docs-skipper-panel__kbd"
            >{navigator?.platform?.includes("Mac") ? "\u2318" : "Ctrl"}J</kbd
          >
        </div>
      </div>
    </div>
  {/if}

  {#if showNudge && !isOpen}
    <div class="docs-skipper-nudge">
      <span>Need help? Ask Skipper about streams, codecs, or setup.</span>
      <button class="docs-skipper-nudge__close" onclick={dismissNudge} aria-label="Dismiss">
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2.5"
          width="10"
          height="10"
          ><line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" /></svg
        >
      </button>
    </div>
  {/if}

  <button
    class="docs-skipper-toggle"
    class:docs-skipper-toggle--glow={!hasSeen}
    class:docs-skipper-toggle--open={isOpen}
    onclick={toggleOpen}
    aria-label="Toggle Skipper docs chat"
  >
    {#if isOpen}
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2.5"
        stroke-linecap="round"
        stroke-linejoin="round"
        width="18"
        height="18"
      >
        <line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" />
      </svg>
    {:else}
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
        width="16"
        height="16"
      >
        <circle cx="12" cy="12" r="10" /><polygon
          points="16.24 7.76 14.12 14.12 7.76 16.24 9.88 9.88 16.24 7.76"
        />
      </svg>
      <span class="docs-skipper-toggle__label">Ask Skipper</span>
    {/if}
  </button>
</div>
