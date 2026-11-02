/**
 * StudioAudioControls Component
 *
 * The audio section of the studio: the browser's own processing of each
 * microphone, and the noise gate, de-esser, EQ and ducking on the mixed audio.
 * Reads the controller from the StreamCrafter context.
 */

import React, { useState } from "react";
import {
  getAudioConstraints,
  type AudioProcessingSettings,
} from "@livepeer-frameworks/streamcrafter-core";
import { useStudioTranslate } from "../context/StudioI18nContext";
import { useStreamCrafterContext } from "../context/StreamCrafterContext";

const GATE_THRESHOLD_DEFAULT = -40;

export interface StudioAudioControlsProps {
  /** Browser processing of the microphones; kept here when not given. */
  browserProcessing?: AudioProcessingSettings;
  onBrowserProcessingChange?: (settings: Partial<AudioProcessingSettings>) => void;
  className?: string;
}

export function StudioAudioControls({
  browserProcessing: browserProp,
  onBrowserProcessingChange,
  className = "",
}: StudioAudioControlsProps) {
  const t = useStudioTranslate();
  const ctx = useStreamCrafterContext();
  const controller = ctx.getController();

  const [ownBrowser, setOwnBrowser] = useState<AudioProcessingSettings>(() => {
    const defaults = getAudioConstraints(ctx.qualityProfile);
    return {
      echoCancellation: defaults.echoCancellation,
      noiseSuppression: defaults.noiseSuppression,
      autoGainControl: defaults.autoGainControl,
    };
  });
  const browser = browserProp ?? ownBrowser;

  const [processing, setProcessing] = useState(() => controller?.getAudioProcessing() ?? {});
  const [ducking, setDucking] = useState(() => controller?.isAudioDuckingEnabled() ?? false);

  const changeBrowser = (settings: Partial<AudioProcessingSettings>) => {
    if (onBrowserProcessingChange) {
      onBrowserProcessingChange(settings);
      return;
    }
    const next = { ...ownBrowser, ...settings };
    setOwnBrowser(next);
    for (const source of ctx.sources) {
      for (const track of source.stream.getAudioTracks()) {
        track.applyConstraints(next).catch((err) => {
          console.warn("Failed to apply audio constraints:", err);
        });
      }
    }
  };

  const change = (config: Parameters<NonNullable<typeof controller>["setAudioProcessing"]>[0]) => {
    controller?.setAudioProcessing(config);
    setProcessing(controller?.getAudioProcessing() ?? {});
  };

  const gate = processing.noiseGate;
  const eq = processing.eq;
  const eqBand = (band: "low" | "mid" | "high", label: string) => (
    <label className="fw-sc-audio-field">
      <span>{label}</span>
      <input
        type="range"
        min={-12}
        max={12}
        step={1}
        value={eq?.[band] ?? 0}
        disabled={!eq?.enabled}
        onChange={(e) => change({ eq: { enabled: true, [band]: Number(e.target.value) } })}
      />
      <span className="fw-sc-audio-value">{eq?.[band] ?? 0} dB</span>
    </label>
  );

  return (
    <details className={`fw-sc-audio ${className}`}>
      <summary className="fw-sc-audio-summary">{t("audio")}</summary>
      <div className="fw-sc-audio-body">
        <fieldset className="fw-sc-audio-group">
          <legend>{t("browserProcessing")}</legend>
          {(["echoCancellation", "noiseSuppression", "autoGainControl"] as const).map((key) => (
            <label key={key} className="fw-sc-audio-toggle">
              <input
                type="checkbox"
                checked={browser[key]}
                onChange={(e) => changeBrowser({ [key]: e.target.checked })}
              />
              {t(key)}
            </label>
          ))}
        </fieldset>

        <fieldset className="fw-sc-audio-group">
          <legend>{t("processing")}</legend>
          <label className="fw-sc-audio-toggle">
            <input
              type="checkbox"
              checked={gate?.enabled ?? false}
              onChange={(e) =>
                change({
                  noiseGate: {
                    enabled: e.target.checked,
                    threshold: gate?.threshold ?? GATE_THRESHOLD_DEFAULT,
                  },
                })
              }
            />
            {t("noiseGate")}
          </label>
          <label className="fw-sc-audio-field">
            <span>{t("noiseGateThreshold")}</span>
            <input
              type="range"
              min={-80}
              max={0}
              step={1}
              value={gate?.threshold ?? GATE_THRESHOLD_DEFAULT}
              disabled={!gate?.enabled}
              onChange={(e) =>
                change({ noiseGate: { enabled: true, threshold: Number(e.target.value) } })
              }
            />
            <span className="fw-sc-audio-value">
              {gate?.threshold ?? GATE_THRESHOLD_DEFAULT} dB
            </span>
          </label>
          <label className="fw-sc-audio-toggle">
            <input
              type="checkbox"
              checked={processing.deEsser?.enabled ?? false}
              onChange={(e) => change({ deEsser: { enabled: e.target.checked } })}
            />
            {t("deEsser")}
          </label>
          <label className="fw-sc-audio-toggle">
            <input
              type="checkbox"
              checked={eq?.enabled ?? false}
              onChange={(e) =>
                change({
                  eq: {
                    enabled: e.target.checked,
                    low: eq?.low ?? 0,
                    mid: eq?.mid ?? 0,
                    high: eq?.high ?? 0,
                  },
                })
              }
            />
            {t("equalizer")}
          </label>
          {eqBand("low", t("eqLow"))}
          {eqBand("mid", t("eqMid"))}
          {eqBand("high", t("eqHigh"))}
          <label className="fw-sc-audio-toggle">
            <input
              type="checkbox"
              checked={ducking}
              onChange={(e) => {
                controller?.setAudioDucking(e.target.checked);
                setDucking(e.target.checked);
              }}
            />
            {t("ducking")}
          </label>
        </fieldset>
      </div>
    </details>
  );
}
