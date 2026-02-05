export function Header() {
  return (
    <header className="header-bar">
      <div className="flex items-center gap-3 px-6 py-4">
        <img src="/favicon.svg" alt="FrameWorks" className="h-8 w-8" />
        <h1 className="text-xl font-semibold">SDK Playground</h1>
        <span
          className="text-xs px-2 py-0.5 rounded"
          style={{ background: "hsl(var(--tn-orange) / 0.15)", color: "hsl(var(--tn-orange))" }}
        >
          StreamCrafter
        </span>
        <span
          className="text-xs px-2 py-0.5 rounded"
          style={{ background: "hsl(var(--tn-teal) / 0.15)", color: "hsl(var(--tn-teal))" }}
        >
          Player
        </span>
      </div>
    </header>
  );
}
