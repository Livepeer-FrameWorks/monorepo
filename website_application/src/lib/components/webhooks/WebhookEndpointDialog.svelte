<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import {
    ALL_EVENT_TYPES,
    draftProblem,
    toggleEventType,
    type EndpointDraft,
  } from "$lib/webhooks";

  let {
    open = $bindable(false),
    mode,
    initial,
    eventTypes,
    saving = false,
    onSubmit,
  }: {
    open?: boolean;
    mode: "create" | "edit";
    initial: EndpointDraft;
    eventTypes: readonly string[];
    saving?: boolean;
    onSubmit: (draft: EndpointDraft) => void | Promise<void>;
  } = $props();

  let url = $state("");
  let description = $state("");
  let selected = $state<string[]>([]);
  let touched = $state(false);

  // Each opening starts from the caller's values.
  $effect(() => {
    if (open) {
      url = initial.url;
      description = initial.description;
      selected = [...initial.eventTypes];
      touched = false;
    }
  });

  const draft = $derived<EndpointDraft>({ url, description, eventTypes: selected });
  const problem = $derived(draftProblem(draft));
  const specificTypes = $derived(eventTypes.filter((type) => type !== ALL_EVENT_TYPES));

  function submit(event: SubmitEvent) {
    event.preventDefault();
    touched = true;
    if (problem) return;
    void onSubmit(draft);
  }
</script>

<Dialog {open} onOpenChange={(value) => (open = value)}>
  <DialogContent
    class="max-w-xl rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden"
  >
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground">
        {mode === "create" ? "Add Webhook Endpoint" : "Edit Webhook Endpoint"}
      </DialogTitle>
      <DialogDescription class="text-xs text-muted-foreground/70">
        FrameWorks POSTs each subscribed public event to this URL, signed with the Standard Webhooks
        scheme.
      </DialogDescription>
    </DialogHeader>

    <form
      id="webhook-endpoint-form"
      onsubmit={submit}
      class="slab-body--padded space-y-4 max-h-[60vh] overflow-y-auto"
    >
      <div class="space-y-2">
        <Label for="webhook-url" class="text-sm font-medium text-foreground">URL</Label>
        <Input
          id="webhook-url"
          type="url"
          bind:value={url}
          placeholder="https://example.com/webhooks/frameworks"
          class="font-mono text-sm"
          disabled={saving}
        />
        <p class="text-xs text-muted-foreground/70">
          An https URL of a public host. Private, loopback, and link-local addresses are refused.
        </p>
      </div>

      <div class="space-y-2">
        <Label for="webhook-description" class="text-sm font-medium text-foreground">
          Description
        </Label>
        <Input
          id="webhook-description"
          type="text"
          bind:value={description}
          placeholder="e.g. Production order service"
          disabled={saving}
        />
      </div>

      <fieldset class="space-y-2">
        <legend class="text-sm font-medium text-foreground">Event types</legend>
        <label class="flex items-center gap-2 text-sm">
          <Checkbox
            checked={selected.includes(ALL_EVENT_TYPES)}
            onCheckedChange={() => (selected = toggleEventType(selected, ALL_EVENT_TYPES))}
            disabled={saving}
          />
          <span>All event types <span class="font-mono text-xs text-muted-foreground">*</span></span
          >
        </label>
        {#if specificTypes.length === 0}
          <p class="text-xs text-muted-foreground">Loading event types…</p>
        {:else}
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-1">
            {#each specificTypes as type (type)}
              <label class="flex items-center gap-2 text-sm">
                <Checkbox
                  checked={selected.includes(type)}
                  onCheckedChange={() => (selected = toggleEventType(selected, type))}
                  disabled={saving}
                />
                <span class="font-mono text-xs">{type}</span>
              </label>
            {/each}
          </div>
        {/if}
      </fieldset>

      {#if touched && problem}
        <p role="alert" class="text-sm text-destructive">{problem}</p>
      {/if}
    </form>

    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)] text-muted-foreground hover:text-foreground"
        onclick={() => (open = false)}
        disabled={saving}
      >
        Cancel
      </Button>
      <Button
        type="submit"
        variant="ghost"
        form="webhook-endpoint-form"
        disabled={saving}
        class="rounded-none h-12 flex-1 text-primary hover:text-primary/80"
      >
        {#if saving}
          Saving…
        {:else}
          {mode === "create" ? "Add Endpoint" : "Save Changes"}
        {/if}
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
