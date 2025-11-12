<script lang="ts">
  import { getIconComponent } from '$lib/iconUtils';
  import { Button } from "$lib/components/ui/button";
  import type { Snippet } from "svelte";

  interface Props {
    iconName?: string;
    title?: string;
    description?: string;
    actionText?: string;
    onAction?: () => void;
    size?: "sm" | "md" | "lg";
    variant?: "default" | "accent" | "subtle";
    buttonVariant?: "default" | "cta" | "outline" | "ghost" | "secondary" | "destructive";
    class?: string;
    showAction?: boolean;
    children?: Snippet;
  }

  let {
    iconName = "FileText",
    title = "No data found",
    description = "",
    actionText = "",
    onAction = () => {},
    size = "md",
    variant = "default",
    buttonVariant = "cta",
    class: className = "",
    showAction = true,
    children,
  }: Props = $props();

  const iconComponent = $derived(getIconComponent(iconName));

  const sizeClasses = {
    sm: {
      container: "py-8",
      iconWrapper: "w-12 h-12 mb-3",
      icon: "w-6 h-6",
      title: "text-lg font-semibold mb-1",
      description: "text-sm mb-4",
    },
    md: {
      container: "py-12",
      iconWrapper: "w-16 h-16 mb-5",
      icon: "w-8 h-8",
      title: "text-xl font-semibold mb-2",
      description: "text-sm mb-6",
    },
    lg: {
      container: "py-16",
      iconWrapper: "w-20 h-20 mb-6",
      icon: "w-10 h-10",
      title: "text-2xl font-bold mb-3",
      description: "text-base mb-8",
    },
  };

  const variantClasses = {
    default: {
      iconWrapper:
        "bg-tokyo-night-bg-highlight border border-tokyo-night-fg-gutter",
      icon: "text-tokyo-night-fg-dark",
    },
    accent: {
      iconWrapper:
        "bg-gradient-to-br from-tokyo-night-blue/20 to-tokyo-night-cyan/20 border border-tokyo-night-blue/30",
      icon: "text-tokyo-night-cyan",
    },
    subtle: {
      iconWrapper: "bg-tokyo-night-bg-dark border border-tokyo-night-fg-gutter/50",
      icon: "text-tokyo-night-comment",
    },
  };

  const classes = $derived(sizeClasses[size]);
  const variantClass = $derived(variantClasses[variant]);
  const SvelteComponent = $derived(iconComponent);
</script>

<div class="text-center {classes.container} {className}">
  <!-- Icon with styled wrapper -->
  <div
    class="mx-auto flex items-center justify-center rounded-2xl {classes.iconWrapper} {variantClass.iconWrapper}"
  >
    <SvelteComponent class="{classes.icon} {variantClass.icon}" />
  </div>

  <!-- Title -->
  <h3 class="text-tokyo-night-fg {classes.title}">
    {title}
  </h3>

  <!-- Description -->
  {#if description}
    <p class="text-tokyo-night-fg-dark {classes.description} max-w-md mx-auto">
      {description}
    </p>
  {/if}

  <!-- Action Button -->
  {#if actionText && showAction}
    <Button variant={buttonVariant} onclick={onAction}>
      {actionText}
    </Button>
  {/if}

  <!-- Custom slot for additional content -->
  {@render children?.()}
</div>
