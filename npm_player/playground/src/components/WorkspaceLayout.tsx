import type { ReactNode } from "react";

type WorkspaceLayoutProps = {
  configPanel: ReactNode;
  mediaPanel: ReactNode;
  drawerOpen: boolean;
  onCloseDrawer: () => void;
};

export function WorkspaceLayout({
  configPanel,
  mediaPanel,
  drawerOpen,
  onCloseDrawer,
}: WorkspaceLayoutProps) {
  return (
    <div className="workspace-layout h-full">
      {drawerOpen && <div className="config-drawer-overlay" onClick={onCloseDrawer} />}

      <div
        className={`config-drawer slab-stack slab-stack--scroll overflow-y-auto${drawerOpen ? " open" : ""}`}
      >
        {configPanel}
      </div>

      <div className="slab-stack overflow-y-auto">{mediaPanel}</div>
    </div>
  );
}
