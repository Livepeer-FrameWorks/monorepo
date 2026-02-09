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

  let textareaRef = $state<HTMLTextAreaElement | null>(null);
  const MAX_TEXTAREA_HEIGHT = 160;

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

<form class="docs-skipper-input" onsubmit={handleSubmit}>
  <div class="docs-skipper-input__shell">
    <label class="docs-skipper-input__field">
      <span class="sr-only">Message Skipper</span>
      <textarea
        bind:this={textareaRef}
        bind:value
        rows={1}
        class="docs-skipper-input__textarea"
        {placeholder}
        {disabled}
        onkeydown={handleKeydown}
        oninput={handleInput}
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
  </div>
</form>
