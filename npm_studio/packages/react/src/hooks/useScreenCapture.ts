/**
 * useScreenCapture Hook
 * React hook for screen capture
 */

import { useState, useEffect, useCallback, useRef } from 'react';
import { ScreenCapture, type ScreenCaptureOptions } from '@livepeer-frameworks/streamcrafter-core';

export interface UseScreenCaptureReturn {
  stream: MediaStream | null;
  isActive: boolean;
  hasAudio: boolean;
  error: string | null;
  start: (options?: ScreenCaptureOptions) => Promise<MediaStream | null>;
  stop: () => void;
}

export function useScreenCapture(): UseScreenCaptureReturn {
  const [stream, setStream] = useState<MediaStream | null>(null);
  const [isActive, setIsActive] = useState(false);
  const [hasAudio, setHasAudio] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const screenCaptureRef = useRef<ScreenCapture | null>(null);

  // Initialize screen capture
  useEffect(() => {
    const capture = new ScreenCapture();
    screenCaptureRef.current = capture;

    const unsubStarted = capture.on('started', (event) => {
      setStream(event.stream);
      setIsActive(true);
      setHasAudio(event.stream.getAudioTracks().length > 0);
    });

    const unsubEnded = capture.on('ended', () => {
      setStream(null);
      setIsActive(false);
      setHasAudio(false);
    });

    const unsubError = capture.on('error', (event) => {
      setError(event.message);
    });

    return () => {
      unsubStarted();
      unsubEnded();
      unsubError();
      capture.destroy();
    };
  }, []);

  const start = useCallback(async (options?: ScreenCaptureOptions) => {
    if (!screenCaptureRef.current) return null;
    setError(null);
    try {
      return await screenCaptureRef.current.start(options);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      return null;
    }
  }, []);

  const stop = useCallback(() => {
    screenCaptureRef.current?.stop();
  }, []);

  return {
    stream,
    isActive,
    hasAudio,
    error,
    start,
    stop,
  };
}
