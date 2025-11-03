import { isValidElement } from 'react'
import { cn } from '@/lib/utils'
import { renderSlot } from '../utils'
import MarketingStackedSeam from '../layout/MarketingStackedSeam'

const normalizeListItem = (item, index, fallbackTone, prefix) => {
  if (item === null || item === undefined) return null

  if (isValidElement(item) || typeof item === 'string' || typeof item === 'number') {
    return {
      key: `${prefix}-${index}`,
      content: item,
      tone: fallbackTone,
    }
  }

  if (typeof item === 'object') {
    const key = item.id ?? item.key ?? `${prefix}-${index}`
    const tone = item.tone ?? fallbackTone
    const content = item.content ?? item.children ?? item.label ?? item.text ?? item.value ?? null
    if (content === null) return null

    return {
      key,
      content,
      tone,
    }
  }

  return {
    key: `${prefix}-${index}`,
    content: String(item),
    tone: fallbackTone,
  }
}

const MarketingComparisonCard = ({
  badge,
  title,
  description,
  price,
  period,
  tone = 'accent',
  meta,
  features = [],
  limitations = [],
  limitHeading = 'Limitations',
  featured = false,
  footnote,
  action,
}) => {
  const normalizedFeatures = features
    .map((feature, index) => normalizeListItem(feature, index, tone, 'feature'))
    .filter(Boolean)

  const normalizedLimits = limitations
    .map((limit, index) => normalizeListItem(limit, index, 'muted', 'limit'))
    .filter(Boolean)

  return (
    <div className={cn('marketing-comparison-card', featured && 'marketing-comparison-card--featured')} data-tone={tone}>
      <div className="marketing-comparison-card__header">
        {badge ? <span className="marketing-comparison-card__badge">{badge}</span> : null}
        {title ? <h3 className="marketing-comparison-card__title">{title}</h3> : null}
        {price ? (
          <div className="marketing-comparison-card__price">
            <span className="marketing-comparison-card__amount">{price}</span>
            {period ? <span className="marketing-comparison-card__period">{period}</span> : null}
          </div>
        ) : null}
        {meta ? <p className="marketing-comparison-card__meta">{renderSlot(meta)}</p> : null}
        {description ? <p className="marketing-comparison-card__description">{description}</p> : null}
      </div>
      {normalizedFeatures.length ? (
        <MarketingStackedSeam gap="sm" className="marketing-comparison-features">
          {normalizedFeatures.map((feature) => (
            <div key={feature.key} className="marketing-comparison-feature">
              <span className="marketing-comparison-feature__dot" data-tone={feature.tone} />
              <span>{renderSlot(feature.content)}</span>
            </div>
          ))}
        </MarketingStackedSeam>
      ) : null}
      {normalizedLimits.length ? (
        <div className="marketing-comparison-card__limits">
          <span className="marketing-comparison-card__limits-title">{limitHeading}</span>
          <MarketingStackedSeam gap="sm" className="marketing-comparison-limits">
            {normalizedLimits.map((limit) => (
              <div key={limit.key} className="marketing-comparison-limit">
                <span className="marketing-comparison-feature__dot" data-tone={limit.tone} />
                <span>{renderSlot(limit.content)}</span>
              </div>
            ))}
          </MarketingStackedSeam>
        </div>
      ) : null}
      <div className="marketing-comparison-card__footer">
        {action}
        {footnote ? <p className="marketing-comparison-card__note">{footnote}</p> : null}
      </div>
    </div>
  )
}

export default MarketingComparisonCard
