<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { getIconComponent } from "$lib/iconUtils";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";

  interface StreamKey {
    id: string;
    keyName?: string;
    keyValue: string;
    createdAt: string;
    isActive: boolean;
    lastUsedAt?: string;
  }

  interface Props {
    streamKeys: StreamKey[];
    loadingStreamKeys: boolean;
    deletingKeyId: string | null;
    copiedUrl: string;
    onCreateKey: () => void;
    onDeleteKey: (keyId: string) => void;
    onCopy: (value: string) => void;
  }

  let {
    streamKeys,
    loadingStreamKeys,
    deletingKeyId,
    copiedUrl,
    onCreateKey,
    onDeleteKey,
    onCopy,
  }: Props = $props();

  const PlusIcon = getIconComponent("Plus");
  const KeyIcon = getIconComponent("Key");
  const Loader2Icon = getIconComponent("Loader2");
  const Trash2Icon = getIconComponent("Trash2");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const CopyIcon = getIconComponent("Copy");
</script>

<div class="space-y-6">
  <div class="flex items-center justify-between">
    <div>
      <h3 class="text-lg font-semibold text-tokyo-night-fg">
        Stream Keys Management
      </h3>
      <p class="text-tokyo-night-fg-dark text-sm">
        Create and manage multiple stream keys for different streaming setups
      </p>
    </div>
    <Button class="gap-2" onclick={onCreateKey}>
      <PlusIcon class="w-4 h-4" />
      Create Key
    </Button>
  </div>

  {#if loadingStreamKeys}
    <div class="space-y-4">
      {#each Array.from({ length: 3 }) as _, index (index)}
        <LoadingCard variant="stream" />
      {/each}
    </div>
  {:else if streamKeys.length === 0}
    <EmptyState
      iconName="Key"
      title="No Stream Keys"
      description="Create your first stream key to start broadcasting"
      actionText="Create Stream Key"
      onAction={onCreateKey}
    />
  {:else}
    <div class="space-y-4">
      {#each streamKeys as key (key.id ?? key.keyValue)}
        <div
          class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
        >
          <div class="flex items-center justify-between mb-3">
            <div class="flex items-center space-x-3">
              <KeyIcon class="w-5 h-5 text-tokyo-night-yellow" />
              <div>
                <h4 class="font-semibold text-tokyo-night-fg">
                  {key.keyName || "Unnamed Key"}
                </h4>
                <p class="text-xs text-tokyo-night-comment">
                  Created {new Date(key.createdAt).toLocaleDateString()}
                </p>
              </div>
            </div>

            <div class="flex items-center space-x-2">
              <span
                class="px-2 py-1 text-xs rounded {key.isActive
                  ? 'bg-tokyo-night-green bg-opacity-20 text-tokyo-night-green'
                  : 'bg-tokyo-night-red bg-opacity-20 text-tokyo-night-red'}"
              >
                {key.isActive ? "Active" : "Inactive"}
              </span>
              <Button
                variant="ghost"
                size="icon-sm"
                class="text-tokyo-night-red hover:text-red-400"
                onclick={() => onDeleteKey(key.id)}
                disabled={deletingKeyId === key.id}
              >
                {#if deletingKeyId === key.id}
                  <Loader2Icon class="w-4 h-4 animate-spin" />
                {:else}
                  <Trash2Icon class="w-4 h-4" />
                {/if}
              </Button>
            </div>
          </div>

          <div class="flex items-center space-x-3">
            <Input
              type="text"
              value={key.keyValue}
              readonly
              class="flex-1 font-mono text-sm"
            />
            <Button
              variant="outline"
              size="sm"
              onclick={() => onCopy(key.keyValue)}
            >
              {#if copiedUrl === key.keyValue}
                <CheckCircleIcon class="w-4 h-4" />
              {:else}
                <CopyIcon class="w-4 h-4" />
              {/if}
            </Button>
          </div>

          {#if key.lastUsedAt}
            <p class="text-xs text-tokyo-night-comment mt-2">
              Last used: {new Date(key.lastUsedAt).toLocaleString()}
            </p>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>
