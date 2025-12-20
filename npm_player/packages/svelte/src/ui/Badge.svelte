<script lang="ts">
  import { cn } from '@livepeer-frameworks/player-core';
  import { badgeVariants, type BadgeVariant } from './badge';

  type $$Props = {
    variant?: BadgeVariant;
    class?: string;
  } & Record<string, any>;

  let {
    variant = "default",
    class: className = "",
    ...rest
  }: $$Props = $props();

  let mergedClasses = $derived(cn(badgeVariants(variant, className)));
</script>

<div class={mergedClasses} {...rest}>
  <slot />
</div>
