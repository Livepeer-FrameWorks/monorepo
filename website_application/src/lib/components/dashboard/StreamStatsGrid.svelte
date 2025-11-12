<script lang="ts">
  interface Props {
    totalStreams: number;
    liveStreams: number;
    totalViewers: string;
  }

  let { totalStreams, liveStreams, totalViewers }: Props = $props();
</script>

<div class="grid grid-cols-3 gap-4">
  <div class="text-center">
    <div class="text-2xl font-bold text-tokyo-night-fg">
      {totalStreams}
    </div>
    <div class="text-xs text-tokyo-night-comment">Total Streams</div>
  </div>
  <div class="text-center">
    <div class="text-2xl font-bold text-tokyo-night-green">
      {liveStreams}
    </div>
    <div class="text-xs text-tokyo-night-comment">Live Now</div>
  </div>
  <div class="text-center">
    <div class="text-2xl font-bold text-tokyo-night-cyan">
      {totalViewers}
    </div>
    <div class="text-xs text-tokyo-night-comment">Current Viewers</div>
  </div>
</div>
