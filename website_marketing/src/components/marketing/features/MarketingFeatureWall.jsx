import { cloneElement, forwardRef, isValidElement, useEffect, useLayoutEffect, useMemo, useState } from 'react'
import { cn } from '@/lib/utils'
import { motion } from 'framer-motion'
import { renderSlot } from '../utils'
import MarketingIconBadge from '../misc/MarketingIconBadge'
import MarketingFeatureCard from './MarketingFeatureCard'

const FEATURE_WALL_BREAKPOINTS = ['sm', 'md', 'lg', 'xl']
const FEATURE_WALL_MEDIA = {
  sm: '(min-width: 40rem)',
  md: '(min-width: 48rem)',
  lg: '(min-width: 64rem)',
  xl: '(min-width: 80rem)',
}
const useIsomorphicLayoutEffect = typeof window !== 'undefined' ? useLayoutEffect : useEffect

const computeColumnDivisors = (itemCount, maxColumns) => {
  const safeCount = Math.max(0, itemCount ?? 0)
  const safeMax = Math.max(1, maxColumns ?? 1)

  if (safeCount <= 1) {
    return [1]
  }

  const limit = Math.min(safeCount, safeMax)
  const divisors = new Set([1])

  for (let candidate = 2; candidate <= limit; candidate += 1) {
    if (safeCount % candidate === 0) {
      divisors.add(candidate)
    }
  }

  return Array.from(divisors).sort((a, b) => a - b)
}

const pickDivisorUpToLimit = (divisors, limit) => {
  if (!Array.isArray(divisors) || divisors.length === 0) {
    return 1
  }

  const safeLimit = Math.max(1, Math.floor(limit ?? 1))
  let result = divisors[0]

  for (const value of divisors) {
    if (value > safeLimit) {
      break
    }
    result = value
  }

  return result
}

const MarketingFeatureWall = forwardRef(
  (
    {
      items = [],
      columns = 4,
      stackAt = 'md',
      // eslint-disable-next-line no-unused-vars
      gap = 'md',
      renderItem,
      variant = 'card',
      flush = false,
      hover = 'lift',
      stripe = true,
      metaAlign = 'start',
      responsiveCols,
      className,
      style,
      ...props
    },
    ref
  ) => {
    const totalItems = Array.isArray(items) ? items.length : 0
    const maxColumns = Math.max(1, columns)
    const stackIndex = Math.max(0, FEATURE_WALL_BREAKPOINTS.indexOf(stackAt))
    const hasResponsiveOverride = responsiveCols && Object.keys(responsiveCols).length > 0

    const columnDivisors = useMemo(
      () => computeColumnDivisors(totalItems, maxColumns),
      [totalItems, maxColumns]
    )
    const baseDivisor = columnDivisors[0] ?? 1

    const columnPlan = useMemo(() => {
      if (hasResponsiveOverride) {
        const normalized = { ...responsiveCols }
        if (normalized.base == null) {
          normalized.base = baseDivisor
        }

        let previous = pickDivisorUpToLimit(columnDivisors, normalized.base)
        normalized.base = previous

        FEATURE_WALL_BREAKPOINTS.forEach((bp) => {
          if (normalized[bp] == null) {
            return
          }

          const candidate = pickDivisorUpToLimit(columnDivisors, normalized[bp])
          const resolved = candidate >= previous ? candidate : previous
          normalized[bp] = resolved
          previous = resolved
        })

        return normalized
      }

      const plan = { base: baseDivisor }
      const progressiveDivisors = columnDivisors.slice(1)
      let previous = baseDivisor
      let pointer = 0

      FEATURE_WALL_BREAKPOINTS.forEach((bp, index) => {
        if (index < stackIndex) {
          plan[bp] = previous
          return
        }

        if (progressiveDivisors.length === 0) {
          plan[bp] = previous
          return
        }

        let candidate = previous
        if (pointer < progressiveDivisors.length) {
          const nextDivisor = progressiveDivisors[pointer]
          if (nextDivisor > previous) {
            candidate = nextDivisor
            pointer += 1
          }
        }

        plan[bp] = candidate
        previous = candidate
      })

      return plan
    }, [hasResponsiveOverride, responsiveCols, columnDivisors, baseDivisor, stackIndex])

    const baseColumns = Math.max(1, columnPlan.base ?? baseDivisor)
    const planValues = [
      columnPlan.base,
      ...FEATURE_WALL_BREAKPOINTS.map((bp) => columnPlan[bp]).filter(
        (value) => typeof value === 'number' && !Number.isNaN(value)
      ),
    ]
    const maxResponsiveColumns = planValues.length ? Math.max(...planValues) : baseColumns

    const [activeColumns, setActiveColumns] = useState(baseColumns)

    useIsomorphicLayoutEffect(() => {
      if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
        setActiveColumns(baseColumns)
        return undefined
      }

      const listeners = FEATURE_WALL_BREAKPOINTS.map((bp) => ({
        bp,
        media: window.matchMedia(FEATURE_WALL_MEDIA[bp]),
      }))

      const resolve = () => {
        let resolved = columnPlan.base ?? baseColumns
        listeners.forEach(({ bp, media }) => {
          if (media.matches) {
            const candidate = columnPlan[bp]
            if (typeof candidate === 'number' && !Number.isNaN(candidate)) {
              resolved = candidate
            }
          }
        })
        resolved = Math.max(1, resolved)
        setActiveColumns((previous) => (previous === resolved ? previous : resolved))
      }

      listeners.forEach(({ media }) => {
        if (typeof media.addEventListener === 'function') {
          media.addEventListener('change', resolve)
        } else if (typeof media.addListener === 'function') {
          media.addListener(resolve)
        }
      })

      resolve()

      return () => {
        listeners.forEach(({ media }) => {
          if (typeof media.removeEventListener === 'function') {
            media.removeEventListener('change', resolve)
          } else if (typeof media.removeListener === 'function') {
            media.removeListener(resolve)
          }
        })
      }
    }, [columnPlan, baseColumns])

    const buildWallCard = (item) => {
      const Icon = item.icon
      const toneClass = item.tone ? `marketing-feature--tone-${item.tone}` : null
      const tileVariant = item.cardVariant === 'tile'
      const shouldShowStripe = item.stripe ?? stripe ?? true
      const titleRowClass = cn('marketing-feature__title-row', tileVariant && 'marketing-feature__title-row--tile')
      const headerClass = cn('marketing-feature__header', tileVariant && 'marketing-feature__header--tile')
      const iconClass = cn(
        'marketing-feature__icon',
        tileVariant ? 'marketing-feature__icon--tile' : 'marketing-feature__icon--neutral'
      )

      return (
        <div className={cn('marketing-feature', toneClass, tileVariant && 'marketing-feature--tile', !shouldShowStripe && 'marketing-feature--no-stripe')}>
          <div className={headerClass}>
            <div className={titleRowClass}>
              {Icon ? (
                <MarketingIconBadge
                  tone={item.iconTone ?? item.tone ?? 'accent'}
                  variant="neutral"
                  className={iconClass}
                >
                  <Icon className="marketing-feature__icon-symbol" />
                </MarketingIconBadge>
              ) : null}
              <div className="marketing-feature__heading">
                {item.title ? <span className="marketing-feature__title">{item.title}</span> : null}
                {item.badge ? <span className="marketing-feature__subtitle">{item.badge}</span> : null}
              </div>
            </div>
            {item.meta ? <div className="marketing-feature__meta">{renderSlot(item.meta)}</div> : null}
          </div>
          {item.description ? <p className="marketing-feature__copy">{item.description}</p> : null}
          {item.children ? renderSlot(item.children) : null}
        </div>
      )
    }

    const renderSeamedNode = (item, index, { flushMode = false } = {}) => {
      const seamClasses = [
        'marketing-feature__cell',
        flushMode && 'marketing-feature-grid__cell',
      ]

      const effectiveColumns = Math.max(1, activeColumns)
      const colPosition = (index % effectiveColumns) + 1
      const rowPosition = Math.floor(index / effectiveColumns) + 1
      const totalRows = Math.ceil(totalItems / effectiveColumns) || 1

      const hasRight = effectiveColumns > 1 && colPosition < effectiveColumns && index + 1 < totalItems
      const hasBottom = rowPosition < totalRows && index + effectiveColumns < totalItems

      if (hasRight) {
        seamClasses.push('marketing-feature__cell--has-right')
      }
      if (hasBottom) {
        seamClasses.push('marketing-feature__cell--has-bottom')
      }

      const cellClass = cn(...seamClasses)
      const key = item.key ?? item.title ?? index

      const applySeamProps = (node, { animate } = { animate: false }) => {
        if (!isValidElement(node)) {
          return (
            <div key={key} className={cellClass}>
              {node}
            </div>
          )
        }

        const mergedClassName = cn(node.props.className, cellClass)

        if (animate) {
          const MotionComponent = motion(node.type)
          // eslint-disable-next-line no-unused-vars
        const { className: childClassName, children: childChildren, ...restProps } = node.props

          return (
            <MotionComponent
              key={key}
              {...restProps}
              className={mergedClassName}
              initial={{ opacity: 0, y: 24 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.5, delay: (item.motionDelay ?? index) * 0.08 }}
            >
              {childChildren}
            </MotionComponent>
          )
        }

        return cloneElement(node, {
          key,
          className: mergedClassName,
        })
      }

      const isCustomRender = Boolean(renderItem)

      const content = isCustomRender
        ? renderItem(item, index)
        : flushMode
          ? (
            <MarketingFeatureCard
              hover={item.hover ?? hover}
              flush
              stripe={item.stripe ?? stripe}
              metaAlign={item.metaAlign ?? metaAlign}
              {...item}
            />
          )
          : buildWallCard(item)

      if (isCustomRender || item.motion === false) {
        return applySeamProps(content)
      }

      return applySeamProps(content, { animate: true })
    }

    const wallResponsiveClasses = FEATURE_WALL_BREAKPOINTS.map((bp) => {
      const value = columnPlan?.[bp]
      if (!value) return null
      return `marketing-feature-wall--${bp}-cols-${value}`
    }).filter(Boolean)

    wallResponsiveClasses.unshift(`marketing-feature-wall--cols-${baseColumns}`)

    const gridResponsiveClasses = FEATURE_WALL_BREAKPOINTS.map((bp) => {
      const value = columnPlan?.[bp]
      if (!value) return null
      return `marketing-feature-grid--${bp}-cols-${value}`
    }).filter(Boolean)

    gridResponsiveClasses.unshift(`marketing-feature-grid--cols-${baseColumns}`)

    const useWallLayout = !flush && variant !== 'grid'

    if (useWallLayout) {
      return (
        <div
          ref={ref}
          className={cn('marketing-feature-wall', wallResponsiveClasses, gridResponsiveClasses, className)}
          data-cols={maxResponsiveColumns}
          style={style}
          {...props}
        >
          {items.map((item, index) => renderSeamedNode(item, index))}
        </div>
      )
    }

    const flushMode = flush || variant === 'grid'
    const gridClassName = cn(
      'marketing-feature-grid',
      flushMode && 'marketing-feature-grid--flush',
      variant && `marketing-feature-grid--${variant}`,
      gridResponsiveClasses,
      className
    )

    return (
      <div
        ref={ref}
        className={gridClassName}
        style={style}
        data-cols={maxResponsiveColumns}
        {...props}
      >
        {items.map((item, index) => renderSeamedNode(item, index, { flushMode }))}
      </div>
    )
  }
)

MarketingFeatureWall.displayName = 'MarketingFeatureWall'

export default MarketingFeatureWall
