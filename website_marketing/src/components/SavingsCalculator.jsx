import React, { useMemo, useState } from 'react'
import InfoTooltip from './InfoTooltip'

const formatNumber = (n) => new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(n)

const Calculator = () => {
  const clamp = (v, min, max) => Math.max(min, Math.min(max, Number.isFinite(v) ? v : min))
  const [viewers, setViewers] = useState(100)
  const [hoursPerDay, setHoursPerDay] = useState(2)
  const [daysPerMonth, setDaysPerMonth] = useState(30)
  const [gpuHoursMonthly, setGpuHoursMonthly] = useState(0)
  const [edgeOffloadPercent, setEdgeOffloadPercent] = useState(0)

  const safeViewers = clamp(Number(viewers) || 0, 0, 10000000)
  const safeHoursPerDay = clamp(Number(hoursPerDay) || 0, 0, 24)
  const safeDaysPerMonth = clamp(Number(daysPerMonth) || 0, 0, 31)
  const safeGpuHours = clamp(Number(gpuHoursMonthly) || 0, 0, 100000000)
  const safeOffload = clamp(Number(edgeOffloadPercent) || 0, 0, 100)

  const minutes = useMemo(() => safeViewers * safeHoursPerDay * safeDaysPerMonth * 60, [safeViewers, safeHoursPerDay, safeDaysPerMonth])
  // FrameWorks minutes-based delivery pricing
  const plans = [
    { name: 'Supporter', price: 79, includedMin: 150000, overPerMin: 0.00049, includedGpu: 10 },
    { name: 'Developer', price: 249, includedMin: 500000, overPerMin: 0.00047, includedGpu: 50 },
    { name: 'Production', price: 999, includedMin: 2000000, overPerMin: 0.00045, includedGpu: 250 },
  ]
  const gpuOveragePerHour = 0.5 // EUR/hour

  const bestEstimate = useMemo(() => {
    const billableMinutes = minutes * (1 - safeOffload / 100)
    const estimates = plans.map(p => {
      const overMin = Math.max(0, billableMinutes - p.includedMin)
      const deliveryOverageCost = overMin * p.overPerMin
      const overGpu = Math.max(0, safeGpuHours - p.includedGpu)
      const gpuOverageCost = overGpu * gpuOveragePerHour
      const total = p.price + deliveryOverageCost + gpuOverageCost
      return { plan: p.name, base: p.price, includedMin: p.includedMin, overMin, deliveryOverageCost, overGpu, gpuOverageCost, total, billableMinutes }
    })
    return estimates.reduce((min, e) => (min && min.total <= e.total ? min : e), null)
  }, [minutes, safeOffload, safeGpuHours])

  // Enterprise threshold: switch to custom quote messaging at high volumes
  const enterpriseMinThreshold = 5000000 // 5,000,000 delivered minutes/month
  const isEnterpriseVolume = minutes > enterpriseMinThreshold

  return (
    <div className="glow-card p-6">
      <h3 className="text-xl font-semibold text-tokyo-night-fg mb-4">Pricing Calculator</h3>
      <div className="grid md:grid-cols-2 gap-6">
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="block text-sm text-tokyo-night-comment mb-1">Concurrent viewers</label>
              <input type="number" className="input" value={safeViewers} min={0} max={10000000} onChange={(e) => setViewers(e.target.value)} />
            </div>
          </div>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="block text-sm text-tokyo-night-comment mb-1">Hours/day</label>
              <input type="number" className="input" value={safeHoursPerDay} min={0} max={24} onChange={(e) => setHoursPerDay(e.target.value)} />
            </div>
            <div>
              <label className="block text-sm text-tokyo-night-comment mb-1">Days/month</label>
              <input type="number" className="input" value={safeDaysPerMonth} min={0} max={31} onChange={(e) => setDaysPerMonth(e.target.value)} />
            </div>
          </div>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="block text-sm text-tokyo-night-comment mb-1">Self-hosted edge offload (%)</label>
              <input type="number" className="input" value={safeOffload} min={0} max={100} onChange={(e) => setEdgeOffloadPercent(e.target.value)} />
              <div className="mt-1 text-xs text-tokyo-night-comment">Offloaded minutes are not billed by FrameWorks.</div>
            </div>
            <div>
              <label className="block text-sm text-tokyo-night-comment mb-1">GPU hours/month (est.)</label>
              <input type="number" className="input" value={safeGpuHours} min={0} onChange={(e) => setGpuHoursMonthly(e.target.value)} />
              <div className="mt-1 text-xs text-tokyo-night-comment">Included by tier: 10/50/250 hrs. Overage €{formatNumber(gpuOveragePerHour)}/hr.</div>
            </div>
          </div>
        </div>
        <div className="space-y-4">
          <div className="bg-tokyo-night-bg-dark border border-tokyo-night-fg-gutter rounded-md p-4">
            <div className="text-sm text-tokyo-night-comment">Usage</div>
            <div className="mt-1 text-tokyo-night-fg">{formatNumber(minutes)} delivered minutes</div>
            <div className="text-xs text-tokyo-night-comment">Billable after edge offload: {formatNumber(bestEstimate.billableMinutes)} min</div>
          </div>
          <div className="bg-tokyo-night-bg-dark border border-tokyo-night-fg-gutter rounded-md p-4">
            {isEnterpriseVolume && (
              <div className="mb-2 text-xs text-tokyo-night-comment">Large volume — custom discounts available. Contact us for tailored pricing.</div>
            )}
            <div className="flex items-center gap-2">
              <div className="text-sm text-tokyo-night-comment">FrameWorks estimate (cheapest option)</div>
              <InfoTooltip>
                Delivery priced per minute. Offload to your own edges to reduce billable minutes. GPU hours are separate; overage charged per hour. Enterprise: custom quote.
              </InfoTooltip>
            </div>
            <div className="mt-1 text-tokyo-night-fg">Plan: <span className="font-semibold">{bestEstimate.plan}</span></div>
            <div className="text-tokyo-night-fg-dark text-sm">Base €{formatNumber(bestEstimate.base)} + Delivery overage €{formatNumber(bestEstimate.deliveryOverageCost)} + GPU overage €{formatNumber(bestEstimate.gpuOverageCost)} = <span className="font-semibold">€{formatNumber(bestEstimate.total)}</span></div>
          </div>
        </div>
      </div>
    </div>
  )
}

export default Calculator
