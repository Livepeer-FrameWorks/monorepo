import { useEffect, useState } from 'react'

const MarketingScrollProgress = () => {
  const [progress, setProgress] = useState(0)

  useEffect(() => {
    const update = () => {
      const { scrollTop, scrollHeight, clientHeight } = document.documentElement
      const total = scrollHeight - clientHeight
      const value = total > 0 ? (scrollTop / total) * 100 : 0
      setProgress(value)
    }

    update()
    window.addEventListener('scroll', update, { passive: true })
    return () => window.removeEventListener('scroll', update)
  }, [])

  return (
    <div className="marketing-scroll-progress" aria-hidden="true">
      <div className="marketing-scroll-progress__bar" style={{ width: `${progress}%` }} />
    </div>
  )
}

export default MarketingScrollProgress
