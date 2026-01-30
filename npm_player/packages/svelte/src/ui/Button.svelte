<script lang="ts">
  import type { Snippet } from "svelte";
  import { cn } from "@livepeer-frameworks/player-core";
  import { buttonVariants, type ButtonSize, type ButtonVariant } from "./button";
  import { getContext } from "svelte";

  type Props = {
    variant?: ButtonVariant;
    size?: ButtonSize;
    class?: string;
    asChild?: boolean;
    type?: "button" | "submit" | "reset";
    children?: Snippet;
    [key: string]: unknown;
  };

  let {
    variant = "default",
    size = "default",
    class: className = "",
    asChild = false,
    type = "button",
    children,
    ...rest
  }: Props = $props();

  let Comp = $derived(
    asChild ? (getContext("__svelte_slot_element") as string) || "div" : "button"
  );

  let mergedClasses = $derived(cn(buttonVariants(variant, size, className)));
</script>

{#if asChild}
  <svelte:element this={Comp} class={mergedClasses} {...rest}>
    {@render children?.()}
  </svelte:element>
{:else}
  <button class={mergedClasses} {type} {...rest}>
    {@render children?.()}
  </button>
{/if}
