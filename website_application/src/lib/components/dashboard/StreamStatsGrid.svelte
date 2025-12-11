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
    <div class="text-2xl font-bold text-foreground">
      {totalStreams}
    </div>
    <div class="text-xs text-muted-foreground">Total Streams</div>
  </div>
  <div class="text-center">
    <div class="text-2xl font-bold text-success">
      {liveStreams}
    </div>
    <div class="text-xs text-muted-foreground">Live Now</div>
  </div>
  <div class="text-center">
    <div class="text-2xl font-bold text-info">
      {totalViewers}
    </div>
    <div class="text-xs text-muted-foreground">Current Viewers</div>
  </div>
</div>
