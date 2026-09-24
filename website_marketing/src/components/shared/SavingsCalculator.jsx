import { useId, useMemo, useState } from "react";
import InfoTooltip from "./InfoTooltip";
import { cn } from "@/lib/utils";
import { Slider } from "@/components/ui/slider";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import pricingCatalog from "@/data/pricing-catalog.json";
import {
  ENTERPRISE_MINUTES_THRESHOLD,
  SOURCE_QUALITIES,
  estimatePlans,
  formatCount,
} from "@/lib/pricingEstimate";

const formatMoney = (n) =>
  new Intl.NumberFormat("en-IE", {
    style: "currency",
    currency: pricingCatalog.currency,
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(n);

const formatGiB = (n) =>
  `${new Intl.NumberFormat("en-US", { maximumFractionDigits: n < 10 ? 1 : 0 }).format(n)} GB`;

const FOREVER = Infinity;

const PRESETS = [
  {
    id: "webinar",
    label: "Weekly webinar",
    inputs: {
      broadcastHours: 6,
      avgViewers: 150,
      concurrentChannels: 1,
      restreamTargets: 0,
      onDemandHours: 200,
      recordLive: true,
      recordingRetentionDays: 365,
      vodHours: 0,
    },
  },
  {
    id: "creator",
    label: "Daily creator",
    inputs: {
      broadcastHours: 60,
      avgViewers: 80,
      concurrentChannels: 1,
      restreamTargets: 2,
      onDemandHours: 300,
      recordLive: true,
      recordingRetentionDays: 30,
      vodHours: 0,
    },
  },
  {
    id: "channel",
    label: "24/7 channel",
    inputs: {
      broadcastHours: 730,
      avgViewers: 300,
      concurrentChannels: 1,
      restreamTargets: 0,
      onDemandHours: 500,
      recordLive: true,
      recordingRetentionDays: 7,
      vodHours: 0,
    },
  },
  {
    id: "event",
    label: "Live event",
    inputs: {
      broadcastHours: 8,
      avgViewers: 2500,
      concurrentChannels: 2,
      restreamTargets: 1,
      onDemandHours: 1500,
      recordLive: true,
      recordingRetentionDays: FOREVER,
      vodHours: 0,
    },
  },
  {
    id: "library",
    label: "VOD library",
    inputs: {
      broadcastHours: 0,
      avgViewers: 0,
      concurrentChannels: 1,
      restreamTargets: 0,
      onDemandHours: 4000,
      recordLive: false,
      recordingRetentionDays: 30,
      vodHours: 40,
    },
  },
];

const DEFAULT_INPUTS = {
  ...PRESETS[1].inputs,
  edgeOffloadPercent: 0,
  sourceQuality: "1080p",
  vodRetentionDays: FOREVER,
};

const RECORDING_RETENTION = [
  { value: 7, label: "7 days" },
  { value: 30, label: "30 days" },
  { value: 90, label: "90 days" },
  { value: 365, label: "1 year" },
  { value: FOREVER, label: "Forever" },
];

const VOD_RETENTION = [
  { value: 90, label: "90 days" },
  { value: 365, label: "1 year" },
  { value: FOREVER, label: "Forever" },
];

const MONTHS = [1, 6, 12];

const INCLUDED_FREE = [
  "Ingest (RTMP, SRT, WHIP)",
  "Live transcoding to the ABR ladder",
  "Bandwidth (egress GB)",
  "Viewers served from your own edges",
];

const retentionKey = (v) => (v === FOREVER ? "forever" : String(v));
const retentionFromKey = (k) => (k === "forever" ? FOREVER : Number(k));

function NumberField({ id, label, hint, tooltip, value, onChange, min, max, sliderMax, step }) {
  const clamp = (v) => Math.max(min, Math.min(max, Number.isFinite(v) ? v : min));
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <span className="flex items-center gap-1.5">
          <Label htmlFor={id} className="pricing-calculator__label">
            {label}
          </Label>
          {tooltip ? (
            <InfoTooltip label={`About ${label.toLowerCase()}`}>{tooltip}</InfoTooltip>
          ) : null}
        </span>
        <Input
          id={id}
          type="number"
          inputMode="numeric"
          className="w-24 h-9 text-sm"
          value={value}
          min={min}
          max={max}
          step={step}
          onChange={(e) => onChange(clamp(Number(e.target.value)))}
        />
      </div>
      <Slider
        value={[Math.min(value, sliderMax ?? max)]}
        onValueChange={([v]) => onChange(v)}
        min={min}
        max={sliderMax ?? max}
        step={step}
        aria-label={label}
        className="w-full"
      />
      {hint ? <div className="pricing-calculator__hint">{hint}</div> : null}
    </div>
  );
}

function SelectField({ id, label, value, options, onChange }) {
  return (
    <div className="space-y-2">
      <Label htmlFor={id} className="pricing-calculator__label">
        {label}
      </Label>
      <select
        id={id}
        className="pricing-calculator__select"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      >
        {options.map((o) => (
          <option key={o.key} value={o.key}>
            {o.label}
          </option>
        ))}
      </select>
    </div>
  );
}

const Calculator = ({ className }) => {
  const [inputs, setInputs] = useState(DEFAULT_INPUTS);
  const [presetId, setPresetId] = useState(PRESETS[1].id);
  const [month, setMonth] = useState(12);
  const idPrefix = useId();
  const fieldId = (name) => `${idPrefix}-${name}`;

  const update = (patch) => {
    setInputs((prev) => ({ ...prev, ...patch }));
    setPresetId("custom");
  };
  const applyPreset = (preset) => {
    setInputs((prev) => ({ ...prev, ...preset.inputs }));
    setPresetId(preset.id);
  };

  const { usage, plans, recommendedId } = useMemo(
    () => estimatePlans(inputs, pricingCatalog, month),
    [inputs, month]
  );
  const recommended = plans.find((p) => p.id === recommendedId);
  const isEnterpriseVolume = usage.billableMinutes > ENTERPRISE_MINUTES_THRESHOLD;
  const viewerHours = (usage.liveViewerMinutes + usage.onDemandMinutes) / 60;
  const perViewerHour = recommended && viewerHours > 0 ? recommended.total / viewerHours : null;

  return (
    <div className={cn("pricing-calculator", className)}>
      <div className="pricing-calculator__header">
        <h3>Estimate your monthly bill</h3>
        <p>
          Describe the workload and we price it the way invoices are rated: delivered minutes above
          your plan allowance, plus recordings and uploads stored per GB-month.
        </p>
      </div>

      <div className="pricing-calculator__presets" role="group" aria-label="Workload presets">
        {PRESETS.map((preset) => (
          <button
            key={preset.id}
            type="button"
            className="pricing-calculator__preset"
            aria-pressed={presetId === preset.id}
            onClick={() => applyPreset(preset)}
          >
            {preset.label}
          </button>
        ))}
        {presetId === "custom" ? (
          <span className="pricing-calculator__preset pricing-calculator__preset--static">
            Custom
          </span>
        ) : null}
      </div>

      <div className="pricing-calculator__grid">
        <div className="pricing-calculator__inputs">
          <fieldset className="pricing-calculator__group">
            <legend className="pricing-calculator__group-title">Live</legend>
            <div className="pricing-calculator__row pricing-calculator__row--split">
              <NumberField
                id={fieldId("hours")}
                label="Broadcast hours / month"
                tooltip="Total hours on air across all channels. A channel that never goes offline is about 730 hours a month."
                value={inputs.broadcastHours}
                onChange={(v) => update({ broadcastHours: v })}
                min={0}
                max={100000}
                sliderMax={1500}
                step={1}
              />
              <NumberField
                id={fieldId("viewers")}
                label="Average viewers"
                tooltip="Average concurrent viewers while you are live, not the peak. Delivered minutes are viewer-minutes actually watched."
                value={inputs.avgViewers}
                onChange={(v) => update({ avgViewers: v })}
                min={0}
                max={1000000}
                sliderMax={5000}
                step={10}
              />
            </div>
            <div className="pricing-calculator__row pricing-calculator__row--split">
              <NumberField
                id={fieldId("channels")}
                label="Streams live at once"
                value={inputs.concurrentChannels}
                onChange={(v) => update({ concurrentChannels: v })}
                min={1}
                max={1000}
                sliderMax={20}
                step={1}
              />
              <NumberField
                id={fieldId("restream")}
                label="Restream destinations"
                tooltip="Each push to YouTube, Twitch or another RTMP/SRT target counts as one viewer for the whole broadcast."
                value={inputs.restreamTargets}
                onChange={(v) => update({ restreamTargets: v })}
                min={0}
                max={20}
                sliderMax={6}
                step={1}
              />
            </div>
          </fieldset>

          <fieldset className="pricing-calculator__group">
            <legend className="pricing-calculator__group-title">Recording &amp; on demand</legend>
            <label className="pricing-calculator__check">
              <input
                type="checkbox"
                checked={inputs.recordLive}
                onChange={(e) => update({ recordLive: e.target.checked })}
              />
              Record live broadcasts
            </label>
            <div className="pricing-calculator__row pricing-calculator__row--split">
              <SelectField
                id={fieldId("quality")}
                label="Source quality"
                value={inputs.sourceQuality}
                options={SOURCE_QUALITIES.map((q) => ({
                  key: q.id,
                  label: `${q.label} · ${q.mbps} Mbps`,
                }))}
                onChange={(v) => update({ sourceQuality: v })}
              />
              <SelectField
                id={fieldId("rec-retention")}
                label="Keep recordings"
                value={retentionKey(inputs.recordingRetentionDays)}
                options={RECORDING_RETENTION.map((o) => ({
                  key: retentionKey(o.value),
                  label: o.label,
                }))}
                onChange={(v) => update({ recordingRetentionDays: retentionFromKey(v) })}
              />
            </div>
            <div className="pricing-calculator__row pricing-calculator__row--split">
              <NumberField
                id={fieldId("vod")}
                label="Video uploaded (hours / month)"
                tooltip="Uploads are transcoded to the adaptive ladder up to the source quality, and the renditions are stored."
                value={inputs.vodHours}
                onChange={(v) => update({ vodHours: v })}
                min={0}
                max={100000}
                sliderMax={200}
                step={1}
              />
              <SelectField
                id={fieldId("vod-retention")}
                label="Keep uploads"
                value={retentionKey(inputs.vodRetentionDays)}
                options={VOD_RETENTION.map((o) => ({ key: retentionKey(o.value), label: o.label }))}
                onChange={(v) => update({ vodRetentionDays: retentionFromKey(v) })}
              />
            </div>
            <NumberField
              id={fieldId("ondemand")}
              label="On-demand watch hours / month"
              tooltip="Hours viewers spend watching recordings, clips and uploads. These are delivered minutes too."
              value={inputs.onDemandHours}
              onChange={(v) => update({ onDemandHours: v })}
              min={0}
              max={10000000}
              sliderMax={20000}
              step={50}
            />
          </fieldset>

          <fieldset className="pricing-calculator__group">
            <legend className="pricing-calculator__group-title">Your infrastructure</legend>
            <NumberField
              id={fieldId("offload")}
              label="Viewers on your own edges (%)"
              tooltip="Run FrameWorks Edge on your own servers or cloud. We keep routing, TLS, analytics and access control; minutes served from your edges are not billed."
              hint="Restream pushes leave from FrameWorks and stay billable."
              value={inputs.edgeOffloadPercent}
              onChange={(v) => update({ edgeOffloadPercent: v })}
              min={0}
              max={100}
              step={5}
            />
          </fieldset>
        </div>

        <div className="pricing-calculator__panels">
          <div className="pricing-calculator__panel">
            <div className="pricing-calculator__panel-label">Usage</div>
            <div className="pricing-calculator__metric">
              <span className="pricing-calculator__metric-label">Delivered minutes</span>
              <span className="pricing-calculator__metric-value">
                {formatCount(usage.deliveredMinutes)}
              </span>
            </div>
            <ul className="pricing-calculator__breakdown">
              <li>
                <span>Live viewers</span>
                <span>{formatCount(usage.liveViewerMinutes)}</span>
              </li>
              <li>
                <span>On demand</span>
                <span>{formatCount(usage.onDemandMinutes)}</span>
              </li>
              <li>
                <span>Restreams</span>
                <span>{formatCount(usage.restreamMinutes)}</span>
              </li>
              {usage.offloadedMinutes > 0 ? (
                <li>
                  <span>Served by your edges</span>
                  <span>−{formatCount(usage.offloadedMinutes)}</span>
                </li>
              ) : null}
            </ul>
            <div className="pricing-calculator__metric">
              <span className="pricing-calculator__metric-label">
                Average storage, month {month}
              </span>
              <span className="pricing-calculator__metric-value">
                {formatGiB(usage.storageGiBMonths)}
              </span>
            </div>
          </div>

          <div className="pricing-calculator__panel">
            <div className="pricing-calculator__panel-heading">
              <span>Cost by plan</span>
              <div className="pricing-calculator__months" role="group" aria-label="Billing month">
                {MONTHS.map((m) => (
                  <button
                    key={m}
                    type="button"
                    aria-pressed={month === m}
                    className="pricing-calculator__month"
                    onClick={() => setMonth(m)}
                  >
                    Month {m}
                  </button>
                ))}
              </div>
            </div>
            {isEnterpriseVolume ? (
              <div className="pricing-calculator__enterprise-hint">
                Over {formatCount(ENTERPRISE_MINUTES_THRESHOLD)} minutes a month.{" "}
                <a href="/contact">Talk to us</a> about committed-volume rates.
              </div>
            ) : null}
            <ul className="pricing-calculator__plans">
              {plans.map((plan) => (
                <li
                  key={plan.id}
                  className={cn(
                    "pricing-calculator__plan",
                    plan.id === recommendedId && "pricing-calculator__plan--recommended",
                    !plan.eligible && "pricing-calculator__plan--ineligible"
                  )}
                >
                  <div className="pricing-calculator__plan-head">
                    <span className="pricing-calculator__plan-name">
                      {plan.name}
                      {plan.id === recommendedId ? (
                        <span className="pricing-calculator__plan-tag">Lowest cost</span>
                      ) : null}
                      {plan.prepaid ? (
                        <span className="pricing-calculator__plan-tag pricing-calculator__plan-tag--muted">
                          Prepaid
                        </span>
                      ) : null}
                    </span>
                    <span className="pricing-calculator__plan-total">
                      {plan.eligible ? formatMoney(plan.total) : "—"}
                    </span>
                  </div>
                  <div className="pricing-calculator__plan-lines">
                    {plan.eligible ? (
                      <>
                        <span>Base {formatMoney(plan.base)}</span>
                        <span>Delivery {formatMoney(plan.delivery)}</span>
                        <span>Storage {formatMoney(plan.storage)}</span>
                      </>
                    ) : (
                      <span>Over the plan limit: {plan.blockers.join(", ")}</span>
                    )}
                  </div>
                </li>
              ))}
            </ul>
            {perViewerHour !== null ? (
              <div className="pricing-calculator__hint">
                {formatMoney(perViewerHour)} per viewer-hour on {recommended.name}.
              </div>
            ) : null}
          </div>

          <div className="pricing-calculator__panel">
            <div className="pricing-calculator__panel-label">Included at €0</div>
            <ul className="pricing-calculator__included">
              {INCLUDED_FREE.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ul>
          </div>

          <details className="pricing-calculator__math">
            <summary>Show the math</summary>
            <ul>
              <li>
                Live delivered minutes = broadcast hours × 60 × average viewers. Restreams add
                broadcast hours × 60 per destination.
              </li>
              <li>
                Recordings keep the source rendition only: {usage.sourceMbps} Mbps ≈{" "}
                {formatGiB(usage.recordingGiBPerMonth)} recorded per month.
              </li>
              <li>
                Uploads keep the transcoded ladder up to the source quality:{" "}
                {usage.vodLadderMbps.toFixed(1)} Mbps combined ≈ {formatGiB(usage.vodGiBPerMonth)}{" "}
                added per month.
              </li>
              <li>
                Storage is billed on the average held over the month, to the second, per GB-month of{" "}
                {pricingCatalog.storageHoursPerMonth} hours (1 GB = 2³⁰ bytes). It grows each month
                until content starts expiring at its retention, so month 12 shows the steady state
                for most workloads.
              </li>
              <li>
                Plan prices come straight from our billing catalog. Estimates exclude VAT and assume
                steady usage through the month.
              </li>
            </ul>
          </details>
        </div>
      </div>
    </div>
  );
};

export default Calculator;
