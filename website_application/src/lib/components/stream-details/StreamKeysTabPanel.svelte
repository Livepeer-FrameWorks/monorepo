<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDate } from "$lib/utils/stream-helpers";
  import { Button } from "$lib/components/ui/button";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { Badge } from "$lib/components/ui/badge";

  let { streamKeys, onCreateKey, onCopyKey, onDeleteKey, deleteLoading = null } = $props();

  const PlusIcon = getIconComponent("Plus");
  const CopyIcon = getIconComponent("Copy");
  const LoaderIcon = getIconComponent("Loader");
  const TrashIcon = getIconComponent("Trash2");
</script>

<div class="slab border-t border-[hsl(var(--tn-fg-gutter)/0.3)]">
  <div class="slab-header flex items-center justify-between">
    <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
      Stream Keys Management
    </h3>
    <Button variant="ghost" class="gap-2 text-primary hover:text-primary/80" onclick={onCreateKey}>
      <PlusIcon class="w-4 h-4" />
      Create Key
    </Button>
  </div>

  {#if streamKeys.length > 0}
    <div class="slab-body--flush">
      {#each streamKeys as key (key.id ?? key.keyValue)}
        <div class="p-6 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] last:border-0">
          <div class="flex items-center justify-between">
            <div class="flex-1">
              <div class="flex items-center space-x-3 mb-2">
                <h5 class="font-medium text-foreground">
                  {key.keyName}
                </h5>
                <Badge
                  variant={key.isActive ? "default" : "secondary"}
                  tone={key.isActive ? "green" : "default"}
                >
                  {key.isActive ? "Active" : "Inactive"}
                </Badge>
              </div>

              <div class="flex items-center space-x-2 mb-2">
                <code class="flex-1 px-3 py-2 text-sm font-mono text-info bg-muted/20">
                  {key.keyValue}
                </code>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  class="hover:bg-muted/50"
                  onclick={() => onCopyKey(key.keyValue)}
                >
                  <CopyIcon class="w-4 h-4" />
                </Button>
              </div>

              <div class="text-sm text-muted-foreground">
                Created: {formatDate(key.createdAt)}
                {#if key.lastUsedAt}
                  • Last used: {formatDate(key.lastUsedAt)}
                {/if}
              </div>
            </div>

            <Button
              variant="destructive"
              size="icon-sm"
              class="ml-4"
              onclick={() => onDeleteKey(key.id)}
              disabled={deleteLoading === key.id}
            >
              {#if deleteLoading === key.id}
                <LoaderIcon class="w-4 h-4 animate-spin" />
              {:else}
                <TrashIcon class="w-4 h-4" />
              {/if}
            </Button>
          </div>
        </div>
      {/each}
    </div>
  {:else}
    <div class="slab-body--padded">
      <EmptyState
        iconName="Key"
        title="No Stream Keys"
        description="Create your first stream key to start broadcasting"
        actionText="Create Stream Key"
        onAction={onCreateKey}
      />
    </div>
  {/if}
</div>
