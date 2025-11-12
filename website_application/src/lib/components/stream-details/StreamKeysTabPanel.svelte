<script>
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDate } from "$lib/utils/stream-helpers";
  import { Button } from "$lib/components/ui/button";
  import EmptyState from "$lib/components/EmptyState.svelte";

  let {
    streamKeys,
    onCreateKey,
    onCopyKey,
    onDeleteKey,
    deleteLoading = null,
  } = $props();

  const PlusIcon = getIconComponent("Plus");
  const CopyIcon = getIconComponent("Copy");
  const LoaderIcon = getIconComponent("Loader");
  const TrashIcon = getIconComponent("Trash2");
</script>

<div>
  <div class="flex items-center justify-between mb-6">
    <h4 class="text-lg font-semibold gradient-text">Stream Keys Management</h4>
    <Button class="gap-2 hover:shadow-brand-soft" onclick={onCreateKey}>
      <PlusIcon class="w-4 h-4" />
      Create Key
    </Button>
  </div>

  {#if streamKeys.length > 0}
    <div class="space-y-4">
      {#each streamKeys as key (key.id ?? key.keyValue)}
        <div
          class="border border-tokyo-night-fg-gutter rounded-lg p-4 transition-all hover:border-tokyo-night-cyan/30 hover:shadow-brand-subtle"
        >
          <div class="flex items-center justify-between">
            <div class="flex-1">
              <div class="flex items-center space-x-3 mb-2">
                <h5 class="font-medium text-tokyo-night-fg">
                  {key.keyName}
                </h5>
                <span
                  class="badge {key.isActive ? 'badge-success' : 'badge-danger'}"
                >
                  {key.isActive ? "Active" : "Inactive"}
                </span>
              </div>

              <div class="flex items-center space-x-2 mb-2">
                <code
                  class="flex-1 px-3 py-2 bg-tokyo-night-bg-dark rounded-lg text-sm font-mono text-tokyo-night-cyan transition-all focus-within:ring-2 focus-within:ring-tokyo-night-blue"
                >
                  {key.keyValue}
                </code>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  class="bg-tokyo-night-bg-dark hover:bg-tokyo-night-selection"
                  onclick={() => onCopyKey(key.keyValue)}
                >
                  <CopyIcon class="w-4 h-4" />
                </Button>
              </div>

              <div class="text-sm text-tokyo-night-comment">
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
    <EmptyState
      icon="Key"
      title="No Stream Keys"
      description="Create your first stream key to start broadcasting"
      actionText="Create Stream Key"
      onAction={onCreateKey}
    />
  {/if}
</div>
