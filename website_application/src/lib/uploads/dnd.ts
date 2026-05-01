export interface DropZoneOptions {
  onFiles: (files: File[]) => void;
  accept?: (file: File) => boolean;
  onEnter?: () => void;
  onLeave?: () => void;
}

export function attachDropZone(node: HTMLElement, opts: DropZoneOptions): { destroy: () => void } {
  let depth = 0;

  const onEnter = (e: DragEvent) => {
    e.preventDefault();
    depth++;
    if (depth === 1) opts.onEnter?.();
  };
  const onOver = (e: DragEvent) => {
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = "copy";
  };
  const onLeave = (e: DragEvent) => {
    e.preventDefault();
    depth = Math.max(0, depth - 1);
    if (depth === 0) opts.onLeave?.();
  };
  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    depth = 0;
    opts.onLeave?.();
    const list = e.dataTransfer?.files;
    if (!list || list.length === 0) return;
    const files: File[] = [];
    for (let i = 0; i < list.length; i++) {
      const f = list[i];
      if (!opts.accept || opts.accept(f)) files.push(f);
    }
    if (files.length > 0) opts.onFiles(files);
  };

  node.addEventListener("dragenter", onEnter);
  node.addEventListener("dragover", onOver);
  node.addEventListener("dragleave", onLeave);
  node.addEventListener("drop", onDrop);

  return {
    destroy() {
      node.removeEventListener("dragenter", onEnter);
      node.removeEventListener("dragover", onOver);
      node.removeEventListener("dragleave", onLeave);
      node.removeEventListener("drop", onDrop);
    },
  };
}

export function isVideoFile(file: File): boolean {
  if (file.type.startsWith("video/")) return true;
  return /\.(mp4|mov|m4v|webm|mkv|avi|flv|wmv|mpeg|mpg|ts)$/i.test(file.name);
}
