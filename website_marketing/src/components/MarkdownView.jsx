import React from 'react'

// Very small markdown renderer: supports #, ##, ###, -, and inline **text**
const mdToHtml = (md) => {
  const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  const lines = md.split(/\r?\n/)
  let html = ''
  let inList = false
  for (let raw of lines) {
    const line = raw.trimEnd()
    if (!line) {
      if (inList) { html += '</ul>' ; inList = false }
      html += '<p class="my-2"></p>'
      continue
    }
    if (line.startsWith('### ')) {
      if (inList) { html += '</ul>'; inList = false }
      html += `<h3 class="text-lg font-semibold text-tokyo-night-fg mt-4 mb-2">${esc(line.slice(4))}</h3>`
    } else if (line.startsWith('## ')) {
      if (inList) { html += '</ul>'; inList = false }
      html += `<h2 class="text-2xl font-bold gradient-text mt-6 mb-3">${esc(line.slice(3))}</h2>`
    } else if (line.startsWith('# ')) {
      if (inList) { html += '</ul>'; inList = false }
      html += `<h1 class="text-3xl font-bold gradient-text mt-6 mb-3">${esc(line.slice(2))}</h1>`
    } else if (line.startsWith('- ')) {
      if (!inList) { html += '<ul class="list-disc pl-6 space-y-1 text-tokyo-night-fg-dark">'; inList = true }
      const content = esc(line.slice(2)).replace(/\*\*(.*?)\*\*/g, '<strong>$1<\/strong>')
      html += `<li>${content}</li>`
    } else {
      const content = esc(line).replace(/\*\*(.*?)\*\*/g, '<strong>$1<\/strong>')
      html += `<p class="text-tokyo-night-fg-dark">${content}</p>`
    }
  }
  if (inList) html += '</ul>'
  return html
}

const MarkdownView = ({ markdown }) => {
  const __html = mdToHtml(markdown)
  return <div className="prose prose-invert max-w-none" dangerouslySetInnerHTML={{ __html }} />
}

export default MarkdownView

