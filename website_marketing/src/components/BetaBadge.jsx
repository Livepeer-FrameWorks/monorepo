import React from 'react'

const BetaBadge = ({ label = 'Public Beta', className = '' }) => (
  <span
    className={`inline-flex items-center px-2.5 py-1 rounded-full text-xs font-semibold border bg-tokyo-night-blue/15 border-tokyo-night-blue text-tokyo-night-blue ${className}`}
  >
    {label}
  </span>
)

export default BetaBadge

