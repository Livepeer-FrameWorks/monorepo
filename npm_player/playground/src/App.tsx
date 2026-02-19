import { useState, useCallback, useEffect } from "react";
import { PlaygroundProvider } from "@/context/PlaygroundContext";
import { Header } from "@/components/Header";
import { WorkspaceLayout } from "@/components/WorkspaceLayout";
import { ConnectionSlab } from "@/components/ConnectionSlab";
import { IngestUrisSlab } from "@/components/IngestUrisSlab";
import { PlaybackSourcesSlab } from "@/components/PlaybackSourcesSlab";
import { StreamsSlab } from "@/components/StreamsSlab";
import { WhipPublisher } from "@/components/WhipPublisher";
import { PlayerPreview } from "@/components/PlayerPreview";

const BREAKPOINT = 1024;

function ConfigPanel() {
  return (
    <>
      <ConnectionSlab />
      <div className="slab-section-label slab-section-label--studio">Ingest</div>
      <IngestUrisSlab />
      <div className="slab-section-label slab-section-label--player">Playback</div>
      <PlaybackSourcesSlab />
      <StreamsSlab />
    </>
  );
}

function MediaPanel() {
  return (
    <>
      <PlayerPreview />
      <WhipPublisher />
    </>
  );
}

export default function App() {
  const [drawerOpen, setDrawerOpen] = useState(false);

  const toggleDrawer = useCallback(() => setDrawerOpen((o) => !o), []);
  const closeDrawer = useCallback(() => setDrawerOpen(false), []);

  useEffect(() => {
    const mq = window.matchMedia(`(min-width: ${BREAKPOINT}px)`);
    const handler = () => {
      if (mq.matches) setDrawerOpen(false);
    };
    mq.addEventListener("change", handler);
    return () => mq.removeEventListener("change", handler);
  }, []);

  return (
    <PlaygroundProvider>
      <div className="flex h-screen flex-col overflow-hidden bg-background text-foreground">
        <Header onToggleDrawer={toggleDrawer} />
        <main className="min-h-0 flex-1">
          <WorkspaceLayout
            configPanel={<ConfigPanel />}
            mediaPanel={<MediaPanel />}
            drawerOpen={drawerOpen}
            onCloseDrawer={closeDrawer}
          />
        </main>
      </div>
    </PlaygroundProvider>
  );
}
