import { useState } from "react";
import { motion, AnimatePresence } from "framer-motion";
import {
  PlayIcon,
  ArrowUpTrayIcon,
  CodeBracketIcon,
  CubeIcon,
  ClipboardDocumentCheckIcon,
  ClipboardDocumentIcon,
} from "@heroicons/react/24/outline";
import { cn } from "@/lib/utils";
import config from "../../config";

const snippetPlayer = `import { Player } from '@livepeer-frameworks/player-react'

export const MyStream = ({ playbackId }) => (
  <Player
    contentId={playbackId}
    contentType="live"
    options={{
      autoplay: true,
      muted: true,
      gatewayUrl: "${config.gatewayUrl}"
    }}
  />
)`;

const snippetIngest = `<script>
  import { StreamCrafter } from '@livepeer-frameworks/streamcrafter-svelte'

  let { whipUrl, streamName } = $props()
</script>

<div class="studio">
  <h2>Broadcasting: {streamName}</h2>
  <StreamCrafter
    {whipUrl}
    initialProfile="broadcast"
    onStateChange={(state) => console.log('State:', state)}
    onError={(err) => console.error(err)}
  />
</div>`;

const snippetWebComponent = `<!-- IIFE via npm CDN — no bundler needed -->
<!-- unpkg -->
<script src="https://unpkg.com/@livepeer-frameworks/player-wc/dist/fw-player.iife.js"></script>
<!-- or jsdelivr -->
<script src="https://cdn.jsdelivr.net/npm/@livepeer-frameworks/player-wc/dist/fw-player.iife.js"></script>

<fw-player
  content-id="your-playback-id"
  content-type="live"
  gateway-url="${config.gatewayUrl}"
  autoplay muted controls
></fw-player>`;

const snippetGraphql = `query LiveStreams {
  streamsConnection(page: { first: 10 }) {
    edges {
      node {
        name
        playbackId
        streamKey
        metrics {
          status
          isLive
          currentViewers
        }
      }
    }
    pageInfo { hasNextPage endCursor }
  }
}`;

export default function SdkCodePreview({ variant = "default", className }) {
  const [activeTab, setActiveTab] = useState("player");
  const [copied, setCopied] = useState(false);

  const snippets = {
    player: snippetPlayer,
    ingest: snippetIngest,
    wc: snippetWebComponent,
    graphql: snippetGraphql,
  };

  const langLabels = {
    player: "React / TSX",
    ingest: "Svelte 5",
    wc: "HTML",
    graphql: "GraphQL",
  };

  const handleCopy = () => {
    navigator.clipboard.writeText(snippets[activeTab]);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const tabs = [
    { id: "player", label: "Player SDK", icon: PlayIcon },
    { id: "ingest", label: "StreamCrafter", icon: ArrowUpTrayIcon },
    { id: "wc", label: "Web Components", icon: CubeIcon },
    { id: "graphql", label: "GraphQL", icon: CodeBracketIcon },
  ];

  return (
    <div
      className={cn(
        "marketing-code-panel w-full h-full min-h-[320px] flex flex-col",
        variant === "flush" && "marketing-code-panel--flush",
        className
      )}
    >
      {/* Header / Tabs */}
      <div className="marketing-code-panel__header">
        <div className="flex flex-wrap gap-1">
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
          <span className="text-[10px] font-bold tracking-widest uppercase text-muted-foreground/60 hidden sm:inline-block">
            {langLabels[activeTab]}
          </span>
          <button
            onClick={handleCopy}
            className="text-muted-foreground hover:text-foreground transition-colors p-1 rounded-md hover:bg-white/5"
            title="Copy to clipboard"
          >
            {copied ? (
              <ClipboardDocumentCheckIcon className="w-4 h-4 text-green-400" />
            ) : (
              <ClipboardDocumentIcon className="w-4 h-4" />
            )}
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
              <code>{snippets[activeTab]}</code>
            </pre>
          </motion.div>
        </AnimatePresence>
      </div>
    </div>
  );
}
