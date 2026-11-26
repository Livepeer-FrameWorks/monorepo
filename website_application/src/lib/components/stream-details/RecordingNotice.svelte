<script lang="ts">
  import { resolve } from "$app/paths";
  import { GetStreamRecordingEventsStore } from "$houdini";
  import { recordingNotice, type RecordingStateEvent } from "$lib/utils/recording-notice";
  import { toast } from "$lib/stores/toast.js";
  let {
    streamId,
    enabled,
    isLive,
    connectedAt = null,
  }: {
    streamId: string;
    enabled: boolean;
    isLive: boolean;
    connectedAt?: string | null;
  } = $props();
  const history = new GetStreamRecordingEventsStore();
  let events = $state<RecordingStateEvent[]>([]);
  let unavailable = $state(false);
  const notice = $derived(recordingNotice(events, enabled, isLive, connectedAt, unavailable));
  $effect(() => {
    const id = streamId;
    const live = isLive;
    const wanted = enabled;
    const generation = connectedAt;
    events = [];
    unavailable = false;
    if (!id || !wanted) return;
    let active = true;
    let sequence = 0;
    async function refresh() {
      const request = ++sequence;
      try {
        const result = await history.fetch({ policy: "NetworkOnly", variables: { streamId: id } });
        if (!active || request !== sequence) return;
        if (
          result.errors?.length ||
          !result.data?.analytics?.lifecycle?.recordingEventsConnection
        ) {
          unavailable = true;
          return;
        }
        events = result.data.analytics.lifecycle.recordingEventsConnection.edges.map((e) => e.node);
        unavailable = false;
        const next = recordingNotice(events, wanted, live, generation);
        try {
          if (next && typeof sessionStorage !== "undefined") {
            const key = `frameworks:recording:${id}:${generation ?? "last"}`;
            const previous = sessionStorage.getItem(key);
            if (next.state === "blocked" && previous !== "blocked")
              toast.error(
                "Automatic recording could not start. Open the stream’s recording notice."
              );
            if (next.state === "recording" && previous === "blocked")
              toast.success("Recording is capturing media again. Earlier footage may be missing.");
            sessionStorage.setItem(key, next.state);
          }
        } catch {
          /* Browser storage is optional for duplicate-toast suppression. */
        }
      } catch {
        if (active && request === sequence) unavailable = true;
      }
    }
    void refresh();
    const timer = setInterval(() => void refresh(), live ? 15000 : 60000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  });
</script>

{#if notice}
  <div
    class={`border-b px-4 py-3 text-sm ${notice.state === "blocked" ? "border-destructive bg-destructive/10 text-destructive" : ""}`}
    role={notice.state === "blocked" ? "alert" : "status"}
  >
    {#if notice.state === "blocked"}
      <p class="font-medium">
        {notice.historical
          ? "The last broadcast had a recording problem"
          : "Automatic recording could not start"}
      </p>
      <p>
        {notice.storage
          ? "The storage limit was reached. Review storage or enable funded usage in Billing."
          : "Recording has not been confirmed. We keep retrying while the broadcast is live."} Footage
        missed before capture starts cannot be recovered.
      </p>
      {#if notice.at}<p class="mt-1 text-xs">
          Last reported {new Date(notice.at).toLocaleString()}.
        </p>{/if}
      <div class="mt-2 flex gap-4">
        <a class="underline" href={resolve("/account/storage")}>Review storage</a><a
          class="underline"
          href={resolve("/account/billing")}>Review billing</a
        >
      </div>
    {:else if notice.state === "recording"}<p>Recording is capturing media.</p>
    {:else if notice.state === "unknown"}<p>
        Recording status is unavailable. We cannot confirm capture right now and will retry.
      </p>
    {:else}<p>
        Automatic recording is enabled. Waiting for confirmation that media is being captured.
      </p>{/if}
  </div>
{/if}
