<script lang="ts">
  import { onMount, onDestroy, tick } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { getIconComponent } from "$lib/iconUtils";
  import { notificationStore } from "$lib/stores/notifications.svelte";
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
  let abortController = $state<AbortController | null>(null);

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
    if (abortController) {
      abortController.abort();
      abortController = null;
    }
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

  // --- API calls ---

  async function loadConversations() {
    try {
      loadingConversations = true;
      const response = await fetch("/api/skipper/conversations", {
        credentials: "include",
      });
      if (destroyed) return;
      if (response.ok) {
        conversations = (await response.json()) ?? [];
      }
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
      const response = await fetch(`/api/skipper/conversations/${id}`, {
        credentials: "include",
      });
      if (destroyed) return;
      if (!response.ok) {
        loadError = `Failed to load conversation (${response.status})`;
        return;
      }
      const convo = await response.json();
      if (destroyed) return;
      messages = (convo.Messages ?? []).map((msg: Record<string, unknown>) => {
        const sources = parseSources(msg.Sources as string | null);
        return {
          id: msg.ID as string,
          role: msg.Role as "user" | "assistant",
          content: msg.Content as string,
          confidence: (msg.Confidence as SkipperConfidence) || undefined,
          citations: sources.citations,
          externalLinks: sources.externalLinks,
          details: parseDetails(msg.ToolsUsed as string | null),
          blocks: parseConfidenceBlocks(msg.ConfidenceBlocks as string | null),
          createdAt: msg.CreatedAt as string,
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

  function parseSources(raw: string | null): {
    citations: SkipperChatMessage["citations"];
    externalLinks: SkipperChatMessage["externalLinks"];
  } {
    if (!raw) return { citations: [], externalLinks: [] };
    try {
      const sources = JSON.parse(raw);
      if (!Array.isArray(sources)) return { citations: [], externalLinks: [] };
      const citations: SkipperChatMessage["citations"] = [];
      const externalLinks: SkipperChatMessage["externalLinks"] = [];
      for (const s of sources) {
        const item = { label: s.Title || s.title || "", url: s.URL || s.url || "" };
        if (!item.url) continue;
        if (s.Type === "web" || s.type === "web") {
          externalLinks.push(item);
        } else {
          citations.push(item);
        }
      }
      return { citations, externalLinks };
    } catch {
      return { citations: [], externalLinks: [] };
    }
  }

  function parseDetails(raw: string | null): SkipperChatMessage["details"] {
    if (!raw) return [];
    try {
      const parsed = JSON.parse(raw);
      // New wrapped format: { calls: [...], details: [...] }
      if (parsed && !Array.isArray(parsed)) {
        if (Array.isArray(parsed.details) && parsed.details.length > 0) {
          return parsed.details;
        }
        if (Array.isArray(parsed.calls) && parsed.calls.length > 0) {
          return parsed.calls.map((t: Record<string, unknown>) => ({
            title: `Tool call: ${t.name || t.Name || "unknown"}`,
            payload: (t.arguments || t.Arguments || {}) as Record<string, unknown>,
          }));
        }
        return [];
      }
      // Old flat-array format: [{ name, arguments }, ...]
      if (!Array.isArray(parsed)) return [];
      return parsed.map((t: Record<string, unknown>) => ({
        title: `Tool call: ${t.name || t.Name || "unknown"}`,
        payload: (t.arguments || t.Arguments || {}) as Record<string, unknown>,
      }));
    } catch {
      return [];
    }
  }

  function parseConfidenceBlocks(raw: string | null): SkipperConfidenceBlock[] | undefined {
    if (!raw) return undefined;
    try {
      const parsed = JSON.parse(raw);
      if (!Array.isArray(parsed) || parsed.length === 0) return undefined;
      return parsed as SkipperConfidenceBlock[];
    } catch {
      return undefined;
    }
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

  function stopStreaming() {
    if (abortController) {
      abortController.abort();
      abortController = null;
    }
  }

  // --- Send message + SSE ---

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

    const controller = new AbortController();
    abortController = controller;
    let inactivityTimer: ReturnType<typeof setTimeout> | null = null;

    try {
      const response = await fetch("/api/skipper/chat", {
        method: "POST",
        credentials: "include",
        signal: controller.signal,
        headers: {
          "Content-Type": "application/json",
          Accept: "text/event-stream",
        },
        body: JSON.stringify({
          message: content,
          conversation_id: activeConversationId || undefined,
        }),
      });

      if (!response.ok || !response.body) {
        let errorMsg = "Unable to reach Skipper right now.";
        if (response.ok && !response.body) {
          errorMsg = "Streaming unavailable.";
        } else {
          try {
            const err = await response.json();
            if (response.status === 429) {
              const mins = Math.ceil((err.retry_after || 60) / 60);
              errorMsg = `Rate limit reached. Try again in ${mins} minute${mins > 1 ? "s" : ""}.`;
            } else if (response.status === 403) {
              errorMsg = err.error || "Skipper requires a premium subscription.";
            } else if (err.error) {
              errorMsg = err.error;
            }
          } catch {
            // Response body wasn't JSON
          }
        }
        updateMessage(assistantId, { content: errorMsg, confidence: "unknown" });
        isStreaming = false;
        abortController = null;
        return;
      }

      // Capture conversation ID from first response
      const newConvoId = response.headers.get("X-Conversation-ID");
      if (newConvoId && !activeConversationId) {
        activeConversationId = newConvoId;
      }

      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      let isDone = false;

      // Abort if no SSE data arrives for 90 seconds (e.g. silent connection drop).
      const INACTIVITY_MS = 90_000;
      function resetInactivityTimer() {
        if (inactivityTimer) clearTimeout(inactivityTimer);
        inactivityTimer = setTimeout(() => {
          console.warn("SSE inactivity timeout — aborting");
          controller.abort();
        }, INACTIVITY_MS);
      }
      resetInactivityTimer();

      while (!isDone) {
        const { done, value } = await reader.read();
        resetInactivityTimer();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let separatorIndex = buffer.indexOf("\n\n");
        while (separatorIndex !== -1) {
          const rawEvent = buffer.slice(0, separatorIndex);
          buffer = buffer.slice(separatorIndex + 2);
          const dataLines = rawEvent
            .split("\n")
            .filter((line) => line.startsWith("data:"))
            .map((line) => line.replace(/^data:\s?/, ""));
          const data = dataLines.join("\n").trim();

          if (data === "[DONE]") {
            isDone = true;
            break;
          }

          if (data) {
            const parsed = tryParseJson(data);
            if (parsed && typeof parsed === "object") {
              if (parsed.type === "token" && typeof parsed.content === "string") {
                // Clear tool status on first token
                if (activeToolName) {
                  activeToolName = "";
                  activeToolError = "";
                }
                const current = messages.find((m) => m.id === assistantId)?.content ?? "";
                updateMessage(assistantId, { content: current + parsed.content });
              } else if (parsed.type === "tool_start" && typeof parsed.tool === "string") {
                activeToolName = parsed.tool;
                activeToolError = "";
              } else if (parsed.type === "tool_end") {
                if (parsed.error) {
                  activeToolError = parsed.error;
                } else {
                  activeToolName = "";
                  activeToolError = "";
                }
              } else if (parsed.type === "meta") {
                updateMessage(assistantId, {
                  confidence: parsed.confidence as SkipperConfidence,
                  citations: parsed.citations,
                  externalLinks: parsed.externalLinks,
                  details: parsed.details,
                  blocks: parsed.blocks,
                });
              } else if (parsed.type === "done") {
                isDone = true;
                break;
              }
            }
          }
          separatorIndex = buffer.indexOf("\n\n");
        }
        scheduleScroll();
      }
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") {
        const current = messages.find((m) => m.id === assistantId)?.content ?? "";
        if (!current.trim()) {
          updateMessage(assistantId, {
            content: "*Response stopped.*",
            confidence: "unknown",
          });
        }
      } else {
        console.error("Streaming error:", err);
        updateMessage(assistantId, {
          content: "Connection lost. Please try again.",
          confidence: "unknown",
        });
      }
    } finally {
      if (inactivityTimer) clearTimeout(inactivityTimer);
      isStreaming = false;
      activeToolName = "";
      activeToolError = "";
      abortController = null;
      if (destroyed) return;
      // Detect empty response from orchestrator crash
      const final = messages.find((m) => m.id === assistantId);
      if (final && !final.content?.trim()) {
        updateMessage(assistantId, {
          content: "Something went wrong. Please try again.",
          confidence: "unknown",
        });
      }
      // Refresh conversation list to show the new/updated conversation
      await loadConversations();
    }
  }

  async function deleteConversation(id: string) {
    if (!confirm("Delete this conversation?")) return;
    try {
      const response = await fetch(`/api/skipper/conversations/${id}`, {
        method: "DELETE",
        credentials: "include",
      });
      if (!response.ok) return;
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
      const response = await fetch(`/api/skipper/conversations/${id}`, {
        method: "PATCH",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title }),
      });
      if (!response.ok) return;
      conversations = conversations.map((c) => (c.ID === id ? { ...c, Title: title } : c));
    } catch (err) {
      console.error("Failed to rename conversation:", err);
    }
  }

  function tryParseJson(data: string) {
    try {
      return JSON.parse(data);
    } catch {
      return null;
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
                <div
                  class="px-4 py-2 border-b border-[hsl(var(--tn-fg-gutter)/0.05)] {!report.readAt
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
                </div>
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
