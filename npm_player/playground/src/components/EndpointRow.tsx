import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Check, Clipboard, Loader2 } from "lucide-react";
import type { MistEndpointDefinition, EndpointStatus } from "@/lib/types";

export type EndpointRowProps = {
  definition: MistEndpointDefinition;
  value: string;
  resolvedValue?: string;
  isCustom: boolean;
  onChange: (value: string) => void;
  onReset: () => void;
  onCopy: () => void;
  onCheck?: () => void;
  status?: EndpointStatus;
  showCheck: boolean;
  checking: boolean;
  copied: boolean;
  disabled: boolean;
  networkOptIn: boolean;
};

export function EndpointRow({
  definition,
  value,
  resolvedValue,
  isCustom,
  onChange,
  onReset,
  onCopy,
  onCheck,
  status,
  showCheck,
  checking,
  copied,
  disabled,
  networkOptIn
}: EndpointRowProps) {
  const statusPill = (() => {
    if (status === "checking") {
      return (
        <span className="inline-flex items-center gap-1 bg-muted px-2 py-0.5 text-xs text-muted-foreground">
          <Loader2 className="h-3 w-3 animate-spin" />
          Checking
        </span>
      );
    }
    if (status === "ok") {
      return <span className="inline-flex items-center bg-green-500/10 px-2 py-0.5 text-xs font-medium text-green-600 dark:text-green-300">Reachable</span>;
    }
    if (status === "error") {
      return <span className="inline-flex items-center bg-red-500/10 px-2 py-0.5 text-xs font-medium text-red-600 dark:text-red-300">Error</span>;
    }
    return null;
  })();

  return (
    <div className="endpoint-row">
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-sm font-medium text-foreground">{definition.label}</p>
          {definition.hint && <p className="text-xs text-muted-foreground">{definition.hint}</p>}
          {isCustom && <span className="mt-1 inline-flex bg-amber-100 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-700 dark:bg-amber-900/40 dark:text-amber-200">Custom</span>}
        </div>
        {statusPill}
      </div>
      <div className="mt-2 flex flex-col gap-2 sm:flex-row sm:items-center">
        <Input
          value={value}
          onChange={(event) => onChange(event.target.value)}
          className="flex-1 font-mono text-xs"
          spellCheck={false}
          autoCorrect="off"
        />
        <div className="flex">
          <Button type="button" variant="ghost" size="icon" onClick={onCopy} disabled={disabled}>
            {copied ? <Check className="h-4 w-4" /> : <Clipboard className="h-4 w-4" />}
          </Button>
          {showCheck && onCheck && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={onCheck}
              disabled={disabled || checking || !networkOptIn}
            >
              {checking ? (
                <>
                  <Loader2 className="mr-1 h-4 w-4 animate-spin" />
                  Checking…
                </>
              ) : (
                "Check"
              )}
            </Button>
          )}
          {isCustom && (
            <Button type="button" variant="ghost" size="sm" onClick={onReset}>
              Reset
            </Button>
          )}
        </div>
      </div>
      {resolvedValue && resolvedValue !== value && (
        <p className="mt-2 text-xs text-muted-foreground">
          Resolved:&nbsp;
          <span className="font-mono">{resolvedValue}</span>
        </p>
      )}
    </div>
  );
}
