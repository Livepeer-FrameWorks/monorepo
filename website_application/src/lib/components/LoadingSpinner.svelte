<script lang="ts">
  import { cn } from "$lib/utils";

  interface Props {
    size?: "sm" | "md" | "lg" | "xl";
    color?: string;
    class?: string;
  }

  let { size = "md", color = "text-primary", class: className }: Props = $props();

  const sizeClasses = {
    sm: "w-4 h-4",
    md: "w-6 h-6",
    lg: "w-8 h-8",
    xl: "w-12 h-12",
  };
</script>

<div
  class={cn(
    "inline-block border-2 border-current border-t-transparent rounded-full animate-spin",
    sizeClasses[size],
    color,
    className
  )}
  role="status"
  aria-label="Loading"
>
  <span class="sr-only">Loading...</span>
</div>
