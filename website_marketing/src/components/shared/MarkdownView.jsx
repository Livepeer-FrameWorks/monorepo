const CALLOUT_VARIANTS = {
  note: 'Note',
  info: 'Info',
  tip: 'Tip',
  warning: 'Warning',
  danger: 'Alert',
}

const CALLOUT_ALIASES = {
  caution: 'warning',
  important: 'info',
  success: 'tip',
  failure: 'danger',
  alert: 'danger',
}

const normalizeCallout = (value) => {
  if (!value) return null
  const key = value.toLowerCase()
  if (CALLOUT_VARIANTS[key]) return key
  if (CALLOUT_ALIASES[key]) return CALLOUT_ALIASES[key]
  return null
}

const sanitizeFenceLang = (value) => {
  if (!value) return { className: '', label: '' }
  const raw = value.trim().toLowerCase()
  const aliases = {
    js: 'javascript',
    ts: 'typescript',
    sh: 'shell',
    shell: 'shell',
    bash: 'shell',
    'c#': 'csharp',
    'c++': 'cpp',
    'f#': 'fsharp',
  }
  const normalized = aliases[raw] || raw.replace(/[^a-z0-9+-]/g, '')
  return {
    className: normalized,
    label: raw.toUpperCase(),
  }
}

const mdToHtml = (md) => {
  const esc = (s) => s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')

  const escAttr = (s) => esc(s).replace(/"/g, '&quot;').replace(/'/g, '&#39;')

  const applyInline = (value, { skipLinks } = {}) => {
    let text = esc(value)

    if (!skipLinks) {
      text = text.replace(/\[([^\]]+)]\(([^)]+)\)/g, (_, label, href) => {
        const safeHref = escAttr(href.trim())
        const isExternal = /^https?:\/\//i.test(href.trim())
        const labelHtml = applyInline(label, { skipLinks: true })
        const attrs = isExternal ? ' target="_blank" rel="noreferrer"' : ''
        return `<a class="md-link"${attrs} href="${safeHref}">${labelHtml}</a>`
      })
    }

    text = text.replace(/`([^`]+)`/g, (_, code) => `<code class="md-inline-code">${code}</code>`)
    text = text.replace(/\*\*(.*?)\*\*/g, '<strong>$1</strong>')
    text = text.replace(/\*(?!\*)([^*]+)\*/g, '<em>$1</em>')
    text = text.replace(/~~(.*?)~~/g, '<del>$1</del>')

    return text
  }

  const toParagraphs = (lines) => {
    const paragraphs = []
    let buffer = []
    for (const entry of lines) {
      if (!entry.trim()) {
        if (buffer.length) {
          paragraphs.push(buffer.join(' '))
          buffer = []
        }
      } else {
        buffer.push(entry.trim())
      }
    }
    if (buffer.length) paragraphs.push(buffer.join(' '))
    return paragraphs
  }

  const renderParagraphs = (lines) => toParagraphs(lines)
    .map((paragraph) => `<p class="md-paragraph">${applyInline(paragraph)}</p>`)
    .join('')

  const lines = md.replace(/\r\n?/g, '\n').split('\n')
  let html = ''
  let inList = false
  let listTag = null
  let inCode = false
  let codeClass = ''
  let codeLabel = ''
  let codeLines = []
  let blockQuoteLines = null
  let blockQuoteMeta = null

  const closeList = () => {
    if (!inList) return
    html += `</${listTag}>`
    inList = false
    listTag = null
  }

  const closeCode = () => {
    if (!inCode) return
    const codeHtml = codeLines.map((line) => esc(line)).join('\n')
    const langAttr = codeLabel ? ` data-lang="${escAttr(codeLabel)}"` : ''
    const classAttr = codeClass ? ` language-${codeClass}` : ''
    html += `<pre class="md-code-block"${langAttr}><code class="md-code-block__code${classAttr}">${codeHtml}</code></pre>`
    inCode = false
    codeLines = []
    codeClass = ''
    codeLabel = ''
  }

  const closeBlockQuote = () => {
    if (!blockQuoteLines) return
    if (blockQuoteMeta && blockQuoteMeta.kind) {
      const variant = blockQuoteMeta.kind
      const titleText = blockQuoteMeta.title || CALLOUT_VARIANTS[variant]
      html += `<div class="md-callout md-callout--${variant}">`
      if (titleText) {
        html += `<div class="md-callout__title">${applyInline(titleText)}</div>`
      }
      if (blockQuoteLines.length) {
        html += `<div class="md-callout__body">${renderParagraphs(blockQuoteLines)}</div>`
      }
      html += '</div>'
    } else {
      html += `<blockquote class="md-quote">${renderParagraphs(blockQuoteLines)}</blockquote>`
    }
    blockQuoteLines = null
    blockQuoteMeta = null
  }

  for (const rawLine of lines) {
    const line = rawLine.trimEnd()
    const trimmed = line.trim()

    if (inCode) {
      if (trimmed.startsWith('```')) {
        closeCode()
      } else {
        codeLines.push(rawLine.replace(/\r$/, ''))
      }
      continue
    }

    if (!trimmed) {
      closeBlockQuote()
      closeList()
      continue
    }

    const leading = line.replace(/^\s+/, '')

    if (leading.startsWith('```')) {
      closeBlockQuote()
      closeList()
      const { className, label } = sanitizeFenceLang(leading.slice(3).trim())
      inCode = true
      codeClass = className
      codeLabel = label
      codeLines = []
      continue
    }

    const horizontalRuleToken = leading.replace(/\s+/g, '')
    if (horizontalRuleToken === '---' || horizontalRuleToken === '***') {
      closeBlockQuote()
      closeList()
      html += '<hr class="md-hr" />'
      continue
    }

    if (leading.startsWith('>')) {
      closeList()
      if (!blockQuoteLines) {
        blockQuoteLines = []
        blockQuoteMeta = null
      }
      const content = leading.replace(/^>\s?/, '').trimStart()
      if (blockQuoteLines.length === 0) {
        const directive = content.match(/^\[!(\w+)]\s*(.*)$/)
        if (directive) {
          const kind = normalizeCallout(directive[1])
          if (kind) {
            blockQuoteMeta = { kind, title: directive[2].trim() }
            continue
          }
        }
        const labelled = content.match(/^\*\*(Note|Info|Tip|Warning|Caution|Danger|Important|Success|Alert)\*?:\s*(.*)$/i)
        if (labelled) {
          const kind = normalizeCallout(labelled[1])
          if (kind) {
            blockQuoteMeta = {
              kind,
              title: labelled[1].slice(0, 1).toUpperCase() + labelled[1].slice(1).toLowerCase(),
            }
            const remainder = labelled[2].trim()
            if (remainder) blockQuoteLines.push(remainder)
            continue
          }
        }
      }
      blockQuoteLines.push(content)
      continue
    }

    closeBlockQuote()

    const unorderedMatch = leading.match(/^([-*+])\s+(.*)$/)
    if (unorderedMatch) {
      const content = unorderedMatch[2]
      if (!inList || listTag !== 'ul') {
        closeList()
        html += '<ul class="md-list md-list--bullet">'
        inList = true
        listTag = 'ul'
      }
      html += `<li class="md-list__item">${applyInline(content)}</li>`
      continue
    }

    const orderedMatch = leading.match(/^(\d+)[.)]\s+(.*)$/)
    if (orderedMatch) {
      const content = orderedMatch[2]
      if (!inList || listTag !== 'ol') {
        closeList()
        html += '<ol class="md-list md-list--numbered">'
        inList = true
        listTag = 'ol'
      }
      html += `<li class="md-list__item">${applyInline(content)}</li>`
      continue
    }

    closeList()

    if (leading.startsWith('### ')) {
      html += `<h3 class="md-heading md-heading--3">${applyInline(leading.slice(4))}</h3>`
      continue
    }

    if (leading.startsWith('## ')) {
      html += `<h2 class="md-heading md-heading--2">${applyInline(leading.slice(3))}</h2>`
      continue
    }

    if (leading.startsWith('# ')) {
      html += `<h1 class="md-heading md-heading--1">${applyInline(leading.slice(2))}</h1>`
      continue
    }

    html += `<p class="md-paragraph">${applyInline(leading)}</p>`
  }

  closeCode()
  closeBlockQuote()
  closeList()

  return html
}

const MarkdownView = ({ markdown }) => {
  const __html = mdToHtml(markdown)
  return <div className="markdown-view prose-invert max-w-none" dangerouslySetInnerHTML={{ __html }} />
}

export default MarkdownView
