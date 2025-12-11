import { useCallback, useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/alert";
import { Loader2 } from "lucide-react";

export type WhipPublisherProps = {
  endpoint?: string;
  enabled: boolean;
};

export function WhipPublisher({ endpoint, enabled }: WhipPublisherProps) {
  const [status, setStatus] = useState<"idle" | "starting" | "publishing" | "error">("idle");
  const [error, setError] = useState<string | null>(null);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const pcRef = useRef<RTCPeerConnection | null>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const resourceRef = useRef<string | null>(null);

  const stopPublishing = useCallback(async () => {
    pcRef.current?.close();
    pcRef.current = null;
    streamRef.current?.getTracks().forEach((track) => track.stop());
    streamRef.current = null;
    if (videoRef.current) {
      videoRef.current.srcObject = null;
    }
    if (resourceRef.current) {
      try {
        await fetch(resourceRef.current, { method: "DELETE" });
      } catch {
        // ignore
      } finally {
        resourceRef.current = null;
      }
    }
    setStatus("idle");
  }, []);

  const startPublishing = useCallback(async () => {
    if (!enabled) {
      setError("Enable networking before starting a WHIP session.");
      return;
    }
    if (!endpoint) {
      setError("Fill out the Mist base URL and stream name to generate a WHIP endpoint.");
      return;
    }

    setStatus("starting");
    setError(null);

    try {
      if (typeof navigator === "undefined" || !navigator.mediaDevices?.getUserMedia) {
        throw new Error("MediaDevices API unavailable in this environment.");
      }

      const stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true });
      streamRef.current = stream;

      if (videoRef.current) {
        videoRef.current.srcObject = stream;
        await videoRef.current.play().catch(() => undefined);
      }

      const pc = new RTCPeerConnection();
      stream.getTracks().forEach((track) => pc.addTrack(track, stream));
      pcRef.current = pc;

      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);

      const response = await fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/sdp" },
        body: offer.sdp ?? "",
        cache: "no-store"
      });

      if (!response.ok) {
        throw new Error(`WHIP init failed: ${response.status} ${response.statusText}`);
      }

      const answer = await response.text();
      await pc.setRemoteDescription({ type: "answer", sdp: answer });
      resourceRef.current = response.headers.get("Location");
      setStatus("publishing");
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setError(message);
      await stopPublishing();
      setStatus("error");
    }
  }, [enabled, endpoint, stopPublishing]);

  useEffect(() => {
    return () => {
      void stopPublishing();
    };
  }, [stopPublishing]);

  const isDisabled = !enabled || !endpoint;

  return (
    <div className="slab-body--flush">
      {!enabled && (
        <div className="slab-form-group">
          <Alert variant="warning">
            Toggle networking on to publish. The helper will not attempt any requests while disabled.
          </Alert>
        </div>
      )}
      {enabled && !endpoint && (
        <div className="slab-form-group">
          <Alert variant="info">
            Provide a Mist base URL and stream name to generate a WHIP endpoint automatically.
          </Alert>
        </div>
      )}
      <div className="aspect-video overflow-hidden border-b border-border/60 bg-muted/40">
        <video ref={videoRef} playsInline muted className="h-full w-full object-cover" />
      </div>
      <div className="slab-actions slab-actions--row">
        <Button variant="ghost" onClick={startPublishing} disabled={status === "starting" || status === "publishing" || isDisabled}>
          {status === "starting" ? (
            <>
              <Loader2 className="mr-2 h-4 w-4 animate-spin" /> Starting…
            </>
          ) : status === "publishing" ? (
            "Publishing"
          ) : (
            "Start capture"
          )}
        </Button>
        <Button variant="ghost" onClick={() => void stopPublishing()} disabled={status !== "publishing"}>
          Stop
        </Button>
        {resourceRef.current && (
          <Button variant="ghost" asChild>
            <a href={resourceRef.current} target="_blank" rel="noreferrer">
              Session resource
            </a>
          </Button>
        )}
      </div>
      {(endpoint || error) && (
        <div className="slab-form-group">
          {endpoint && (
            <p className="text-xs text-muted-foreground">
              WHIP endpoint:&nbsp;<span className="font-mono">{endpoint}</span>
            </p>
          )}
          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>
      )}
    </div>
  );
}
