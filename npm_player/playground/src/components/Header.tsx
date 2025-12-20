export function Header() {
  return (
    <header className="header-bar">
      <div className="flex items-center gap-3 px-6 py-4">
        <img src="/favicon.svg" alt="FrameWorks" className="h-8 w-8" />
        <h1 className="text-xl font-semibold">SDK Playground</h1>
        <span className="text-xs text-muted-foreground bg-muted px-2 py-0.5 rounded">
          Player + StreamCrafter
        </span>
      </div>
    </header>
  );
}
