import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Alert } from "@/components/ui/alert";

export type HeaderProps = {
  useDarkTheme: boolean;
  onThemeChange: (dark: boolean) => void;
  networkOptIn: boolean;
  onNetworkOptInChange: (enabled: boolean) => void;
};

export function Header({
  useDarkTheme,
  onThemeChange,
  networkOptIn,
  onNetworkOptInChange
}: HeaderProps) {
  return (
    <header className="header-bar">
      <div className="container py-10">
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div>
            <Badge className="mb-2" variant="secondary">
              FrameWorks developer tooling
            </Badge>
            <h1 className="text-3xl font-semibold tracking-tight">Player playground</h1>
            <p className="mt-2 max-w-2xl text-muted-foreground">
              Exercise the upgraded player without touching production load balancers. Start with known-safe presets, then opt in to edge overrides when you need to validate a Mist node or Gateway response.
            </p>
          </div>
          <div className="slab slab--compact inline-flex items-center px-4 py-2">
            <Label htmlFor="theme" className="mr-3">Dark theme</Label>
            <Switch id="theme" checked={useDarkTheme} onCheckedChange={onThemeChange} aria-label="Toggle dark theme" />
          </div>
        </div>
        <div className="seam my-6" />
        <Alert>
          <strong className="font-semibold text-foreground">Safety first.</strong> Networking is off by default. Enable it only when you intend to reach real infrastructure or public demo streams.
        </Alert>
        <div className="seam my-6" />
        <div className="slab slab--compact">
          <div className="flex items-center justify-between px-4 py-3 text-sm">
            <div className="flex flex-col gap-1">
              <span className="font-medium text-foreground">Networking opt-in</span>
              <span className="text-muted-foreground">
                When disabled, the player UI renders in a dormant state and no requests are issued.
              </span>
            </div>
            <Switch checked={networkOptIn} onCheckedChange={onNetworkOptInChange} id="network-toggle" aria-label="Toggle network access" />
          </div>
        </div>
      </div>
    </header>
  );
}
