import { cn } from '@/lib/utils'

const MarketingSlab = ({ className, variant = 'default', children }) => (
  <div className={cn('marketing-slab', variant !== 'default' && `marketing-slab--${variant}`, className)}>
    <div className="marketing-slab__content">{children}</div>
  </div>
)

export default MarketingSlab
