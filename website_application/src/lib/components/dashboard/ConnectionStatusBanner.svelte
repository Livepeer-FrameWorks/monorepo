<script lang="ts">
  interface Props {
    visible: boolean;
    message: string;
  }

  let { visible, message }: Props = $props();
</script>

{#if visible}
  <div
    class="bg-warning/10 border border-warning/30 p-3"
  >
    <div class="flex items-center space-x-2 text-sm">
      <div
        class="w-2 h-2 rounded-full bg-warning animate-pulse"
      ></div>
      <span class="text-warning">{message}</span>
    </div>
  </div>
{/if}
