<script>
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDate } from "$lib/utils/stream-helpers";

  let { stream, onCopy } = $props();

  const CopyIcon = getIconComponent("Copy");
</script>

<div
  class="glass-card p-6 transition-all hover:shadow-brand-subtle hover:scale-[1.01]"
>
  <h3 class="text-lg font-semibold gradient-text mb-4">Playback Info</h3>
  <div class="space-y-3">
    <div>
      <span class="text-sm text-tokyo-night-comment">Playback ID</span>
      <div class="flex items-center space-x-2 mt-1">
        <code
          class="flex-1 px-3 py-2 bg-tokyo-night-bg-dark rounded-lg text-sm font-mono text-tokyo-night-cyan border border-tokyo-night-fg-gutter transition-all focus-within:ring-2 focus-within:ring-tokyo-night-blue"
        >
          {stream?.playbackId || "Not set"}
        </code>
        <button
          onclick={() => onCopy?.(stream?.playbackId)}
          class="p-2 rounded-lg bg-tokyo-night-bg-dark hover:bg-tokyo-night-selection transition-all hover:shadow-brand-subtle"
        >
          <CopyIcon class="w-4 h-4" />
        </button>
      </div>
    </div>
    <div class="text-sm text-tokyo-night-comment">
      Created: {formatDate(stream?.createdAt)}
    </div>
  </div>
</div>
