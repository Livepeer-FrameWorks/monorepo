<script lang="ts">
  import { Badge } from "$lib/components/ui/badge";

  interface Props {
    label?: string;
    className?: string;
    variant?: import("$lib/components/ui/badge").BadgeVariant;
    tone?: import("$lib/components/ui/badge").BadgeTone;
  }

  // Note: kept component name for minimal touch points; default label now reflects alpha stage.
  let {
    label = "Public Alpha",
    className = "",
    variant = "secondary",
    tone = undefined,
  }: Props = $props();
</script>

<Badge class={`uppercase tracking-wide ${className}`} {variant} {tone}>
  {label}
</Badge>
