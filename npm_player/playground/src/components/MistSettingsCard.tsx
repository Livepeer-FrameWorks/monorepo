import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import type { MistSettings } from "@/lib/types";

export type MistSettingsCardProps = {
  settings: MistSettings;
  onSettingChange: (key: keyof MistSettings, value: string) => void;
};

export function MistSettingsCard({ settings, onSettingChange }: MistSettingsCardProps) {
  return (
    <div className="slab">
      <div className="slab-header">
        <h3 className="slab-title">Mist Node Configuration</h3>
        <p className="slab-description">Define the base Mist URL once and let the playground derive every ingest and playback endpoint.</p>
      </div>
      <div className="slab-body--flush">
        <div className="slab-form-group">
          <Label htmlFor="mist-label">Workspace label</Label>
          <Input
            id="mist-label"
            className="mt-2"
            placeholder="Local Mist"
            value={settings.label}
            onChange={(event) => onSettingChange("label", event.target.value)}
          />
        </div>
        <div className="slab-form-group">
          <Label htmlFor="mist-base-url">Mist base URL</Label>
          <Input
            id="mist-base-url"
            className="mt-2"
            placeholder="https://mist.dev.local"
            value={settings.baseUrl}
            onChange={(event) => onSettingChange("baseUrl", event.target.value)}
          />
          <p className="mt-1 text-xs text-muted-foreground">Include scheme + host (and port if needed). The derived endpoints reuse this origin.</p>
        </div>
        <div className="slab-form-group grid gap-4 md:grid-cols-2">
          <div>
            <Label htmlFor="mist-viewer-path">Viewer path (optional)</Label>
            <Input
              id="mist-viewer-path"
              className="mt-2"
              placeholder="/stream"
              value={settings.viewerPath}
              onChange={(event) => onSettingChange("viewerPath", event.target.value)}
            />
          </div>
          <div>
            <Label htmlFor="mist-stream-name">Stream name</Label>
            <Input
              id="mist-stream-name"
              className="mt-2"
              placeholder="demo-stream"
              value={settings.streamName}
              onChange={(event) => onSettingChange("streamName", event.target.value)}
            />
          </div>
        </div>
        <Accordion type="single" collapsible className="border-t border-border/30">
          <AccordionItem value="mist-advanced" className="border-none">
            <AccordionTrigger className="slab-form-group hover:bg-accent/5">Advanced fields</AccordionTrigger>
            <AccordionContent>
              <div className="slab-form-group">
                <Label htmlFor="mist-auth-token">Auth token (optional)</Label>
                <Input
                  id="mist-auth-token"
                  className="mt-2"
                  placeholder="Playback token appended as ?token="
                  value={settings.authToken ?? ""}
                  onChange={(event) => onSettingChange("authToken", event.target.value)}
                />
                <p className="mt-1 text-xs text-muted-foreground">Token is appended to every derived URL so you can test gated nodes safely.</p>
              </div>
              <div className="slab-form-group">
                <Label htmlFor="mist-ingest-app">RTMP / SRT application</Label>
                <Input
                  id="mist-ingest-app"
                  className="mt-2"
                  placeholder="live"
                  value={settings.ingestApp ?? ""}
                  onChange={(event) => onSettingChange("ingestApp", event.target.value)}
                />
                <p className="mt-1 text-xs text-muted-foreground">Used when building RTMP and SRT ingest strings.</p>
              </div>
            </AccordionContent>
          </AccordionItem>
        </Accordion>
      </div>
    </div>
  );
}
