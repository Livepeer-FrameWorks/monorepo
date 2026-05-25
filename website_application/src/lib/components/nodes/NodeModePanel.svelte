<script lang="ts">
  import { SetNodeModeStore, type NodeOperationalMode$options } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { toast } from "$lib/stores/toast";
  import { CircleDot, GaugeCircle, Wrench, AlertTriangle } from "lucide-svelte";

  interface Props {
    nodeId: string;
    nodeName: string;
    effectiveMode: NodeOperationalMode$options;
    activeStreams: number;
    activeViewers: number;
    onModeChanged?: () => Promise<void> | void;
  }

  let { nodeId, nodeName, effectiveMode, activeStreams, activeViewers, onModeChanged }: Props =
    $props();

  const setModeMutation = new SetNodeModeStore();

  let pending = $state<NodeOperationalMode$options | null>(null);
  let reason = $state("");

  const MODES: Array<{
    mode: NodeOperationalMode$options;
    label: string;
    description: string;
    icon: typeof CircleDot;
    tone: "success" | "warning" | "destructive";
  }> = [
    {
      mode: "NORMAL",
      label: "Normal",
      description: "Accepts new viewers and ingest. Default operational state.",
      icon: CircleDot,
      tone: "success",
    },
    {
      mode: "DRAINING",
      label: "Draining",
      description:
        "Existing viewers continue; new viewers route elsewhere. Use before maintenance to bleed load gracefully.",
      icon: GaugeCircle,
      tone: "warning",
    },
    {
      mode: "MAINTENANCE",
      label: "Maintenance",
      description: "Hard out — node is removed from routing entirely. Existing sessions terminate.",
      icon: Wrench,
      tone: "destructive",
    },
  ];

  async function apply(mode: NodeOperationalMode$options) {
    if (mode === effectiveMode) {
      toast.info(`Already in ${mode.toLowerCase()}`);
      return;
    }
    pending = mode;
    try {
      const result = await setModeMutation.mutate({
        input: { nodeId, mode, reason: reason.trim() || undefined },
      });
      const data = result.data?.setNodeMode;
      if (!data) {
        toast.error("No response from server");
        return;
      }
      switch (data.__typename) {
        case "InfrastructureNode":
          toast.success(`${nodeName} is now ${mode.toLowerCase()}`);
          reason = "";
          await onModeChanged?.();
          break;
        case "ValidationError":
          toast.error(`${data.message}${data.field ? ` (${data.field})` : ""}`);
          break;
        case "NotFoundError":
          toast.error(data.message || "Node not found");
          break;
        case "AuthError":
          toast.error(data.message || "Not authorised");
          break;
      }
    } catch (err) {
      toast.error(`Failed to change mode: ${(err as Error).message}`);
    } finally {
      pending = null;
    }
  }

  function badgeClass(tone: "success" | "warning" | "destructive") {
    return tone === "success"
      ? "bg-success/15 text-success"
      : tone === "warning"
        ? "bg-warning/15 text-warning"
        : "bg-destructive/15 text-destructive";
  }
</script>

<section class="border border-border rounded-md p-4">
  <div class="flex items-center justify-between mb-3">
    <h3 class="text-base font-semibold">Operational mode</h3>
    {#each MODES as m (m.mode)}
      {#if m.mode === effectiveMode}
        <span class="text-xs px-2 py-0.5 rounded {badgeClass(m.tone)}">
          {m.label.toUpperCase()}
        </span>
      {/if}
    {/each}
  </div>

  {#if effectiveMode === "DRAINING" || effectiveMode === "MAINTENANCE"}
    <div
      class="border border-warning/30 bg-warning/5 rounded p-2 mb-3 text-xs flex items-start gap-2"
    >
      <AlertTriangle class="w-3.5 h-3.5 text-warning shrink-0 mt-0.5" />
      <div>
        Node is currently <strong>{effectiveMode.toLowerCase()}</strong>. Routing is steering around
        it. Restore to <strong>NORMAL</strong> to re-admit it.
      </div>
    </div>
  {/if}

  <p class="text-xs text-muted-foreground mb-3">
    Current live traffic on this node:
    <strong class="text-foreground">{activeStreams}</strong> streams,
    <strong class="text-foreground">{activeViewers}</strong> viewers.
  </p>

  <div class="space-y-2 mb-3">
    <Label for="mode-reason" class="text-xs">Reason (optional, recorded in audit)</Label>
    <Input
      id="mode-reason"
      placeholder="e.g. monthly maintenance window"
      bind:value={reason}
      disabled={pending !== null}
    />
  </div>

  <div class="grid grid-cols-1 xl:grid-cols-3 gap-2">
    {#each MODES as m (m.mode)}
      {@const Icon = m.icon}
      <Button
        variant={m.mode === effectiveMode ? "default" : "outline"}
        disabled={pending !== null || m.mode === effectiveMode}
        onclick={() => apply(m.mode)}
        class="h-auto w-full min-w-0 items-start justify-start whitespace-normal px-3 py-2.5 text-left"
      >
        <Icon class="w-4 h-4 mr-2 shrink-0" />
        <div class="min-w-0 flex-1">
          <div class="text-sm font-medium">{m.label}</div>
          <div class="break-words text-[10px] leading-tight text-muted-foreground">
            {m.description}
          </div>
        </div>
      </Button>
    {/each}
  </div>
</section>
