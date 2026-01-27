<script lang="ts">
  import { cn } from "@livepeer-frameworks/player-core";
  import { buttonVariants, type ButtonSize, type ButtonVariant } from "./button";
  import { getContext, setContext } from "svelte";

  type $$Props = {
    variant?: ButtonVariant;
    size?: ButtonSize;
    class?: string;
    asChild?: boolean;
    type?: "button" | "submit" | "reset";
  } & Record<string, any>;

  let {
    variant = "default",
    size = "default",
    class: className = "",
    asChild = false,
    type = "button",
    ...rest
  }: $$Props = $props();

  let Comp: string = "button";
  if (asChild) {
    Comp = getContext("__svelte_slot_element") || "div";
  }

  // Set context for potential nested slots (though less common for Button)
  setContext("__svelte_slot_element", Comp);

  let mergedClasses = $derived(cn(buttonVariants(variant, size, className)));
</script>

{#if asChild}
  <svelte:element this={Comp} class={mergedClasses} {...rest}>
    <slot />
  </svelte:element>
{:else}
  <button class={mergedClasses} {type} {...rest}>
    <slot />
  </button>
{/if}
