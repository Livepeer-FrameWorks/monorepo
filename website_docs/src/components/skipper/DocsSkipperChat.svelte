<script lang="ts">
  import { tick } from "svelte";
  import SkipperMessage, {
    type SkipperChatMessage,
    type SkipperConfidence,
  } from "./SkipperMessage.svelte";
  import SkipperInput from "./SkipperInput.svelte";

  let isOpen = $state(false);
  let isStreaming = $state(false);
  let draft = $state("");
  let messages = $state<SkipperChatMessage[]>([]);
  let scrollRef = $state<HTMLDivElement | null>(null);

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
  }

  async function streamResponse(id: string, content: string) {
    isStreaming = true;
    updateMessage(id, { confidence: "best_guess" });

    const response = await fetch("/api/skipper/chat?mode=docs", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Accept: "text/event-stream",
      },
      body: JSON.stringify({ message: content }),
    });

    if (!response.ok || !response.body) {
      updateMessage(id, {
        content: "Unable to reach Skipper right now. Please try again soon.",
        confidence: "unknown",
      });
      isStreaming = false;
      return;
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let isDone = false;

    while (!isDone) {
      const { done, value } = await reader.read();
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
              appendToken(id, parsed.content);
            } else if (parsed.type === "meta") {
              updateMessage(id, {
                confidence: parsed.confidence as SkipperConfidence,
                citations: parsed.citations,
                externalLinks: parsed.externalLinks,
              });
            } else if (parsed.type === "done") {
              isDone = true;
              break;
            }
          } else {
            appendToken(id, data);
          }
        }

        separatorIndex = buffer.indexOf("\n\n");
      }

      await scrollToBottom();
    }

    isStreaming = false;
  }

  function appendToken(id: string, token: string) {
    const current = messages.find((message) => message.id === id)?.content ?? "";
    updateMessage(id, { content: `${current}${token}` });
  }

  function tryParseJson(data: string) {
    try {
      return JSON.parse(data);
    } catch {
      return null;
    }
  }
</script>

<div class="docs-skipper-chat">
  {#if isOpen}
    <div class="docs-skipper-panel" role="dialog" aria-label="Skipper docs chat">
      <div class="docs-skipper-panel__header">
        <div class="docs-skipper-panel__title">Skipper</div>
        <button
          class="docs-skipper-panel__close"
          onclick={() => {
            isOpen = false;
          }}
          aria-label="Close Skipper chat"
        >
          ✕
        </button>
      </div>

      <div class="docs-skipper-panel__body" bind:this={scrollRef}>
        {#if messages.length === 0}
          <div class="docs-skipper-panel__empty">
            Ask Skipper about FrameWorks docs, streaming setup, or troubleshooting steps.
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
        <SkipperInput bind:value={draft} disabled={isStreaming} onSend={handleSend} />
        <div class="docs-skipper-panel__hint">Searches knowledge base + web sources.</div>
      </div>
    </div>
  {/if}

  <button
    class="docs-skipper-toggle"
    onclick={() => {
      isOpen = !isOpen;
    }}
    aria-label="Toggle Skipper docs chat"
  >
    {#if isOpen}
      ✕
    {:else}
      Chat
    {/if}
  </button>
</div>
