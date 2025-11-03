import { forwardRef } from 'react'
import { cn } from '@/lib/utils'
import MarketingBand from '../layout/MarketingBand'
import HeadlineStack from '../content/HeadlineStack'
import MarketingTimeline from './MarketingTimeline'

const TimelineBand = forwardRef(
  (
    { eyebrow, title, subtitle, actions, items = [], children, surface = 'mesh', variant = 'band', className, ...props },
    ref
  ) => (
    <MarketingBand
      ref={ref}
      surface={variant === 'band' ? surface : 'none'}
      className={cn('timeline-band', variant === 'plain' && 'timeline-band--plain', className)}
      flush={variant === 'plain'}
      {...props}
    >
      {title || subtitle || eyebrow || actions ? (
        <HeadlineStack
          eyebrow={eyebrow}
          title={title}
          subtitle={subtitle}
          actions={actions}
          actionsPlacement="inline"
          underline={false}
        />
      ) : null}
      {items.length ? <MarketingTimeline items={items} /> : children}
    </MarketingBand>
  )
)

export default TimelineBand
