import React from "react";
import { Player } from "./library";

export function App(): React.ReactElement {
  return (
    <div className="flex min-h-screen flex-col items-center justify-center bg-slate-950 p-6 text-slate-200">
      <header className="mb-10 max-w-2xl text-center">
        <h1 className="text-3xl font-semibold">FrameWorks Player Demo</h1>
        <p className="mt-3 text-sm text-slate-400">
          This package ships with a dedicated Vite playground. Run <code className="rounded bg-slate-800 px-2 py-1 text-xs">pnpm run playground:dev</code> to try the interactive tester.
        </p>
      </header>
      <div className="w-full max-w-4xl overflow-hidden rounded-xl border border-white/10 bg-black">
        <div className="aspect-video">
          <Player
            contentType="live"
            contentId="demo-stream"
            options={{ controls: true, stockControls: false }}
            thumbnailUrl="https://images.unsplash.com/photo-1500530855697-b586d89ba3ee?w=1200"
            endpoints={undefined}
          />
        </div>
      </div>
    </div>
  );
}

export default App;
