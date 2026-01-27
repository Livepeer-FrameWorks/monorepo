import { useState, useCallback } from "react";
import { Check, Copy } from "lucide-react";
import { COPY_FEEDBACK_DURATION_MS } from "@/lib/constants";

type UriRowProps = {
  label: string;
  uri: string;
};

export function UriRow({ label, uri }: UriRowProps) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(uri);
      setCopied(true);
      setTimeout(() => setCopied(false), COPY_FEEDBACK_DURATION_MS);
    } catch (err) {
      console.warn("Failed to copy:", err);
    }
  }, [uri]);

  return (
    <div className="endpoint-row flex items-center justify-between gap-3">
      <div className="min-w-0 flex-1">
        <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {label}
        </div>
        <div className="truncate font-mono text-sm text-foreground">{uri}</div>
      </div>
      <button
        type="button"
        onClick={handleCopy}
        className="flex h-8 w-8 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        title={copied ? "Copied!" : "Copy to clipboard"}
      >
        {copied ? <Check className="h-4 w-4 text-success" /> : <Copy className="h-4 w-4" />}
      </button>
    </div>
  );
}
