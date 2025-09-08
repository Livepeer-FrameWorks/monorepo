import React, { useEffect, useMemo, useState } from 'react'
import { motion } from 'framer-motion'
import config from '../config'

const pill = (label, cls) => (
  <span className={`px-3 py-1 rounded-full text-sm border ${cls}`}>{label}</span>
)

const formatTime = (ts) => {
  try {
    const d = new Date(ts)
    if (isNaN(+d)) return 'Unknown'
    return d.toLocaleString()
  } catch { return 'Unknown' }
}

const computeServiceRollups = (instances) => {
  const byService = new Map()
  for (const inst of instances) {
    const sid = inst.serviceId || 'unknown'
    const arr = byService.get(sid) || []
    arr.push(inst)
    byService.set(sid, arr)
  }
  const rollups = []
  for (const [serviceId, list] of byService) {
    const total = list.length
    const healthyCount = list.filter(x => String(x.status).toLowerCase() === 'healthy' || String(x.status).toLowerCase() === 'live' || String(x.status).toLowerCase() === 'ready').length
    const last = list.reduce((acc, x) => Math.max(acc, x.lastHealthCheck ? Date.parse(x.lastHealthCheck) : 0), 0)
    let status = 'operational'
    if (healthyCount === 0) status = 'down'
    else if (healthyCount < total) status = 'degraded'
    rollups.push({ serviceId, total, healthy: healthyCount, status, lastHealthCheck: last })
  }
  return rollups.sort((a, b) => a.serviceId.localeCompare(b.serviceId))
}

const overallFromRollups = (rollups) => {
  if (!rollups.length) return { label: 'Unknown', cls: 'bg-tokyo-night-comment/20 text-tokyo-night-comment border-tokyo-night-comment/40' }
  const anyDown = rollups.some(r => r.status === 'down')
  const anyDegraded = rollups.some(r => r.status === 'degraded')
  if (anyDown) return { label: 'Partial Outage', cls: 'bg-red-500/20 text-red-400 border-red-500/40' }
  if (anyDegraded) return { label: 'Degraded Performance', cls: 'bg-yellow-500/20 text-yellow-400 border-yellow-500/40' }
  return { label: 'All Systems Operational', cls: 'bg-green-500/20 text-green-400 border-green-500/40' }
}

const StatusPage = () => {
  const [instances, setInstances] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [lastUpdated, setLastUpdated] = useState(0)

  const rollups = useMemo(() => computeServiceRollups(instances), [instances])
  const overall = useMemo(() => overallFromRollups(rollups), [rollups])
  const lastCheck = useMemo(() => {
    const last = Math.max(...instances.map(i => i.lastHealthCheck ? Date.parse(i.lastHealthCheck) : 0), 0)
    return last || lastUpdated
  }, [instances, lastUpdated])

  const fetchHealth = async () => {
    setLoading(true)
    setError('')
    try {
      const res = await fetch(config.gatewayUrl, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ query: 'query { serviceInstancesHealth { instanceId serviceId clusterId protocol host port healthEndpoint status lastHealthCheck } }' })
      })
      if (!res.ok) throw new Error(`Gateway status ${res.status}`)
      const json = await res.json()
      if (json.errors?.length) throw new Error(json.errors[0]?.message || 'GraphQL error')
      const list = json.data?.serviceInstancesHealth || []
      setInstances(Array.isArray(list) ? list : [])
      setLastUpdated(Date.now())
    } catch (e) {
      setError(String(e.message || e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchHealth()
    const id = setInterval(fetchHealth, 30000)
    return () => clearInterval(id)
  }, [])

  return (
    <div className="pt-16">
      <section className="section-padding bg-gradient-to-br from-tokyo-night-bg via-tokyo-night-bg-light to-tokyo-night-bg">
        <div className="max-w-7xl mx-auto text-center">
          <motion.div initial={{ opacity: 0, y: 30 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.6 }}>
            <h1 className="text-4xl md:text-6xl font-bold gradient-text mb-6">Status</h1>
            <p className="text-xl text-tokyo-night-fg-dark max-w-3xl mx-auto">Public Beta: demo may be intermittently offline</p>
            <div className="mt-4 inline-flex items-center gap-3">
              {pill(overall.label, overall.cls)}
              <span className="text-tokyo-night-comment text-sm">{loading ? 'Refreshing…' : `Last check: ${formatTime(lastCheck)}`}</span>
              <button onClick={fetchHealth} className="btn-secondary py-1 px-3">Refresh</button>
            </div>
            {error && <div className="mt-2 text-sm text-tokyo-night-orange">{error}</div>}
          </motion.div>
        </div>
      </section>
      <section className="section-padding">
        <div className="max-w-5xl mx-auto">
          <div className="glow-card p-6">
            <h2 className="text-xl font-semibold text-tokyo-night-fg mb-4">Services</h2>
            {rollups.length === 0 ? (
              <p className="text-tokyo-night-comment">No health data yet.</p>
            ) : (
              <div className="grid md:grid-cols-2 gap-4">
                {rollups.map((r) => (
                  <div key={r.serviceId} className="border border-tokyo-night-fg-gutter rounded-md p-4">
                    <div className="flex items-center justify-between mb-2">
                      <div className="font-medium text-tokyo-night-fg">{r.serviceId}</div>
                      {r.status === 'operational' && pill('Operational', 'bg-green-500/20 text-green-400 border-green-500/40')}
                      {r.status === 'degraded' && pill('Degraded', 'bg-yellow-500/20 text-yellow-400 border-yellow-500/40')}
                      {r.status === 'down' && pill('Down', 'bg-red-500/20 text-red-400 border-red-500/40')}
                    </div>
                    <div className="text-tokyo-night-fg-dark text-sm">Instances: {r.healthy}/{r.total} healthy</div>
                    <div className="text-tokyo-night-comment text-xs mt-1">Last health: {formatTime(r.lastHealthCheck)}</div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </section>

      <section className="section-padding">
        <div className="max-w-4xl mx-auto">
          <div className="glow-card p-6">
            <h2 className="text-xl font-semibold text-tokyo-night-fg mb-4">Recent Incidents</h2>
            <p className="text-tokyo-night-comment">No incidents reported.</p>
          </div>
        </div>
      </section>
    </div>
  )
}

export default StatusPage
