import React, { useRef, useEffect, useState } from "react";
import { WHEPPlayerProps } from '../types';

const WHEPPlayer: React.FC<WHEPPlayerProps> = ({
  whepUrl,
  autoPlay = true,
  muted = true,
  onError,
  onConnected,
  onDisconnected
}) => {
  const videoRef = useRef<HTMLVideoElement>(null);
  const pcRef = useRef<RTCPeerConnection | null>(null);
  const [isLoading, setIsLoading] = useState<boolean>(true);

  const handleError = (err: Error) => {
    console.error("WHEP Player error:", err);
    setIsLoading(false);
    if (onError) onError(err);
  };

  const cleanup = () => {
    if (pcRef.current) {
      pcRef.current.close();
      pcRef.current = null;
    }
    setIsLoading(false);
  };

  // Update video muted state when prop changes
  useEffect(() => {
    if (videoRef.current) {
      videoRef.current.muted = muted;
    }
  }, [muted]);

  const startWHEPPlayback = async (url: string) => {
    try {
      setIsLoading(true);

      // Create RTCPeerConnection
      const pc = new RTCPeerConnection({
        iceServers: [
          { urls: "stun:stun.l.google.com:19302" }
        ]
      });
      pcRef.current = pc;

      // Handle incoming streams
      pc.ontrack = (event: RTCTrackEvent) => {
        console.log("Received track:", event.track.kind);
        if (videoRef.current && event.streams[0]) {
          videoRef.current.srcObject = event.streams[0];
          setIsLoading(false);
          if (onConnected) onConnected();
        }
      };

      pc.oniceconnectionstatechange = () => {
        console.log("ICE connection state:", pc.iceConnectionState);
        if (pc.iceConnectionState === "failed" || pc.iceConnectionState === "disconnected") {
          handleError(new Error("Connection lost"));
          if (onDisconnected) onDisconnected();
        }
      };

      // Create offer for WHEP (receive only)
      pc.addTransceiver("video", { direction: "recvonly" });
      pc.addTransceiver("audio", { direction: "recvonly" });

      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);

      // Send offer to WHEP endpoint
      const response = await fetch(url, {
        method: "POST",
        headers: {
          "Content-Type": "application/sdp",
        },
        body: offer.sdp,
      });

      if (!response.ok) {
        throw new Error(`WHEP request failed: ${response.status} ${response.statusText}`);
      }

      const answerSdp = await response.text();
      const answer = new RTCSessionDescription({
        type: "answer",
        sdp: answerSdp,
      });

      await pc.setRemoteDescription(answer);

      // Store session URL for cleanup
      const sessionUrl = response.headers.get("Location");
      if (sessionUrl) {
        (pc as any)._whepSessionUrl = sessionUrl;
      }

    } catch (err) {
      handleError(err as Error);
    }
  };

  useEffect(() => {
    if (!whepUrl) {
      console.error("No WHEP URL provided");
      return;
    }

    startWHEPPlayback(whepUrl);

    return () => {
      // Cleanup session
      if (pcRef.current && (pcRef.current as any)._whepSessionUrl) {
        fetch((pcRef.current as any)._whepSessionUrl, { method: "DELETE" }).catch(console.warn);
      }
      cleanup();
    };
  }, [whepUrl]);

  return (
    <div style={{ position: "relative", width: "100%", height: "100%" }}>
      <video
        ref={videoRef}
        autoPlay={autoPlay}
        muted={muted}
        playsInline
        controls
        style={{
          width: "100%",
          height: "100%",
          backgroundColor: "#000",
          borderRadius: "1px",
          opacity: isLoading ? 0 : 1,
          transition: "opacity 0.3s ease"
        }}
      />
    </div>
  );
};

export default WHEPPlayer; 