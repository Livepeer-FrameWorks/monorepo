<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Alert, AlertDescription } from "$lib/components/ui/alert";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import { getIconComponent } from "$lib/iconUtils";
  import { toast } from "$lib/stores/toast";
  import type { RevealedSecret } from "$lib/webhooks";

  let {
    secret,
    onDismiss,
  }: {
    /** The secret to show; null closes the dialog. */
    secret: RevealedSecret | null;
    /** Called when the user closes the dialog; the caller drops the secret. */
    onDismiss: () => void;
  } = $props();

  const CopyIcon = getIconComponent("Copy");

  async function copy() {
    if (!secret) return;
    try {
      await navigator.clipboard.writeText(secret.secret);
      toast.success("Signing secret copied");
    } catch {
      toast.error("Copy failed; select the secret and copy it manually.");
    }
  }
</script>

<Dialog
  open={secret !== null}
  onOpenChange={(value) => {
    if (!value) onDismiss();
  }}
>
  <DialogContent
    class="max-w-xl rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden"
  >
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground">
        {secret?.reason === "rotated" ? "New Signing Secret" : "Endpoint Signing Secret"}
      </DialogTitle>
      <DialogDescription class="text-xs text-muted-foreground/70 break-all">
        {secret?.url}
      </DialogDescription>
    </DialogHeader>

    {#if secret}
      <div class="slab-body--padded space-y-4">
        <div class="flex items-stretch gap-2">
          <code
            class="flex-1 break-all bg-muted px-3 py-2 font-mono text-xs text-foreground select-all"
            data-testid="webhook-secret">{secret.secret}</code
          >
          <Button variant="outline" class="gap-2 self-start" onclick={copy}>
            <CopyIcon class="w-4 h-4" />
            Copy
          </Button>
        </div>
        <Alert variant="warning">
          <AlertDescription>
            <strong>Copy the secret now.</strong> It is shown once and never returned again. Verify
            each delivery's <code>webhook-signature</code> header with it.
            {#if secret.reason === "rotated"}
              Until the previous secret expires, deliveries carry a signature for each secret, so
              your receiver can switch over without dropping events.
            {/if}
          </AlertDescription>
        </Alert>
      </div>
    {/if}

    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 text-primary hover:text-primary/80"
        onclick={onDismiss}
      >
        I've stored the secret
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
