<script lang="ts">
  import { tick } from "svelte";
  import { MessageCircle, XIcon } from "lucide-svelte";
  import SkipperMessage, {
    type SkipperChatMessage,
    type SkipperConfidence,
  } from "./SkipperMessage.svelte";
  import SkipperInput from "./SkipperInput.svelte";

  interface Props {
    mock?: boolean;
  }

  let { mock = true }: Props = $props();

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

    if (mock) {
      await mockStreamResponse(assistantId, content);
      return;
    }

    await streamResponse(assistantId, content);
  }

  async function mockStreamResponse(id: string, content: string) {
    isStreaming = true;
    const sample =
      "Here is a quick summary based on your dashboard context. Viewers are trending up by 12% week-over-week, with the highest engagement spikes on Saturday evenings. Consider scheduling your next live session in that window for maximum reach.";
    const tokens = sample.split(" ");

    for (const token of tokens) {
      await new Promise((resolve) => setTimeout(resolve, 60));
      updateMessage(id, {
        content: `${messages.find((message) => message.id === id)?.content ?? ""}${token} `,
      });
      await scrollToBottom();
    }

    updateMessage(id, {
      confidence: "sourced",
      citations: [
        { label: "Dashboard analytics snapshot", url: "https://example.com/dashboard" },
        { label: "Stream performance report", url: "https://example.com/report" },
      ],
      externalLinks: [
        { label: "Livepeer engagement benchmark", url: "https://example.com/benchmark" },
      ],
      details: [
        {
          title: "Tool call: analytics.summary",
          payload: {
            request: content,
            window: "Last 7 days",
            metrics: ["concurrent_viewers", "chat_activity", "retention"],
          },
        },
        {
          title: "Raw response",
          payload: {
            trending: "+12%",
            peakWindow: "Saturday 7-9pm",
            recommendation: "Schedule next session in peak window",
          },
        },
      ],
    });

    isStreaming = false;
  }

  async function streamResponse(id: string, content: string) {
    isStreaming = true;
    updateMessage(id, { confidence: "best_guess" });

    const response = await fetch("/api/skipper/chat", {
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
                details: parsed.details,
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

<div class="fixed bottom-4 right-4 z-40 flex flex-col items-end gap-3">
  {#if isOpen}
    <div
      class="flex h-[520px] w-[360px] flex-col overflow-hidden rounded-2xl border border-border bg-card shadow-2xl"
    >
      <div class="flex items-center justify-between border-b border-border bg-muted/30 px-4 py-3">
        <div
          class="flex items-center gap-2 text-xs font-semibold uppercase tracking-[0.2em] text-muted-foreground"
        >
          <MessageCircle class="h-4 w-4 text-primary" />
          Skipper
        </div>
        <button
          class="rounded-md border border-border bg-background/80 p-1 text-muted-foreground transition hover:text-foreground"
          onclick={() => {
            isOpen = false;
          }}
          aria-label="Close Skipper chat"
        >
          <XIcon class="h-4 w-4" />
        </button>
      </div>

      <div class="flex-1 space-y-4 overflow-y-auto bg-background px-4 py-4" bind:this={scrollRef}>
        {#if messages.length === 0}
          <div
            class="rounded-xl border border-dashed border-border bg-muted/30 px-4 py-3 text-sm text-muted-foreground"
          >
            Ask Skipper about viewer trends, stream health, or recommended actions.
          </div>
        {:else}
          {#each messages as message (message.id)}
            <div class={message.role === "user" ? "flex justify-end" : "flex justify-start"}>
              <div class="max-w-[85%]">
                <SkipperMessage {message} />
              </div>
            </div>
          {/each}
        {/if}
      </div>

      <div class="border-t border-border bg-muted/20 px-4 py-3">
        <SkipperInput bind:value={draft} disabled={isStreaming} onSend={handleSend} />
        <div class="mt-2 text-[11px] text-muted-foreground">
          {#if mock}
            Demo mode · streaming mock data
          {:else}
            Streaming live analytics from Skipper
          {/if}
        </div>
      </div>
    </div>
  {/if}

  <button
    class="flex h-12 w-12 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-[0_14px_30px_rgba(0,0,0,0.35)] transition hover:-translate-y-0.5 hover:shadow-[0_20px_38px_rgba(0,0,0,0.4)]"
    onclick={() => {
      isOpen = !isOpen;
    }}
    aria-label="Toggle Skipper chat"
  >
    {#if isOpen}
      <XIcon class="h-5 w-5" />
    {:else}
      <MessageCircle class="h-5 w-5" />
    {/if}
  </button>
</div>
