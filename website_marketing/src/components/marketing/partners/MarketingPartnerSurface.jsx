import { forwardRef } from 'react'
import { cn } from '@/lib/utils'
import { Link } from 'react-router-dom'
import { ArrowTopRightOnSquareIcon } from '@heroicons/react/24/outline'
import { renderSlot } from '../utils'
import MarketingBand from '../layout/MarketingBand'
import MarketingGridSeam from '../layout/MarketingGridSeam'
import HeadlineStack from '../content/HeadlineStack'

const MarketingPartnerSurface = forwardRef(
  (
    {
      partners = [],
      headline,
      eyebrow,
      subtitle,
      actions,
      columns = 2,
      stackAt = 'md',
      variant = 'card',
      className,
      ...props
    },
    ref
  ) => (
    <MarketingBand
      ref={ref}
      surface="beam"
      className={cn('marketing-partner-surface', variant && `marketing-partner-surface--${variant}`, className)}
      {...props}
    >
      {headline || eyebrow || subtitle || actions ? (
        <HeadlineStack
          eyebrow={eyebrow}
          title={headline}
          subtitle={subtitle}
          actions={actions}
          actionsPlacement="inline"
          underline={false}
        />
      ) : null}
      <MarketingGridSeam
        columns={columns}
        stackAt={stackAt}
        gap="md"
        surface={variant === 'flush' ? 'panel' : 'glass'}
        className={cn('marketing-partner-surface__grid', variant === 'flush' && 'marketing-partner-surface__grid--flush')}
      >
        {partners.map((partner) => {
          const hasLink = Boolean(partner.href || partner.to)
          const isExternalHref = Boolean(partner.href && !partner.href.startsWith('/'))
          const Wrapper = partner.href ? 'a' : partner.to ? Link : 'div'
          const wrapperProps = partner.href
            ? {
                href: partner.href,
                target: isExternalHref ? '_blank' : undefined,
                rel: isExternalHref ? 'noreferrer noopener' : undefined,
                'aria-label': `Visit ${partner.name}`,
              }
            : partner.to
              ? { to: partner.to, 'aria-label': `Visit ${partner.name}` }
              : {}

          return (
            <Wrapper
              key={partner.name}
              className={cn(
                'marketing-partner-card',
                hasLink ? 'marketing-partner-card--interactive' : 'marketing-partner-card--static',
                variant === 'flush' && 'marketing-partner-card--flush'
              )}
              {...wrapperProps}
            >
              <div className="marketing-partner-card__header">
                <div className="marketing-partner-card__identity">
                  <span className="marketing-partner-card__avatar">
                    {partner.avatar ? <img src={partner.avatar} alt="" aria-hidden="true" /> : renderSlot(partner.icon)}
                  </span>
                  <div className="marketing-partner-card__meta">
                    <span className="marketing-partner-card__name">{partner.name}</span>
                    {partner.role ? <span className="marketing-partner-card__role">{partner.role}</span> : null}
                  </div>
                </div>
                {hasLink ? (
                  <span className="marketing-partner-card__arrow" aria-hidden="true">
                    <ArrowTopRightOnSquareIcon className="w-4 h-4 cta-button__icon" />
                  </span>
                ) : null}
              </div>
              {partner.description ? <p className="marketing-partner-card__copy">{partner.description}</p> : null}
            </Wrapper>
          )
        })}
      </MarketingGridSeam>
    </MarketingBand>
  )
)

MarketingPartnerSurface.displayName = 'MarketingPartnerSurface'

export default MarketingPartnerSurface
