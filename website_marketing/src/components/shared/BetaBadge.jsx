import { cn } from '@/lib/utils'

const BetaBadge = ({ label = 'Public Beta', className = '' }) => (
  <span
    className={cn(
      'beta-badge inline-flex items-center rounded-full px-2.5 py-1 text-[0.68rem] font-semibold uppercase tracking-[0.2em]',
      className
    )}
  >
    {label}
  </span>
)

export default BetaBadge
