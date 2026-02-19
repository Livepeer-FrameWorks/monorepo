<script lang="ts">
  import { onMount, onDestroy, tick } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { getIconComponent } from "$lib/iconUtils";
  import { notificationStore } from "$lib/stores/notifications.svelte";
  import {
    GetSkipperConversationsStore,
    GetSkipperConversationStore,
    DeleteSkipperConversationStore,
    UpdateSkipperConversationStore,
    SkipperChatStore,
  } from "$houdini";
  import SkipperMessage, {
    type SkipperChatMessage,
    type SkipperConfidence,
    type SkipperConfidenceBlock,
  } from "$lib/components/skipper/SkipperMessage.svelte";
  import SkipperInput from "$lib/components/skipper/SkipperInput.svelte";
  import SkipperToolStatus from "$lib/components/skipper/SkipperToolStatus.svelte";
  import SkipperSuggestedPrompts from "$lib/components/skipper/SkipperSuggestedPrompts.svelte";
  import SkipperConversationList, {
    type SkipperConversationSummary,
  } from "$lib/components/skipper/SkipperConversationList.svelte";

  // Houdini stores
  const conversationsQuery = new GetSkipperConversationsStore();
  const conversationQuery = new GetSkipperConversationStore();
  const deleteMutation = new DeleteSkipperConversationStore();
  const updateMutation = new UpdateSkipperConversationStore();
  let chatSub: SkipperChatStore | null = null;
  let chatUnsubscribe: (() => void) | null = null;

  let isAuthenticated = false;
  let destroyed = false;

  // Conversation state
  let conversations = $state<SkipperConversationSummary[]>([]);
  let activeConversationId = $state<string>("");
  let messages = $state<SkipperChatMessage[]>([]);
  let isStreaming = $state(false);
  let draft = $state("");
  let activeToolName = $state("");
  let activeToolError = $state("");
  let loadingConversations = $state(true);
  let loadingMessages = $state(false);
  let loadError = $state("");

  // Refs
  let scrollRef = $state<HTMLDivElement | null>(null);

  const BotIcon = getIconComponent("Bot");
  const ArrowLeftIcon = getIconComponent("ArrowLeft");
  const AlertTriangleIcon = getIconComponent("AlertTriangle");

  let showInvestigations = $state(true);

  function formatRelativeTime(dateStr: string): string {
    const diffMs = Date.now() - new Date(dateStr).getTime();
    const diffMin = Math.floor(diffMs / 60000);
    if (diffMin < 1) return "just now";
    if (diffMin < 60) return `${diffMin}m ago`;
    const diffHr = Math.floor(diffMin / 60);
    if (diffHr < 24) return `${diffHr}h ago`;
    return `${Math.floor(diffHr / 24)}d ago`;
  }

  let isMobile = $state(false);
  let mobileView = $state<"list" | "chat">("list");
  let showChat = $derived(!isMobile || mobileView === "chat");

  const unsubscribeAuth = auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  $effect(() => {
    const handleResize = () => {
      isMobile = window.innerWidth < 1024;
    };
    handleResize();
    window.addEventListener("resize", handleResize);
    return () => window.removeEventListener("resize", handleResize);
  });

  $effect(() => {
    if (!loadingConversations && conversations.length === 0 && isMobile) {
      mobileView = "chat";
    }
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadConversations();
  });

  onDestroy(() => {
    destroyed = true;
    unsubscribeAuth();
    teardownChat();
  });

  function createId() {
    if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
      return crypto.randomUUID();
    }
    return `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }

  function updateMessage(id: string, update: Partial<SkipperChatMessage>) {
    messages = messages.map((m) => (m.id === id ? { ...m, ...update } : m));
  }

  let scrollRafPending = false;

  async function scrollToBottom() {
    await tick();
    if (scrollRef) {
      scrollRef.scrollTop = scrollRef.scrollHeight;
    }
  }

  function scheduleScroll() {
    if (scrollRafPending) return;
    scrollRafPending = true;
    requestAnimationFrame(async () => {
      scrollRafPending = false;
      if (!destroyed) await scrollToBottom();
    });
  }

  // --- GraphQL API calls ---

  async function loadConversations() {
    try {
      loadingConversations = true;
      const resp = await conversationsQuery.fetch({
        policy: "NetworkOnly",
        variables: { limit: 50, offset: 0 },
      });
      if (destroyed) return;
      if (resp.errors?.length) {
        console.error("Failed to load conversations:", resp.errors);
        return;
      }
      conversations = (resp.data?.skipperConversations ?? []).map((c) => ({
        id: c.id,
        title: c.title,
        createdAt: c.createdAt,
        updatedAt: c.updatedAt,
      }));
    } catch (err) {
      if (!destroyed) console.error("Failed to load conversations:", err);
    } finally {
      if (!destroyed) loadingConversations = false;
    }
  }

  async function loadConversation(id: string) {
    try {
      loadingMessages = true;
      loadError = "";
      activeConversationId = id;
      const resp = await conversationQuery.fetch({
        policy: "NetworkOnly",
        variables: { id },
      });
      if (destroyed) return;
      if (resp.errors?.length) {
        loadError = `Failed to load conversation: ${resp.errors[0].message}`;
        return;
      }
      const convo = resp.data?.skipperConversation;
      if (!convo) {
        loadError = "Conversation not found";
        return;
      }
      messages = (convo.messages ?? []).map((msg) => {
        const sources = parseSources(msg.sources);
        return {
          id: msg.id,
          role: msg.role as "user" | "assistant",
          content: msg.content,
          confidence: (msg.confidence as SkipperConfidence) || undefined,
          citations: sources.citations,
          externalLinks: sources.externalLinks,
          details: parseDetails(msg.toolsUsed),
          blocks: parseConfidenceBlocks(msg.confidenceBlocks),
          createdAt: msg.createdAt,
        };
      });
      await scrollToBottom();
    } catch (err) {
      if (!destroyed) {
        console.error("Failed to load conversation:", err);
        loadError = "Failed to load conversation. Check connection.";
      }
    } finally {
      if (!destroyed) loadingMessages = false;
    }
  }

  // GraphQL JSON scalars arrive as already-parsed objects
  function parseSources(raw: unknown): {
    citations: SkipperChatMessage["citations"];
    externalLinks: SkipperChatMessage["externalLinks"];
  } {
    if (!raw) return { citations: [], externalLinks: [] };
    const sources = Array.isArray(raw) ? raw : [];
    const citations: SkipperChatMessage["citations"] = [];
    const externalLinks: SkipperChatMessage["externalLinks"] = [];
    for (const s of sources) {
      if (!s || typeof s !== "object") continue;
      const rec = s as Record<string, unknown>;
      const item = {
        label: (rec.Title as string) || (rec.title as string) || "",
        url: (rec.URL as string) || (rec.url as string) || "",
      };
      if (!item.url) continue;
      if (rec.Type === "web" || rec.type === "web") {
        externalLinks.push(item);
      } else {
        citations.push(item);
      }
    }
    return { citations, externalLinks };
  }

  function parseDetails(raw: unknown): SkipperChatMessage["details"] {
    if (!raw) return [];
    if (!Array.isArray(raw) && typeof raw === "object") {
      const obj = raw as Record<string, unknown>;
      if (Array.isArray(obj.details) && obj.details.length > 0) {
        return obj.details as SkipperChatMessage["details"];
      }
      if (Array.isArray(obj.calls) && obj.calls.length > 0) {
        return (obj.calls as Record<string, unknown>[]).map((t) => ({
          title: `Tool call: ${t.name || t.Name || "unknown"}`,
          payload: (t.arguments || t.Arguments || {}) as Record<string, unknown>,
        }));
      }
      return [];
    }
    if (!Array.isArray(raw)) return [];
    return (raw as Record<string, unknown>[]).map((t) => ({
      title: `Tool call: ${t.name || t.Name || "unknown"}`,
      payload: (t.arguments || t.Arguments || {}) as Record<string, unknown>,
    }));
  }

  function parseConfidenceBlocks(raw: unknown): SkipperConfidenceBlock[] | undefined {
    if (!raw) return undefined;
    if (!Array.isArray(raw) || raw.length === 0) return undefined;
    return raw as SkipperConfidenceBlock[];
  }

  // --- New conversation ---

  function startNewConversation() {
    activeConversationId = "";
    messages = [];
    draft = "";
    activeToolName = "";
    loadError = "";
  }

  function handleMobileBack() {
    mobileView = "list";
  }

  function teardownChat() {
    if (chatUnsubscribe) {
      chatUnsubscribe();
      chatUnsubscribe = null;
    }
    if (chatSub) {
      chatSub.unlisten();
      chatSub = null;
    }
  }

  function stopStreaming() {
    // Mark the current assistant message as stopped if it's empty
    const lastMsg = messages[messages.length - 1];
    if (lastMsg?.role === "assistant" && !lastMsg.content?.trim()) {
      updateMessage(lastMsg.id, { content: "*Response stopped.*", confidence: "unknown" });
    }
    teardownChat();
    isStreaming = false;
    activeToolName = "";
    activeToolError = "";
  }

  // --- Send message via GraphQL subscription ---

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
      { id: assistantId, role: "assistant", content: "", confidence: "best_guess" },
    ];
    await scrollToBottom();

    isStreaming = true;
    activeToolName = "";
    activeToolError = "";

    // Tear down any previous subscription
    teardownChat();

    chatSub = new SkipperChatStore();

    // Inactivity timeout: abort if no events for 90 seconds
    let inactivityTimer: ReturnType<typeof setTimeout> | null = null;
    const INACTIVITY_MS = 90_000;
    function resetInactivityTimer() {
      if (inactivityTimer) clearTimeout(inactivityTimer);
      inactivityTimer = setTimeout(() => {
        console.warn("Chat subscription inactivity timeout");
        finishStreaming(assistantId, inactivityTimer);
      }, INACTIVITY_MS);
    }
    resetInactivityTimer();

    chatUnsubscribe = chatSub.subscribe((result) => {
      if (destroyed) return;
      resetInactivityTimer();

      if (result.errors?.length) {
        const errMsg = result.errors[0].message || "Something went wrong.";
        let displayMsg = "Unable to reach Skipper right now.";
        if (/rate.?limit/i.test(errMsg)) {
          displayMsg = "Rate limit reached. Please try again in a few minutes.";
        } else if (/premium|subscription/i.test(errMsg)) {
          displayMsg = "Skipper requires a premium subscription.";
        } else if (/auth/i.test(errMsg)) {
          displayMsg = "Authentication required. Please log in again.";
        } else {
          displayMsg = errMsg;
        }
        updateMessage(assistantId, { content: displayMsg, confidence: "unknown" });
        finishStreaming(assistantId, inactivityTimer);
        return;
      }

      const event = result.data?.skipperChat;
      if (!event) return;

      const typename = (event as Record<string, unknown>).__typename;
      switch (typename) {
        case "SkipperToken": {
          const e = event as { content: string };
          if (activeToolName) {
            activeToolName = "";
            activeToolError = "";
          }
          const current = messages.find((m) => m.id === assistantId)?.content ?? "";
          updateMessage(assistantId, { content: current + e.content });
          scheduleScroll();
          break;
        }
        case "SkipperToolStartEvent": {
          const e = event as { tool: string };
          activeToolName = e.tool;
          activeToolError = "";
          break;
        }
        case "SkipperToolEndEvent": {
          const e = event as { tool: string; error?: string | null };
          if (e.error) {
            activeToolError = e.error;
          } else {
            activeToolName = "";
            activeToolError = "";
          }
          break;
        }
        case "SkipperMeta": {
          const e = event as {
            confidence: string;
            citations: SkipperChatMessage["citations"];
            externalLinks: SkipperChatMessage["externalLinks"];
            details: SkipperChatMessage["details"];
            blocks?: SkipperConfidenceBlock[] | null;
          };
          updateMessage(assistantId, {
            confidence: e.confidence as SkipperConfidence,
            citations: e.citations,
            externalLinks: e.externalLinks,
            details: e.details,
            blocks: e.blocks ?? undefined,
          });
          break;
        }
        case "SkipperDone": {
          const e = event as { conversationId: string };
          if (e.conversationId && !activeConversationId) {
            activeConversationId = e.conversationId;
          }
          finishStreaming(assistantId, inactivityTimer);
          break;
        }
      }
    });

    chatSub.listen({
      input: {
        message: content,
        conversationId: activeConversationId || undefined,
      },
    });
  }

  function finishStreaming(
    assistantId: string,
    inactivityTimer: ReturnType<typeof setTimeout> | null
  ) {
    if (inactivityTimer) clearTimeout(inactivityTimer);
    teardownChat();
    isStreaming = false;
    activeToolName = "";
    activeToolError = "";
    if (destroyed) return;
    // Detect empty response from orchestrator crash
    const final = messages.find((m) => m.id === assistantId);
    if (final && !final.content?.trim()) {
      updateMessage(assistantId, {
        content: "Something went wrong. Please try again.",
        confidence: "unknown",
      });
    }
    void loadConversations();
  }

  async function deleteConversation(id: string) {
    if (!confirm("Delete this conversation?")) return;
    try {
      const resp = await deleteMutation.mutate({ id });
      if (resp.errors?.length) return;
      if (activeConversationId === id) {
        startNewConversation();
      }
      await loadConversations();
    } catch (err) {
      console.error("Failed to delete conversation:", err);
    }
  }

  async function renameConversation(id: string, title: string) {
    try {
      const resp = await updateMutation.mutate({ id, title });
      if (resp.errors?.length) return;
      conversations = conversations.map((c) => (c.id === id ? { ...c, title } : c));
    } catch (err) {
      console.error("Failed to rename conversation:", err);
    }
  }
</script>

<svelte:head>
  <title>Skipper - FrameWorks</title>
</svelte:head>

<div class="flex h-full flex-col overflow-hidden">
  <!-- Header -->
  <div
    class="flex shrink-0 items-center justify-between border-b border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background px-4 py-3 sm:px-6"
  >
    <div class="flex items-center gap-3">
      {#if isMobile && mobileView === "chat"}
        <button
          class="rounded-md p-1.5 text-muted-foreground transition hover:bg-muted hover:text-foreground"
          onclick={handleMobileBack}
          aria-label="Back to conversations"
        >
          <ArrowLeftIcon class="h-4 w-4" />
        </button>
      {/if}
      <BotIcon class="h-5 w-5 text-primary" />
      <div>
        <h1 class="text-lg font-bold text-foreground">Skipper</h1>
        <p class="text-xs text-muted-foreground">AI video consultant</p>
      </div>
    </div>
  </div>

  <!-- Main Content -->
  <div class="flex flex-1 overflow-hidden">
    <!-- Mobile: full-width conversation list -->
    {#if isMobile && mobileView === "list"}
      <div class="flex flex-1 flex-col overflow-hidden lg:hidden">
        {#if loadingConversations}
          <div class="space-y-3 p-4">
            {#each Array.from({ length: 4 }) as _, i (i)}
              <div class="h-12 animate-pulse rounded-lg bg-muted"></div>
            {/each}
          </div>
        {:else}
          <SkipperConversationList
            {conversations}
            activeId={activeConversationId}
            onSelect={(id) => {
              loadConversation(id);
              mobileView = "chat";
            }}
            onNew={() => {
              startNewConversation();
              mobileView = "chat";
            }}
            onDelete={deleteConversation}
            onRename={renameConversation}
          />
        {/if}
      </div>
    {/if}

    <!-- Chat Area -->
    {#if showChat}
      <div class="flex flex-1 flex-col overflow-hidden">
        <!-- Messages -->
        <div bind:this={scrollRef} class="flex-1 overflow-y-auto bg-background/50 p-4 sm:p-6">
          {#if messages.length === 0 && !loadingMessages}
            {#if loadError}
              <div
                class="mx-auto mb-6 max-w-2xl rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400"
              >
                {loadError}
              </div>
            {/if}
            <SkipperSuggestedPrompts onSend={handleSend} />
          {:else if loadingMessages}
            <div class="mx-auto max-w-3xl space-y-4">
              {#each Array.from({ length: 4 }) as _, i (i)}
                <div class="flex gap-3 {i % 2 === 0 ? 'justify-end' : ''}">
                  <div class="max-w-[70%]">
                    <div class="h-16 animate-pulse rounded-lg bg-muted"></div>
                  </div>
                </div>
              {/each}
            </div>
          {:else}
            <div class="mx-auto max-w-3xl space-y-6">
              {#each messages as message (message.id)}
                <div class={message.role === "user" ? "flex justify-end" : "flex justify-start"}>
                  <div class="max-w-[85%]">
                    <SkipperMessage {message} />
                  </div>
                </div>
              {/each}

              <!-- Tool status indicator -->
              {#if activeToolName}
                <div class="flex justify-start">
                  <div class="max-w-[85%]">
                    <SkipperToolStatus toolName={activeToolName} error={activeToolError} />
                  </div>
                </div>
              {/if}

              <!-- Streaming indicator (waiting for first token) -->
              {#if isStreaming && !activeToolName}
                {@const lastMsg = messages[messages.length - 1]}
                {#if lastMsg?.role === "assistant" && !lastMsg.content}
                  <div class="flex justify-start">
                    <div
                      class="flex items-center gap-1.5 rounded-xl border border-border bg-card px-4 py-3"
                    >
                      <span class="h-2 w-2 animate-pulse rounded-full bg-primary/60"></span>
                      <span
                        class="h-2 w-2 animate-pulse rounded-full bg-primary/60"
                        style="animation-delay: 150ms"
                      ></span>
                      <span
                        class="h-2 w-2 animate-pulse rounded-full bg-primary/60"
                        style="animation-delay: 300ms"
                      ></span>
                    </div>
                  </div>
                {/if}
              {/if}
            </div>
          {/if}
        </div>

        <!-- Input Footer -->
        <div
          class="shrink-0 border-t border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-4 sm:px-6"
        >
          <div class="mx-auto max-w-3xl">
            <SkipperInput
              bind:value={draft}
              disabled={isStreaming}
              streaming={isStreaming}
              onSend={handleSend}
              onStop={stopStreaming}
            />
          </div>
        </div>
      </div>
    {/if}

    <!-- Right rail: investigations + conversation history (desktop only) -->
    <div
      class="hidden w-72 shrink-0 flex-col border-l border-[hsl(var(--tn-fg-gutter)/0.3)] bg-sidebar lg:flex"
    >
      <!-- Investigations section -->
      <div class="border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
        <button
          class="flex w-full items-center justify-between px-4 py-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
          onclick={() => (showInvestigations = !showInvestigations)}
        >
          <span class="flex items-center gap-1.5">
            <AlertTriangleIcon class="h-3.5 w-3.5" />
            Investigations
            {#if notificationStore.unreadCount > 0}
              <span
                class="inline-flex items-center justify-center min-w-[16px] h-4 px-1 text-[10px] font-bold leading-none text-white bg-[hsl(var(--tn-red))] rounded-full"
              >
                {notificationStore.unreadCount}
              </span>
            {/if}
          </span>
          <svg
            class="h-3 w-3 transition-transform {showInvestigations ? 'rotate-180' : ''}"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M19 9l-7 7-7-7"
            />
          </svg>
        </button>
        {#if showInvestigations}
          <div class="max-h-48 overflow-y-auto">
            {#if notificationStore.reports.length === 0}
              <p class="px-4 py-3 text-xs text-muted-foreground">No investigations yet</p>
            {:else}
              {#each notificationStore.reports.slice(0, 5) as report (report.id)}
                <button
                  onclick={() => {
                    if (!report.readAt) notificationStore.markRead([report.id]);
                  }}
                  class="w-full text-left px-4 py-2 border-b border-[hsl(var(--tn-fg-gutter)/0.05)] hover:bg-[hsl(var(--tn-bg-visual))] transition-colors cursor-pointer {!report.readAt
                    ? 'border-l-2 border-l-[hsl(var(--tn-blue))]'
                    : 'border-l-2 border-l-transparent'}"
                >
                  <p
                    class="text-xs leading-snug line-clamp-2 {!report.readAt
                      ? 'text-foreground font-medium'
                      : 'text-muted-foreground'}"
                  >
                    {report.summary}
                  </p>
                  <div class="mt-0.5 flex items-center gap-1.5">
                    {#if report.trigger}
                      <span
                        class="text-[9px] uppercase tracking-wider px-1 py-0.5 rounded bg-[hsl(var(--tn-bg-visual))] text-muted-foreground"
                      >
                        {report.trigger}
                      </span>
                    {/if}
                    <span class="text-[9px] text-muted-foreground">
                      {formatRelativeTime(report.createdAt)}
                    </span>
                  </div>
                </button>
              {/each}
            {/if}
          </div>
        {/if}
      </div>

      <!-- Conversation list -->
      <div class="flex-1 overflow-hidden">
        {#if loadingConversations}
          <div class="space-y-3 p-4">
            {#each Array.from({ length: 4 }) as _, i (i)}
              <div class="h-12 animate-pulse rounded-lg bg-muted"></div>
            {/each}
          </div>
        {:else}
          <SkipperConversationList
            {conversations}
            activeId={activeConversationId}
            onSelect={loadConversation}
            onNew={startNewConversation}
            onDelete={deleteConversation}
            onRename={renameConversation}
          />
        {/if}
      </div>
    </div>
  </div>
</div>
