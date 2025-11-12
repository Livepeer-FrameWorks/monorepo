<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";

  interface Stream {
    streamKey?: string;
    playbackId?: string;
    resolution?: string;
  }

  interface RealTimeMetrics {
    bandwidth?: number;
  }

  interface Props {
    selectedStream: Stream;
    isLive: boolean;
    viewers: number;
    realTimeMetrics: RealTimeMetrics;
    formatBandwidth: (bandwidth: number | undefined) => string;
  }

  let {
    selectedStream,
    isLive,
    viewers,
    realTimeMetrics,
    formatBandwidth,
  }: Props = $props();

  const UsersIcon = getIconComponent("Users");
  const BarChartIcon = getIconComponent("BarChart3");
  const MonitorIcon = getIconComponent("Monitor");
  const KeyIcon = getIconComponent("Key");
  const PlayIcon = getIconComponent("Play");
</script>

<div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-6 gap-6 mb-8">
  <div class="metric-card">
    <div class="flex items-center justify-between">
      <div>
        <p class="text-sm text-tokyo-night-comment">Stream Status</p>
        <p
          class="text-2xl font-bold {isLive
            ? 'text-tokyo-night-green'
            : 'text-tokyo-night-red'}"
        >
          {isLive ? "Live" : "Offline"}
        </p>
      </div>
      <div
        class="w-3 h-3 {isLive
          ? 'bg-tokyo-night-green animate-pulse'
          : 'bg-tokyo-night-red'} rounded-full"
      ></div>
    </div>
  </div>

  <div class="metric-card">
    <div class="flex items-center justify-between">
      <div>
        <p class="text-sm text-tokyo-night-comment">Current Viewers</p>
        <p class="text-2xl font-bold text-tokyo-night-fg">{viewers}</p>
      </div>
      <UsersIcon class="w-6 h-6" />
    </div>
  </div>

  <div class="metric-card">
    <div class="flex items-center justify-between">
      <div>
        <p class="text-sm text-tokyo-night-comment">Bandwidth</p>
        <p class="text-sm font-bold text-tokyo-night-fg">
          {formatBandwidth(realTimeMetrics.bandwidth)}
        </p>
      </div>
      <BarChartIcon class="w-8 h-8 text-tokyo-night-blue" />
    </div>
  </div>

  <div class="metric-card">
    <div class="flex items-center justify-between">
      <div>
        <p class="text-sm text-tokyo-night-comment">Resolution</p>
        <p class="text-lg font-bold text-tokyo-night-fg">
          {selectedStream?.resolution || "N/A"}
        </p>
      </div>
      <MonitorIcon class="w-8 h-8 text-tokyo-night-purple" />
    </div>
  </div>

  <div class="metric-card">
    <div class="flex items-center justify-between">
      <div>
        <p class="text-sm text-tokyo-night-comment">Stream Key</p>
        <p class="text-sm font-mono text-tokyo-night-fg">
          {selectedStream?.streamKey
            ? `${selectedStream.streamKey.slice(0, 8)}...`
            : "No stream"}
        </p>
      </div>
      <KeyIcon class="w-8 h-8 text-tokyo-night-yellow" />
    </div>
  </div>

  <div class="metric-card">
    <div class="flex items-center justify-between">
      <div>
        <p class="text-sm text-tokyo-night-comment">Playback ID</p>
        <p class="text-sm font-mono text-tokyo-night-fg">
          {selectedStream?.playbackId
            ? `${selectedStream.playbackId.slice(0, 8)}...`
            : "No stream"}
        </p>
      </div>
      <PlayIcon class="w-8 h-8 text-tokyo-night-green" />
    </div>
  </div>
</div>
