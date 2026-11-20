<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";

  let {
    open = $bindable(false),
    action = "disconnect",
    loading = false,
    onConfirm,
  }: {
    open?: boolean;
    action?: "disconnect" | "rotate";
    loading?: boolean;
    onConfirm: (endActiveSession: boolean) => void;
  } = $props();
  let endActiveSession = $state(false);
  $effect(() => {
    if (open) endActiveSession = false;
  });
</script>

<Dialog
  {open}
  onOpenChange={(value) => {
    if (!loading) open = value;
  }}
>
  <DialogContent>
    <DialogHeader>
      <DialogTitle>{action === "rotate" ? "Rotate stream key" : "Disconnect encoder"}</DialogTitle>
      <DialogDescription>
        {#if action === "rotate"}
          Replace this stream's key. Update your encoder before its next connection.
        {:else}
          End the current ingest session. An encoder with this stream's key can reconnect.
        {/if}
      </DialogDescription>
    </DialogHeader>
    {#if action === "rotate"}
      <label class="flex items-start gap-2 text-sm">
        <input type="checkbox" bind:checked={endActiveSession} disabled={loading} class="mt-1" />
        <span>Also disconnect the current encoder. It must use the new key to reconnect.</span>
      </label>
    {/if}
    <DialogFooter>
      <Button variant="outline" disabled={loading} onclick={() => (open = false)}>Cancel</Button>
      <Button
        variant="destructive"
        disabled={loading}
        onclick={() => onConfirm(action === "disconnect" || endActiveSession)}
      >
        {loading ? "Requesting…" : action === "rotate" ? "Rotate key" : "Disconnect encoder"}
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
