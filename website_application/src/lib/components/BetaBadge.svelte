<script lang="ts">
  import { Badge } from "$lib/components/ui/badge";

  interface Props {
    label?: string;
    className?: string;
    variant?: import("$lib/components/ui/badge").BadgeVariant;
    tone?: import("$lib/components/ui/badge").BadgeTone;
  }

  let {
    label = "Public Beta",
    className = "",
    variant = "secondary",
    tone = undefined,
  }: Props = $props();
</script>

<Badge class={`uppercase tracking-wide ${className}`} variant={variant} tone={tone}>
  {label}
</Badge>
