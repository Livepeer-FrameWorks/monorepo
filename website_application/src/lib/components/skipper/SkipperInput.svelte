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
    if (textareaRef) {
      textareaRef.style.height = "auto";
    }
  }

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      send();
    }
  }

  function handleInput() {
    if (!textareaRef) return;
    textareaRef.style.height = "auto";
    textareaRef.style.height = `${Math.min(textareaRef.scrollHeight, 160)}px`;
  }
</script>

<form class="flex items-end gap-2" onsubmit={handleSubmit}>
  <div class="flex-1">
    <textarea
      bind:this={textareaRef}
      bind:value
      rows={1}
      class="w-full resize-none rounded-lg border border-border bg-background px-3 py-2.5 text-sm text-foreground placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 disabled:cursor-not-allowed disabled:opacity-60"
      {placeholder}
      {disabled}
      onkeydown={handleKeydown}
      oninput={handleInput}
    ></textarea>
  </div>
  {#if streaming}
    <button
      type="button"
      class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-red-500/90 text-white shadow-sm transition hover:bg-red-500"
      onclick={onStop}
      aria-label="Stop response"
    >
      <SquareIcon class="h-4 w-4" />
    </button>
  {:else}
    <button
      type="submit"
      class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground shadow-sm transition disabled:cursor-not-allowed disabled:opacity-60"
      disabled={disabled || !value.trim()}
      aria-label="Send message"
    >
      <SendIcon class="h-4 w-4" />
    </button>
  {/if}
</form>
<p class="mt-1.5 text-[11px] text-muted-foreground">
  Press Enter to send, Shift+Enter for a new line
</p>
