import type { ReactNode } from "react";

type WorkspaceLayoutProps = {
  configPanel: ReactNode;
  mediaPanel: ReactNode;
};

export function WorkspaceLayout({ configPanel, mediaPanel }: WorkspaceLayoutProps) {
  return (
    <div className="workspace-layout h-full">
      <div className="slab-stack overflow-y-auto">{configPanel}</div>
      <div className="slab-stack overflow-y-auto">{mediaPanel}</div>
    </div>
  );
}
