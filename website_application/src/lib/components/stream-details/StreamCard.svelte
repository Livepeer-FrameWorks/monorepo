<script lang="ts">
  import { resolve } from "$app/paths";
  import { getIconComponent } from "$lib/iconUtils";
  import HealthScoreIndicator from "$lib/components/health/HealthScoreIndicator.svelte";

  interface Stream {
    id: string;
    name?: string;
    playbackId?: string;
    status?: string;
    viewers?: number;
  }

  interface HealthData {
    healthScore: number;
    issuesDescription?: string;
  }

  interface Props {
    stream: Stream;
    selected: boolean;
    deleting: boolean;
    healthData: HealthData | null;
    onSelect: () => void;
    onDelete: () => void;
  }

  let { stream, selected, deleting, healthData, onSelect, onDelete }: Props =
    $props();

  const PlayIcon = getIconComponent("Play");
  const XIcon = getIconComponent("X");
</script>

<div
  class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter cursor-pointer transition-all hover:border-tokyo-night-cyan {selected
    ? 'border-tokyo-night-cyan bg-tokyo-night-bg-highlight'
    : ''}"
  role="button"
  tabindex="0"
  onclick={onSelect}
  onkeydown={(e) => e.key === "Enter" && onSelect()}
>
  <div class="flex items-center justify-between mb-3">
    <h3 class="font-semibold text-tokyo-night-fg truncate">
      {stream.name || `Stream ${stream.id.slice(0, 8)}`}
    </h3>
    <div class="flex items-center space-x-2">
      <div
        class="w-2 h-2 rounded-full {stream.status === 'live'
          ? 'bg-tokyo-night-green animate-pulse'
          : 'bg-tokyo-night-red'}"
      ></div>
      {#if stream.status === "live"}
        <a
          href={resolve(`/view?type=live&id=${stream.playbackId || stream.id}`)}
          class="text-tokyo-night-cyan hover:text-tokyo-night-blue text-sm p-1"
          onclick={(event) => event.stopPropagation()}
          title="Watch live stream"
        >
          <PlayIcon class="w-4 h-4" />
        </a>
      {/if}
      <button
        class="text-tokyo-night-red hover:text-red-400 text-sm"
        onclick={(event) => {
          event.stopPropagation();
          onDelete();
        }}
        disabled={deleting}
      >
        {#if deleting}
          ...
        {:else}
          <XIcon class="w-4 h-4" />
        {/if}
      </button>
    </div>
  </div>

  <div class="grid grid-cols-2 gap-4 text-sm mb-3">
    <div>
      <p class="text-tokyo-night-comment">Status</p>
      <p class="font-semibold text-tokyo-night-fg capitalize">
        {stream.status || "offline"}
      </p>
    </div>
    <div>
      <p class="text-tokyo-night-comment">Viewers</p>
      <p class="font-semibold text-tokyo-night-fg">
        {stream.viewers || 0}
      </p>
    </div>
  </div>

  <!-- Health Indicator -->
  {#if healthData}
    <div class="mb-3">
      <div class="flex items-center justify-between">
        <div class="flex items-center space-x-2">
          <HealthScoreIndicator
            healthScore={healthData.healthScore}
            size="sm"
            showLabel={false}
          />
          <span class="text-xs text-tokyo-night-comment">Health</span>
        </div>
        <a
          href={resolve(`/streams/${stream.id}/health`)}
          class="text-xs text-tokyo-night-cyan hover:text-tokyo-night-blue transition-colors"
          onclick={(event) => event.stopPropagation()}
        >
          View Details
        </a>
      </div>
      {#if healthData.issuesDescription}
        <p class="text-xs text-red-400 mt-1 truncate">
          {healthData.issuesDescription}
        </p>
      {/if}
    </div>
  {:else if stream.status === "live"}
    <div class="mb-3">
      <div class="flex items-center justify-between">
        <span class="text-xs text-tokyo-night-comment">Loading health...</span>
        <a
          href={resolve(`/streams/${stream.id}/health`)}
          class="text-xs text-tokyo-night-cyan hover:text-tokyo-night-blue transition-colors"
          onclick={(event) => event.stopPropagation()}
        >
          View Details
        </a>
      </div>
    </div>
  {/if}

  <div class="pt-3 border-t border-tokyo-night-fg-gutter">
    <p class="text-xs text-tokyo-night-comment truncate">
      ID: {stream.playbackId || stream.id.slice(0, 16)}
    </p>
  </div>
</div>
