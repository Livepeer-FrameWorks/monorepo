/**
 * useDevices Hook
 * React hook for device enumeration and management
 */

import { useState, useEffect, useCallback, useRef } from "react";
import { DeviceManager, type DeviceInfo } from "@livepeer-frameworks/streamcrafter-core";

export interface UseDevicesReturn {
  devices: DeviceInfo[];
  videoInputs: DeviceInfo[];
  audioInputs: DeviceInfo[];
  audioOutputs: DeviceInfo[];
  isLoading: boolean;
  error: string | null;
  hasPermission: { video: boolean; audio: boolean };
  refresh: () => Promise<void>;
  requestPermissions: (options?: { video?: boolean; audio?: boolean }) => Promise<void>;
}

export function useDevices(): UseDevicesReturn {
  const [devices, setDevices] = useState<DeviceInfo[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [hasPermission, setHasPermission] = useState({ video: false, audio: false });

  const deviceManagerRef = useRef<DeviceManager | null>(null);

  // Initialize device manager
  useEffect(() => {
    const manager = new DeviceManager();
    deviceManagerRef.current = manager;

    // Set up event listeners
    const unsubDevices = manager.on("devicesChanged", (event) => {
      setDevices(event.devices);
    });

    const unsubPermission = manager.on("permissionChanged", (event) => {
      setHasPermission({
        video: event.granted,
        audio: event.granted,
      });
    });

    const unsubError = manager.on("error", (event) => {
      setError(event.message);
    });

    // Initial enumeration
    manager
      .enumerateDevices()
      .then((deviceList) => {
        setDevices(deviceList);
        setIsLoading(false);
      })
      .catch((err) => {
        setError(err.message);
        setIsLoading(false);
      });

    return () => {
      unsubDevices();
      unsubPermission();
      unsubError();
      manager.destroy();
    };
  }, []);

  const refresh = useCallback(async () => {
    if (!deviceManagerRef.current) return;
    setIsLoading(true);
    setError(null);
    try {
      const deviceList = await deviceManagerRef.current.enumerateDevices();
      setDevices(deviceList);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setIsLoading(false);
    }
  }, []);

  const requestPermissions = useCallback(
    async (options: { video?: boolean; audio?: boolean } = { video: true, audio: true }) => {
      if (!deviceManagerRef.current) return;
      setError(null);
      try {
        const result = await deviceManagerRef.current.requestPermissions(options);
        setHasPermission(result);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    },
    []
  );

  return {
    devices,
    videoInputs: devices.filter((d) => d.kind === "videoinput"),
    audioInputs: devices.filter((d) => d.kind === "audioinput"),
    audioOutputs: devices.filter((d) => d.kind === "audiooutput"),
    isLoading,
    error,
    hasPermission,
    refresh,
    requestPermissions,
  };
}
