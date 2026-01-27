import { useId, useMemo, useState } from "react";
import InfoTooltip from "./InfoTooltip";
import { cn } from "@/lib/utils";
import { Slider } from "@/components/ui/slider";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

const formatNumber = (n) =>
  new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(n);

const PRICING_PLANS = [
  { name: "Supporter", price: 79, includedMin: 150000, overPerMin: 0.00049, includedGpu: 10 },
  { name: "Developer", price: 249, includedMin: 500000, overPerMin: 0.00047, includedGpu: 50 },
  { name: "Production", price: 999, includedMin: 2000000, overPerMin: 0.00045, includedGpu: 250 },
];

const GPU_OVERAGE_PER_HOUR = 0.5;
const ENTERPRISE_MIN_THRESHOLD = 5_000_000;

const Calculator = ({ className, variant = "default" }) => {
  const clamp = (v, min, max) => Math.max(min, Math.min(max, Number.isFinite(v) ? v : min));
  const [viewers, setViewers] = useState(100);
  const [hoursPerDay, setHoursPerDay] = useState(2);
  const [daysPerMonth, setDaysPerMonth] = useState(30);
  const [gpuHoursMonthly, setGpuHoursMonthly] = useState(0);
  const [edgeOffloadPercent, setEdgeOffloadPercent] = useState(0);
  const idPrefix = useId();

  const ids = {
    viewers: `${idPrefix}-viewers`,
    hoursPerDay: `${idPrefix}-hours`,
    daysPerMonth: `${idPrefix}-days`,
    edgeOffloadPercent: `${idPrefix}-offload`,
    gpuHoursMonthly: `${idPrefix}-gpu-hours`,
  };

  const safeViewers = clamp(Number(viewers) || 0, 0, 10000000);
  const safeHoursPerDay = clamp(Number(hoursPerDay) || 0, 0, 24);
  const safeDaysPerMonth = clamp(Number(daysPerMonth) || 0, 0, 31);
  const safeGpuHours = clamp(Number(gpuHoursMonthly) || 0, 0, 100000000);
  const safeOffload = clamp(Number(edgeOffloadPercent) || 0, 0, 100);

  const minutes = useMemo(
    () => safeViewers * safeHoursPerDay * safeDaysPerMonth * 60,
    [safeViewers, safeHoursPerDay, safeDaysPerMonth]
  );

  const bestEstimate = useMemo(() => {
    const billableMinutes = minutes * (1 - safeOffload / 100);
    const estimates = PRICING_PLANS.map((p) => {
      const overMin = Math.max(0, billableMinutes - p.includedMin);
      const deliveryOverageCost = overMin * p.overPerMin;
      const overGpu = Math.max(0, safeGpuHours - p.includedGpu);
      const gpuOverageCost = overGpu * GPU_OVERAGE_PER_HOUR;
      const total = p.price + deliveryOverageCost + gpuOverageCost;
      return {
        plan: p.name,
        base: p.price,
        includedMin: p.includedMin,
        overMin,
        deliveryOverageCost,
        overGpu,
        gpuOverageCost,
        total,
        billableMinutes,
      };
    });
    return estimates.reduce((min, e) => (min && min.total <= e.total ? min : e), null);
  }, [minutes, safeOffload, safeGpuHours]);

  // Enterprise threshold: switch to custom quote messaging at high volumes
  const isEnterpriseVolume = minutes > ENTERPRISE_MIN_THRESHOLD;

  return (
    <div
      className={cn(
        "pricing-calculator",
        variant === "compact" && "pricing-calculator--compact",
        className
      )}
    >
      <div className="pricing-calculator__header">
        <h3>Pricing calculator</h3>
        <p>Estimate monthly spend by plugging in viewers, runtime, and GPU workloads.</p>
      </div>
      <div className="pricing-calculator__grid">
        <div className="pricing-calculator__inputs">
          <div className="pricing-calculator__row">
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label htmlFor={ids.viewers} className="pricing-calculator__label">
                  Concurrent viewers
                </Label>
                <Input
                  id={ids.viewers}
                  type="number"
                  className="w-24 h-9 text-sm"
                  value={safeViewers}
                  min={0}
                  max={10000}
                  onChange={(e) => setViewers(e.target.value)}
                />
              </div>
              <Slider
                value={[safeViewers]}
                onValueChange={([val]) => setViewers(val)}
                min={0}
                max={10000}
                step={10}
                className="w-full"
              />
            </div>
          </div>
          <div className="pricing-calculator__row pricing-calculator__row--split">
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label htmlFor={ids.hoursPerDay} className="pricing-calculator__label">
                  Hours/day
                </Label>
                <Input
                  id={ids.hoursPerDay}
                  type="number"
                  className="w-20 h-9 text-sm"
                  value={safeHoursPerDay}
                  min={0}
                  max={24}
                  onChange={(e) => setHoursPerDay(e.target.value)}
                />
              </div>
              <Slider
                value={[safeHoursPerDay]}
                onValueChange={([val]) => setHoursPerDay(val)}
                min={0}
                max={24}
                step={1}
                className="w-full"
              />
            </div>
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label htmlFor={ids.daysPerMonth} className="pricing-calculator__label">
                  Days/month
                </Label>
                <Input
                  id={ids.daysPerMonth}
                  type="number"
                  className="w-20 h-9 text-sm"
                  value={safeDaysPerMonth}
                  min={0}
                  max={31}
                  onChange={(e) => setDaysPerMonth(e.target.value)}
                />
              </div>
              <Slider
                value={[safeDaysPerMonth]}
                onValueChange={([val]) => setDaysPerMonth(val)}
                min={0}
                max={31}
                step={1}
                className="w-full"
              />
            </div>
          </div>
          <div className="pricing-calculator__row pricing-calculator__row--split">
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label htmlFor={ids.edgeOffloadPercent} className="pricing-calculator__label">
                  Edge offload (%)
                </Label>
                <Input
                  id={ids.edgeOffloadPercent}
                  type="number"
                  className="w-20 h-9 text-sm"
                  value={safeOffload}
                  min={0}
                  max={100}
                  onChange={(e) => setEdgeOffloadPercent(e.target.value)}
                />
              </div>
              <Slider
                value={[safeOffload]}
                onValueChange={([val]) => setEdgeOffloadPercent(val)}
                min={0}
                max={100}
                step={5}
                className="w-full"
              />
              <div className="pricing-calculator__hint">
                Offloaded minutes are not billed by FrameWorks.
              </div>
            </div>
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label htmlFor={ids.gpuHoursMonthly} className="pricing-calculator__label">
                  GPU hrs/mo
                </Label>
                <Input
                  id={ids.gpuHoursMonthly}
                  type="number"
                  className="w-20 h-9 text-sm"
                  value={safeGpuHours}
                  min={0}
                  max={500}
                  onChange={(e) => setGpuHoursMonthly(e.target.value)}
                />
              </div>
              <Slider
                value={[safeGpuHours]}
                onValueChange={([val]) => setGpuHoursMonthly(val)}
                min={0}
                max={500}
                step={5}
                className="w-full"
              />
              <div className="pricing-calculator__hint">
                Included by tier: 10/50/250 hrs. Overage €{formatNumber(GPU_OVERAGE_PER_HOUR)}/hr.
              </div>
            </div>
          </div>
        </div>
        <div className="pricing-calculator__panels">
          <div className="pricing-calculator__panel">
            <div className="pricing-calculator__panel-label">Usage</div>
            <div className="pricing-calculator__metric">
              <span className="pricing-calculator__metric-label">Delivered minutes</span>
              <span className="pricing-calculator__metric-value">{formatNumber(minutes)}</span>
            </div>
            <div className="pricing-calculator__metric">
              <span className="pricing-calculator__metric-label">Billable after offload</span>
              <span className="pricing-calculator__metric-value">
                {formatNumber(bestEstimate.billableMinutes)}
              </span>
            </div>
          </div>
          <div className="pricing-calculator__panel">
            {isEnterpriseVolume && (
              <div className="pricing-calculator__enterprise-hint">
                Large volume — custom discounts available. Contact us for tailored pricing.
              </div>
            )}
            <div className="pricing-calculator__panel-heading">
              <span>FrameWorks estimate (cheapest option)</span>
              <InfoTooltip>
                Delivery is priced per minute. Offload to your own edges to shrink billable minutes.
                GPU hours are separate; overage is charged per hour. Enterprise: custom quote.
              </InfoTooltip>
            </div>
            <div className="pricing-calculator__metric">
              <span className="pricing-calculator__metric-label">Recommended plan</span>
              <span className="pricing-calculator__metric-value">{bestEstimate.plan}</span>
            </div>
            <ul className="pricing-calculator__breakdown">
              <li>
                <span>Base subscription</span>
                <span>€{formatNumber(bestEstimate.base)}</span>
              </li>
              <li>
                <span>Delivery overage</span>
                <span>€{formatNumber(bestEstimate.deliveryOverageCost)}</span>
              </li>
              <li>
                <span>GPU overage</span>
                <span>€{formatNumber(bestEstimate.gpuOverageCost)}</span>
              </li>
            </ul>
            <div className="pricing-calculator__panel-total">
              Estimated total
              <span>€{formatNumber(bestEstimate.total)}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
};

export default Calculator;
