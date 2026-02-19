<script lang="ts">
  import { getContext } from "svelte";
  import type { Readable } from "svelte/store";
  import {
    createStudioTranslator,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import type { IngestState, ReconnectionState } from "@livepeer-frameworks/streamcrafter-core";

  interface Props {
    state?: IngestState;
    reconnectionState?: ReconnectionState | null;
    isReconnecting?: boolean;
  }

  let {
    state: propState,
    reconnectionState: propReconnState,
    isReconnecting: propIsReconn,
  }: Props = $props();

  let pc: any = getContext("fw-sc-controller");
  const translatorCtx = getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator");
  const fallbackT = createStudioTranslator({ locale: "en" });
  let t: StudioTranslateFn = $derived(translatorCtx ? $translatorCtx : fallbackT);

  let state = $derived(propState ?? pc?.state ?? "idle");
  let reconnState = $derived(propReconnState ?? pc?.reconnectionState);
  let isReconn = $derived(propIsReconn ?? pc?.isReconnecting ?? false);

  let statusText = $derived.by(() => {
    if (reconnState?.isReconnecting)
      return t("reconnectingAttempt", { attempt: reconnState.attemptNumber, max: 5 });
    switch (state) {
      case "idle":
        return t("idle");
      case "requesting_permissions":
        return t("requestingPermissions");
      case "capturing":
        return t("ready");
      case "connecting":
        return t("connecting");
      case "streaming":
        return t("live");
      case "reconnecting":
        return t("reconnecting");
      case "error":
        return t("error");
      case "destroyed":
        return t("destroyed");
      default:
        return state;
    }
  });

  let badgeClass = $derived.by(() => {
    if (state === "streaming") return "fw-sc-badge fw-sc-badge--live";
    if (isReconn) return "fw-sc-badge fw-sc-badge--connecting";
    if (state === "error") return "fw-sc-badge fw-sc-badge--error";
    if (state === "capturing") return "fw-sc-badge fw-sc-badge--ready";
    return "fw-sc-badge fw-sc-badge--idle";
  });
</script>

<span class={badgeClass}>{statusText}</span>
