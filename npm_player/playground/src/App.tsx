import { PlaygroundProvider } from "@/context/PlaygroundContext";
import { Header } from "@/components/Header";
import { WorkspaceLayout } from "@/components/WorkspaceLayout";
import { ConnectionSlab } from "@/components/ConnectionSlab";
import { IngestUrisSlab } from "@/components/IngestUrisSlab";
import { PlaybackSourcesSlab } from "@/components/PlaybackSourcesSlab";
import { WhipPublisher } from "@/components/WhipPublisher";
import { PlayerPreview } from "@/components/PlayerPreview";

function ConfigPanel() {
  return (
    <>
      <ConnectionSlab />
      <div className="slab-section-label slab-section-label--studio">Ingest</div>
      <IngestUrisSlab />
      <div className="slab-section-label slab-section-label--player">Playback</div>
      <PlaybackSourcesSlab />
    </>
  );
}

function MediaPanel() {
  return (
    <>
      <WhipPublisher />
      <PlayerPreview />
    </>
  );
}

export default function App() {
  return (
    <PlaygroundProvider>
      <div className="flex min-h-screen flex-col bg-background text-foreground">
        <Header />
        <main className="flex-1">
          <WorkspaceLayout configPanel={<ConfigPanel />} mediaPanel={<MediaPanel />} />
        </main>
      </div>
    </PlaygroundProvider>
  );
}
