import { useEffect, useRef, useState } from "react";

interface CopyButtonProps {
  text: string;
  label?: string;
  className?: string;
}

export function CopyButton({ text, label = "Copy", className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    []
  );

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };

  return (
    <button
      type="button"
      className={className ?? "fwt-button fwt-button--ghost"}
      onClick={copy}
      aria-live="polite"
    >
      {copied ? "Copied" : label}
    </button>
  );
}

export function CopyRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="fwt-copy-row">
      <div className="fwt-copy-row__text">
        <span className="fwt-label">{label}</span>
        <code className="fwt-mono">{value}</code>
      </div>
      <CopyButton text={value} />
    </div>
  );
}
