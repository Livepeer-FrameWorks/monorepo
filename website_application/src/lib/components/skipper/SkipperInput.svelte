<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";

  interface Props {
    value?: string;
    disabled?: boolean;
    streaming?: boolean;
    placeholder?: string;
    onSend?: (message: string) => void;
    onStop?: () => void;
  }

  let {
    value = $bindable(""),
    disabled = false,
    streaming = false,
    placeholder = "Ask Skipper anything...",
    onSend = () => {},
    onStop = () => {},
  }: Props = $props();

  let textareaRef = $state<HTMLTextAreaElement | null>(null);
  const MAX_TEXTAREA_HEIGHT = 160;

  const SendIcon = getIconComponent("Send");
  const SquareIcon = getIconComponent("Square");

  function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    send();
  }

  function send() {
    const trimmed = value.trim();
    if (!trimmed || disabled) return;
    onSend(trimmed);
    value = "";
    resizeTextarea();
  }

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      send();
    }
  }

  function resizeTextarea(_currentValue: string = value) {
    if (!textareaRef) return;
    textareaRef.style.height = "auto";
    textareaRef.style.height = `${Math.min(textareaRef.scrollHeight, MAX_TEXTAREA_HEIGHT)}px`;
    textareaRef.style.overflowY =
      textareaRef.scrollHeight > MAX_TEXTAREA_HEIGHT ? "auto" : "hidden";
  }

  function handleInput() {
    resizeTextarea();
  }

  $effect(() => {
    resizeTextarea(value);
  });
</script>

<form class="space-y-1.5" onsubmit={handleSubmit}>
  <div
    class="flex items-end gap-2 rounded-lg border border-border bg-background p-2 focus-within:ring-2 focus-within:ring-primary/40"
  >
    <textarea
      bind:this={textareaRef}
      bind:value
      rows={1}
      class="min-h-9 w-full resize-none bg-transparent px-2 py-1.5 text-sm leading-5 text-foreground placeholder:text-muted-foreground focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-60"
      {placeholder}
      {disabled}
      onkeydown={handleKeydown}
      oninput={handleInput}
    ></textarea>
    {#if streaming}
      <button
        type="button"
        class="mb-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-red-500/90 text-white shadow-sm transition hover:bg-red-500"
        onclick={onStop}
        aria-label="Stop response"
      >
        <SquareIcon class="h-4 w-4" />
      </button>
    {:else}
      <button
        type="submit"
        class="mb-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-primary text-primary-foreground shadow-sm transition disabled:cursor-not-allowed disabled:opacity-60"
        disabled={disabled || !value.trim()}
        aria-label="Send message"
      >
        <SendIcon class="h-4 w-4" />
      </button>
    {/if}
  </div>
</form>
<p class="mt-1.5 text-[11px] text-muted-foreground">
  Press Enter to send, Shift+Enter for a new line
</p>
