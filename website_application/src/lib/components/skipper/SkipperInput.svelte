<script lang="ts">
  interface Props {
    value?: string;
    disabled?: boolean;
    placeholder?: string;
    onSend?: (message: string) => void;
  }

  let {
    value = $bindable(""),
    disabled = false,
    placeholder = "Ask Skipper anything about your dashboard...",
    onSend = () => {},
  }: Props = $props();

  function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    const trimmed = value.trim();
    if (!trimmed || disabled) return;
    onSend(trimmed);
    value = "";
  }
</script>

<form class="flex items-end gap-2" onsubmit={handleSubmit}>
  <div class="flex-1">
    <textarea
      bind:value
      rows={2}
      class="w-full resize-none rounded-lg border border-border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 disabled:cursor-not-allowed disabled:opacity-60"
      {placeholder}
      {disabled}
    ></textarea>
  </div>
  <button
    type="submit"
    class="h-9 shrink-0 rounded-lg bg-primary px-4 text-sm font-semibold text-primary-foreground shadow-sm transition disabled:cursor-not-allowed disabled:opacity-60"
    disabled={disabled || !value.trim()}
  >
    Send
  </button>
</form>
