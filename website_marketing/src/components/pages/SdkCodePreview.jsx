import { useState } from 'react'
import { motion, AnimatePresence } from 'framer-motion'
import { PlayIcon, ArrowUpTrayIcon, CpuChipIcon, ClipboardDocumentCheckIcon, ClipboardDocumentIcon } from '@heroicons/react/24/outline'
import { cn } from '@/lib/utils'
import config from '../../config'

const snippetPlayer = `import { Player } from '@livepeer-frameworks/player-react'

export const MyStream = () => (
  <Player
    contentId="pk_29f8..."
    contentType="live"
    options={{
      autoplay: true,
      muted: true,
      gatewayUrl: "${config.gatewayUrl}"
    }}
  />
)`

const snippetIngest = `import { useStreamCrafter } from '@livepeer-frameworks/streamcrafter-react'

export const Broadcaster = () => {
  const { startStreaming, isStreaming } = useStreamCrafter({
    streamKey: "sk_92d1...",
    gatewayUrl: "${config.gatewayUrl}"
  })

  return (
    <button onClick={startStreaming}>
      {isStreaming ? 'Stop' : 'Go Live'}
    </button>
  )
}`

const snippetAgent = `// Add to your MCP client config
{
  "mcpServers": {
    "frameworks": {
      "url": "${config.mcpUrl}",
      "headers": {
        "Authorization": "Bearer $API_TOKEN"
      }
    }
  }
}

// Then ask your AI:
// "Create a stream called My Broadcast"
// Agent pays via x402 if balance is low`

export default function SdkCodePreview({ variant = 'default', className }) {
  const [activeTab, setActiveTab] = useState('player')
  const [copied, setCopied] = useState(false)

  const snippets = {
    player: snippetPlayer,
    ingest: snippetIngest,
    agent: snippetAgent,
  }

  const langLabels = {
    player: 'React / TSX',
    ingest: 'React / TSX',
    agent: 'JSON',
  }

  const handleCopy = () => {
    navigator.clipboard.writeText(snippets[activeTab])
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  const tabs = [
    { id: 'player', label: 'Player SDK', icon: PlayIcon },
    { id: 'ingest', label: 'StreamCrafter', icon: ArrowUpTrayIcon },
    { id: 'agent', label: 'AI Agents', icon: CpuChipIcon },
  ]

  return (
    <div className={cn(
      'marketing-code-panel w-full h-full min-h-[320px] flex flex-col',
      variant === 'flush' && 'marketing-code-panel--flush',
      className
    )}>
      {/* Header / Tabs */}
      <div className="marketing-code-panel__header">
        <div className="flex space-x-1">
          {tabs.map((tab) => (
            <button
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={cn(
                "flex items-center gap-2 px-3 py-1.5 text-xs font-medium rounded-md transition-all outline-none",
                activeTab === tab.id
                  ? "bg-primary/10 text-primary border border-primary/20 shadow-sm"
                  : "text-muted-foreground hover:text-foreground hover:bg-white/5 border border-transparent"
              )}
            >
              <tab.icon className="w-3.5 h-3.5" />
              {tab.label}
            </button>
          ))}
        </div>
        <div className="marketing-code-panel__actions">
            <span className="text-[10px] font-bold tracking-widest uppercase text-muted-foreground/60 hidden sm:inline-block">{langLabels[activeTab]}</span>
            <button
                onClick={handleCopy}
                className="text-muted-foreground hover:text-foreground transition-colors p-1 rounded-md hover:bg-white/5"
                title="Copy to clipboard"
            >
                {copied ? <ClipboardDocumentCheckIcon className="w-4 h-4 text-green-400" /> : <ClipboardDocumentIcon className="w-4 h-4" />}
            </button>
        </div>
      </div>

      {/* Code Body */}
      <div className="marketing-code-panel__body flex-1 relative font-mono text-sm overflow-hidden">
         <AnimatePresence mode="wait">
            <motion.div
                key={activeTab}
                initial={{ opacity: 0, y: 5 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: -5 }}
                transition={{ duration: 0.15 }}
                className="absolute inset-0 p-6 overflow-auto custom-scrollbar"
            >
                <pre className="text-blue-100/90 leading-relaxed">
                    <code>
                        {snippets[activeTab]}
                    </code>
                </pre>
            </motion.div>
         </AnimatePresence>
      </div>
    </div>
  )
}
