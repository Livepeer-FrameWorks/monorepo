<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDate } from "$lib/utils/stream-helpers";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import EmptyState from "$lib/components/EmptyState.svelte";

  interface PushTarget {
    id: string;
    streamId: string;
    platform?: string | null;
    name: string;
    targetUri: string;
    isEnabled: boolean;
    status: string;
    lastError?: string | null;
    lastPushedAt?: string | null;
    createdAt: string;
  }

  let {
    pushTargets = [],
    onAdd,
    onEdit,
    onToggle,
    onDelete,
    deleteLoading = null,
    toggleLoading = null,
  }: {
    pushTargets: PushTarget[];
    onAdd?: () => void;
    onEdit?: (target: PushTarget) => void;
    onToggle?: (target: PushTarget) => void;
    onDelete?: (id: string) => void;
    deleteLoading?: string | null;
    toggleLoading?: string | null;
  } = $props();

  const PLATFORM_ICONS: Record<string, string> = {
    twitch: "Tv",
    youtube: "Play",
    facebook: "Users",
    kick: "Zap",
    x: "AtSign",
  };

  const PLATFORM_LABELS: Record<string, string> = {
    twitch: "Twitch",
    youtube: "YouTube",
    facebook: "Facebook",
    kick: "Kick",
    x: "X",
  };

  function getPlatformIcon(platform?: string | null): string {
    return PLATFORM_ICONS[platform ?? ""] ?? "Radio";
  }

  function getPlatformLabel(platform?: string | null): string {
    return PLATFORM_LABELS[platform ?? ""] ?? "Custom";
  }

  function getStatusTone(status: string): "green" | "default" | "red" {
    if (status === "pushing") return "green";
    if (status === "failed") return "red";
    return "default";
  }

  function getStatusLabel(status: string): string {
    if (status === "pushing") return "Pushing";
    if (status === "failed") return "Failed";
    return "Idle";
  }

  const PlusIcon = getIconComponent("Plus");
  const EditIcon = getIconComponent("Edit");
  const TrashIcon = getIconComponent("Trash2");
  const LoaderIcon = getIconComponent("Loader");
  const ToggleLeftIcon = getIconComponent("ToggleLeft");
  const ToggleRightIcon = getIconComponent("ToggleRight");
</script>

<div class="dashboard-grid border-t border-[hsl(var(--tn-fg-gutter)/0.3)]">
  <div class="slab">
    <div class="slab-header flex items-center justify-between">
      <div>
        <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
          Multistream Targets
        </h3>
        <p class="text-xs text-muted-foreground/70 mt-1">
          Push your stream to external platforms automatically when you go live
        </p>
      </div>
      {#if onAdd}
        <Button variant="ghost" class="gap-2 text-primary hover:text-primary/80" onclick={onAdd}>
          <PlusIcon class="w-4 h-4" />
          Add Target
        </Button>
      {/if}
    </div>

    {#if pushTargets.length > 0}
      <div class="slab-body--flush">
        {#each pushTargets as target (target.id)}
          {@const PlatformIcon = getIconComponent(getPlatformIcon(target.platform))}
          <div class="p-6 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] last:border-0">
            <div class="flex items-start justify-between gap-4">
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-3 mb-2">
                  <PlatformIcon class="w-4 h-4 text-info shrink-0" />
                  <h5 class="font-medium text-foreground truncate">{target.name}</h5>
                  <Badge variant="outline" class="shrink-0">
                    {getPlatformLabel(target.platform)}
                  </Badge>
                  <Badge
                    variant={target.isEnabled ? "default" : "secondary"}
                    tone={target.isEnabled ? getStatusTone(target.status) : "default"}
                    class="shrink-0"
                  >
                    {#if !target.isEnabled}
                      Disabled
                    {:else}
                      {getStatusLabel(target.status)}
                    {/if}
                  </Badge>
                </div>

                <code class="block px-3 py-2 text-xs font-mono text-info bg-muted/20 truncate mb-2">
                  {target.targetUri}
                </code>

                <div class="text-xs text-muted-foreground space-x-2">
                  <span>Added {formatDate(target.createdAt)}</span>
                  {#if target.lastPushedAt}
                    <span>•</span>
                    <span>Last push {formatDate(target.lastPushedAt)}</span>
                  {/if}
                  {#if target.status === "failed" && target.lastError}
                    <span>•</span>
                    <span class="text-error">{target.lastError}</span>
                  {/if}
                </div>
              </div>

              <div class="flex items-center gap-1 shrink-0">
                <Button
                  variant="ghost"
                  size="icon-sm"
                  class="hover:bg-muted/50"
                  title={target.isEnabled ? "Disable" : "Enable"}
                  onclick={() => onToggle?.(target)}
                  disabled={toggleLoading === target.id}
                >
                  {#if toggleLoading === target.id}
                    <LoaderIcon class="w-4 h-4 animate-spin" />
                  {:else if target.isEnabled}
                    <ToggleRightIcon class="w-4 h-4 text-success" />
                  {:else}
                    <ToggleLeftIcon class="w-4 h-4 text-muted-foreground" />
                  {/if}
                </Button>

                <Button
                  variant="ghost"
                  size="icon-sm"
                  class="hover:bg-muted/50"
                  title="Edit"
                  onclick={() => onEdit?.(target)}
                >
                  <EditIcon class="w-4 h-4" />
                </Button>

                <Button
                  variant="destructive"
                  size="icon-sm"
                  onclick={() => onDelete?.(target.id)}
                  disabled={deleteLoading === target.id}
                  title="Delete"
                >
                  {#if deleteLoading === target.id}
                    <LoaderIcon class="w-4 h-4 animate-spin" />
                  {:else}
                    <TrashIcon class="w-4 h-4" />
                  {/if}
                </Button>
              </div>
            </div>
          </div>
        {/each}
      </div>
    {:else}
      <div class="slab-body--padded">
        <EmptyState
          iconName="Radio"
          title="No Push Targets"
          description="Add a multistream target to automatically restream to Twitch, YouTube, Kick, and more when you go live"
          actionText="Add Push Target"
          onAction={onAdd}
        />
      </div>
    {/if}
  </div>
</div>
