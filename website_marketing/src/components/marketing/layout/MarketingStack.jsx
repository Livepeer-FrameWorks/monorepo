import { forwardRef } from 'react'
import { cn } from '@/lib/utils'

const MarketingStack = forwardRef(
  ({ align = 'start', gap = 'md', direction = 'column', className, children, ...props }, ref) => (
    <div
      ref={ref}
      className={cn(
        'marketing-stack',
        align && `marketing-stack--align-${align}`,
        gap && `marketing-stack--gap-${gap}`,
        direction === 'row' && 'marketing-stack--row',
        className
      )}
      {...props}
    >
      {children}
    </div>
  )
)

export default MarketingStack
