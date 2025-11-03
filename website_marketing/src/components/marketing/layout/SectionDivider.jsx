import { forwardRef } from 'react'
import { cn } from '@/lib/utils'

const SectionDivider = forwardRef(
  ({ angle = 'default', showBar = true, className, 'aria-hidden': ariaHidden, ...props }, ref) => (
    <div
      ref={ref}
      className={cn(
        'marketing-section-divider',
        angle && `marketing-section-divider--${angle}`,
        className
      )}
      aria-hidden={ariaHidden ?? 'true'}
      {...props}
    >
      {showBar ? <span className="marketing-section-divider__bar" /> : null}
    </div>
  )
)

export default SectionDivider
