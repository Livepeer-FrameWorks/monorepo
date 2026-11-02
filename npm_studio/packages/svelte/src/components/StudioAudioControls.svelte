<!--
  The audio section of the studio: the browser's own processing of each
  microphone, and the noise gate, de-esser, EQ and ducking on the mixed audio.
  Reads the controller from the StreamCrafter context.
-->
<script lang="ts">
  import { getContext } from "svelte";
  import { readable, type Readable } from "svelte/store";
  import {
    createStudioTranslator,
    getAudioConstraints,
    type AudioProcessingConfig,
    type IngestControllerV2,
    type MediaSource,
    type QualityProfile,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import type { AudioProcessingSettings } from "./AdvancedPanel.svelte";

  interface Props {
    /** Browser processing of the microphones; kept here when not given. */
    browserProcessing?: AudioProcessingSettings;
    onBrowserProcessingChange?: (settings: Partial<AudioProcessingSettings>) => void;
    class?: string;
  }

  let {
    browserProcessing: browserProp,
    onBrowserProcessingChange,
    class: className = "",
  }: Props = $props();

  const GATE_THRESHOLD_DEFAULT = -40;
  const BROWSER_KEYS = ["echoCancellation", "noiseSuppression", "autoGainControl"] as const;

  const pc = getContext<
    | {
        sources: MediaSource[];
        qualityProfile: QualityProfile;
        getController: () => IngestControllerV2 | null;
      }
    | undefined
  >("fw-sc-controller");
  const fallbackT = createStudioTranslator({ locale: "en" });
  const translatorCtx =
    getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator") ?? readable(fallbackT);
  let t = $derived($translatorCtx);

  const defaults = getAudioConstraints(pc?.qualityProfile ?? "broadcast");
  let ownBrowser = $state<AudioProcessingSettings>({
    echoCancellation: defaults.echoCancellation,
    noiseSuppression: defaults.noiseSuppression,
    autoGainControl: defaults.autoGainControl,
  });
  let browser = $derived(browserProp ?? ownBrowser);

  let processing = $state<Omit<AudioProcessingConfig, "sidechain">>(
    pc?.getController()?.getAudioProcessing() ?? {}
  );
  let ducking = $state(pc?.getController()?.isAudioDuckingEnabled() ?? false);
  let gate = $derived(processing.noiseGate);
  let eq = $derived(processing.eq);

  function changeBrowser(settings: Partial<AudioProcessingSettings>) {
    if (onBrowserProcessingChange) {
      onBrowserProcessingChange(settings);
      return;
    }
    ownBrowser = { ...ownBrowser, ...settings };
    for (const source of pc?.sources ?? []) {
      for (const track of source.stream.getAudioTracks()) {
        track.applyConstraints(ownBrowser).catch((err) => {
          console.warn("Failed to apply audio constraints:", err);
        });
      }
    }
  }

  function change(config: Omit<AudioProcessingConfig, "sidechain">) {
    const controller = pc?.getController();
    controller?.setAudioProcessing(config);
    processing = controller?.getAudioProcessing() ?? {};
  }

  function setDucking(enabled: boolean) {
    pc?.getController()?.setAudioDucking(enabled);
    ducking = enabled;
  }

  function checked(e: Event): boolean {
    return (e.target as HTMLInputElement).checked;
  }

  function value(e: Event): number {
    return Number((e.target as HTMLInputElement).value);
  }
</script>

<details class="fw-sc-audio {className}">
  <summary class="fw-sc-audio-summary">{t("audio")}</summary>
  <div class="fw-sc-audio-body">
    <fieldset class="fw-sc-audio-group">
      <legend>{t("browserProcessing")}</legend>
      {#each BROWSER_KEYS as key (key)}
        <label class="fw-sc-audio-toggle">
          <input
            type="checkbox"
            checked={browser[key]}
            onchange={(e) => changeBrowser({ [key]: checked(e) })}
          />
          {t(key)}
        </label>
      {/each}
    </fieldset>

    <fieldset class="fw-sc-audio-group">
      <legend>{t("processing")}</legend>
      <label class="fw-sc-audio-toggle">
        <input
          type="checkbox"
          checked={gate?.enabled ?? false}
          onchange={(e) =>
            change({
              noiseGate: {
                enabled: checked(e),
                threshold: gate?.threshold ?? GATE_THRESHOLD_DEFAULT,
              },
            })}
        />
        {t("noiseGate")}
      </label>
      <label class="fw-sc-audio-field">
        <span>{t("noiseGateThreshold")}</span>
        <input
          type="range"
          min="-80"
          max="0"
          step="1"
          value={gate?.threshold ?? GATE_THRESHOLD_DEFAULT}
          disabled={!gate?.enabled}
          oninput={(e) => change({ noiseGate: { enabled: true, threshold: value(e) } })}
        />
        <span class="fw-sc-audio-value">{gate?.threshold ?? GATE_THRESHOLD_DEFAULT} dB</span>
      </label>
      <label class="fw-sc-audio-toggle">
        <input
          type="checkbox"
          checked={processing.deEsser?.enabled ?? false}
          onchange={(e) => change({ deEsser: { enabled: checked(e) } })}
        />
        {t("deEsser")}
      </label>
      <label class="fw-sc-audio-toggle">
        <input
          type="checkbox"
          checked={eq?.enabled ?? false}
          onchange={(e) =>
            change({
              eq: {
                enabled: checked(e),
                low: eq?.low ?? 0,
                mid: eq?.mid ?? 0,
                high: eq?.high ?? 0,
              },
            })}
        />
        {t("equalizer")}
      </label>
      {#each [["low", t("eqLow")], ["mid", t("eqMid")], ["high", t("eqHigh")]] as const as [band, label] (band)}
        <label class="fw-sc-audio-field">
          <span>{label}</span>
          <input
            type="range"
            min="-12"
            max="12"
            step="1"
            value={eq?.[band] ?? 0}
            disabled={!eq?.enabled}
            oninput={(e) => change({ eq: { enabled: true, [band]: value(e) } })}
          />
          <span class="fw-sc-audio-value">{eq?.[band] ?? 0} dB</span>
        </label>
      {/each}
      <label class="fw-sc-audio-toggle">
        <input type="checkbox" checked={ducking} onchange={(e) => setDucking(checked(e))} />
        {t("ducking")}
      </label>
    </fieldset>
  </div>
</details>
