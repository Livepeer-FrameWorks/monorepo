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
    placeholder = "Ask Skipper about FrameWorks docs...",
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

<form class="docs-skipper-input" onsubmit={handleSubmit}>
  <label class="docs-skipper-input__field">
    <span class="sr-only">Message Skipper</span>
    <textarea bind:value rows={2} class="docs-skipper-input__textarea" {placeholder} {disabled}
    ></textarea>
  </label>
  <button
    type="submit"
    class="docs-skipper-input__submit"
    disabled={disabled || !value.trim()}
    aria-label="Send message"
  >
    <svg viewBox="0 0 24 24" fill="currentColor" width="16" height="16">
      <path d="M2.01 21L23 12 2.01 3 2 10l15 2-15 2z" />
    </svg>
  </button>
</form>
