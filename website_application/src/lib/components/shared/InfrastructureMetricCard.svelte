<script lang="ts">
  import { Card, CardContent } from "$lib/components/ui/card";
  import { Badge } from "$lib/components/ui/badge";

  interface Props {
    label: string;
    value: string | number;
    tone: string;
  }

  let { label, value, tone }: Props = $props();
</script>

<Card>
  <CardContent class="py-4 text-center space-y-2">
    <Badge variant="outline" class="mx-auto w-fit uppercase tracking-wide text-[0.65rem]">
      {label}
    </Badge>
    <span class="text-2xl font-semibold {tone}">{value}</span>
  </CardContent>
</Card>
