/**
 * useAudioLevels Hook
 * React hook for VU meter / audio level monitoring
 * Provides real-time audio levels from the AudioMixer
 */

import { useState, useEffect, useCallback, useRef } from "react";
import type { IngestControllerV2 } from "@livepeer-frameworks/streamcrafter-core";

export interface AudioLevels {
  level: number; // Current level 0-1
  peakLevel: number; // Peak level with decay 0-1
}

export interface UseAudioLevelsOptions {
  /** IngestControllerV2 instance to monitor */
  controller: IngestControllerV2 | null;
  /** Whether to automatically start monitoring (default: true) */
  autoStart?: boolean;
}

export interface UseAudioLevelsReturn {
  /** Current audio levels */
  levels: AudioLevels;
  /** Whether level monitoring is active */
  isMonitoring: boolean;
  /** Start level monitoring */
  startMonitoring: () => void;
  /** Stop level monitoring */
  stopMonitoring: () => void;
}

/**
 * Hook for monitoring audio levels (VU meter)
 *
 * @example
 * ```tsx
 * const { controller } = useStreamCrafterV2({ whipUrl });
 * const { levels, isMonitoring } = useAudioLevels({ controller });
 *
 * return (
 *   <div className="vu-meter">
 *     <div style={{ width: `${levels.level * 100}%` }} />
 *   </div>
 * );
 * ```
 */
export function useAudioLevels(options: UseAudioLevelsOptions): UseAudioLevelsReturn {
  const { controller, autoStart = true } = options;

  const [levels, setLevels] = useState<AudioLevels>({ level: 0, peakLevel: 0 });
  const [isMonitoring, setIsMonitoring] = useState(false);
  const unsubscribeRef = useRef<(() => void) | null>(null);

  // Start monitoring
  const startMonitoring = useCallback(() => {
    if (!controller) return;

    const audioMixer = controller.getAudioMixer();
    if (!audioMixer) return;

    // Subscribe to level updates
    const unsubscribe = audioMixer.on("levelUpdate", (event) => {
      setLevels({ level: event.level, peakLevel: event.peakLevel });
    });

    unsubscribeRef.current = unsubscribe;

    // Start the audio mixer's level monitoring
    audioMixer.startLevelMonitoring();
    setIsMonitoring(true);
  }, [controller]);

  // Stop monitoring
  const stopMonitoring = useCallback(() => {
    if (!controller) return;

    const audioMixer = controller.getAudioMixer();
    if (audioMixer) {
      audioMixer.stopLevelMonitoring();
    }

    if (unsubscribeRef.current) {
      unsubscribeRef.current();
      unsubscribeRef.current = null;
    }

    setIsMonitoring(false);
    setLevels({ level: 0, peakLevel: 0 });
  }, [controller]);

  // Auto-start monitoring when controller is available and autoStart is true
  useEffect(() => {
    if (autoStart && controller) {
      startMonitoring();
    }

    return () => {
      stopMonitoring();
    };
  }, [controller, autoStart, startMonitoring, stopMonitoring]);

  return {
    levels,
    isMonitoring,
    startMonitoring,
    stopMonitoring,
  };
}
