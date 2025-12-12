<script lang="ts">
  interface Props {
    /** Visual variant of the divider */
    variant?: "default" | "shelf";
    /** Whether to show the center glowing bar */
    showBar?: boolean;
    /** Additional CSS classes */
    class?: string;
  }

  let {
    variant = "default",
    showBar = true,
    class: className = "",
  }: Props = $props();
</script>

<div
  class={`section-divider ${variant === "shelf" ? "section-divider--shelf" : "section-divider--default"} ${className}`}
  aria-hidden="true"
>
  {#if showBar && variant === "default"}
    <span class="section-divider__bar"></span>
  {/if}
</div>
