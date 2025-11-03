import { motion } from 'framer-motion'
import { Badge } from '@/components/ui/badge'
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from '@/components/ui/accordion'

const MotionAccordionItem = motion(AccordionItem)

const MarketingTimeline = ({ items = [] }) => {
  if (!items.length) return null

  const stringify = (value, index = 0) =>
    `item-${String(value ?? index)
      .toLowerCase()
      .replace(/\s+/g, '-')}-${index}`

  const defaultValue = undefined

  return (
    <Accordion type="single" collapsible defaultValue={defaultValue} className="marketing-timeline">
      {items.map((item, index) => {
        const value = stringify(item.year, index)
        const Icon = item.icon
        const badges = Array.isArray(item.badges) ? item.badges : []
        const points = Array.isArray(item.points) ? item.points : []
        const summary = item.summary ?? item.description ?? null
        return (
          <MotionAccordionItem
            key={value}
            value={value}
            initial={{ opacity: 0, y: 20 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true, amount: 0.2 }}
            transition={{ duration: 0.45, delay: index * 0.06 }}
            className="marketing-timeline__item"
          >
            <AccordionTrigger className="marketing-timeline__trigger">
              <div className="marketing-timeline__row">
                <span className="marketing-timeline__icon" aria-hidden="true">
                  {Icon ? <Icon /> : null}
                </span>
                <div className="marketing-timeline__content">
                  <div className="marketing-timeline__title-row">
                    {item.year ? <span className="marketing-timeline__year">{item.year}</span> : null}
                    <span className="marketing-timeline__title">{item.title}</span>
                  </div>
                  {item.subtitle ? <span className="marketing-timeline__subtitle">{item.subtitle}</span> : null}
                </div>
              </div>
              {badges.length ? (
                <div className="marketing-timeline__badges">
                  {badges.map((badge) => (
                    <Badge key={`${value}-${badge}`} variant="outline" className="marketing-timeline__badge">
                      {badge}
                    </Badge>
                  ))}
                </div>
              ) : null}
            </AccordionTrigger>
            <AccordionContent className="marketing-timeline__panel">
              <div className="marketing-timeline__body">
                {summary ? <p className="marketing-timeline__summary">{summary}</p> : null}
                {points.length ? (
                  <ul className="marketing-timeline__list">
                    {points.map((point) => (
                      <li key={point} className="marketing-timeline__list-item">
                        <span className="marketing-timeline__bullet" aria-hidden="true" />
                        <span>{point}</span>
                      </li>
                    ))}
                  </ul>
                ) : null}
              </div>
            </AccordionContent>
          </MotionAccordionItem>
        )
      })}
    </Accordion>
  )
}

export default MarketingTimeline
