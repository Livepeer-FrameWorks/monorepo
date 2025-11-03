import { cn } from '@/lib/utils'

const MarketingSlabHeader = ({ title, eyebrow, actions, subtitle, className }) => (
  <div className={cn('marketing-slab__header marketing-slab__header--stack', className)}>
    <div className="marketing-slab__row">
      <div className="flex flex-col gap-2">
        {eyebrow ? <span className="marketing-pill">{eyebrow}</span> : null}
        {title ? <h3 className="marketing-slab__title">{title}</h3> : null}
      </div>
      {actions}
    </div>
    {subtitle ? <p className="marketing-slab__subtitle">{subtitle}</p> : null}
  </div>
)

export default MarketingSlabHeader
