/**
 * Devices Store
 * Svelte 5 store for device enumeration and management
 */

import { DeviceManager, type DeviceInfo } from '@livepeer-frameworks/streamcrafter-core';

export interface DevicesState {
  devices: DeviceInfo[];
  videoInputs: DeviceInfo[];
  audioInputs: DeviceInfo[];
  audioOutputs: DeviceInfo[];
  isLoading: boolean;
  error: string | null;
  hasPermission: { video: boolean; audio: boolean };
}

export interface DevicesStore {
  subscribe: (fn: (state: DevicesState) => void) => () => void;
  refresh: () => Promise<void>;
  requestPermissions: (options?: { video?: boolean; audio?: boolean }) => Promise<void>;
  destroy: () => void;
}

export function createDevicesStore(): DevicesStore {
  let state = $state<DevicesState>({
    devices: [],
    videoInputs: [],
    audioInputs: [],
    audioOutputs: [],
    isLoading: true,
    error: null,
    hasPermission: { video: false, audio: false },
  });

  const subscribers = new Set<(state: DevicesState) => void>();
  let deviceManager: DeviceManager | null = null;

  function notify() {
    const snapshot = { ...state };
    snapshot.videoInputs = state.devices.filter((d) => d.kind === 'videoinput');
    snapshot.audioInputs = state.devices.filter((d) => d.kind === 'audioinput');
    snapshot.audioOutputs = state.devices.filter((d) => d.kind === 'audiooutput');
    subscribers.forEach((fn) => fn(snapshot));
  }

  function init() {
    if (deviceManager) return;

    deviceManager = new DeviceManager();

    deviceManager.on('devicesChanged', (event) => {
      state.devices = event.devices;
      notify();
    });

    deviceManager.on('permissionChanged', (event) => {
      state.hasPermission = { video: event.granted, audio: event.granted };
      notify();
    });

    deviceManager.on('error', (event) => {
      state.error = event.message;
      notify();
    });

    // Initial enumeration
    deviceManager.enumerateDevices()
      .then((devices) => {
        state.devices = devices;
        state.isLoading = false;
        notify();
      })
      .catch((err) => {
        state.error = err.message;
        state.isLoading = false;
        notify();
      });
  }

  return {
    subscribe(fn) {
      init();
      subscribers.add(fn);
      fn({
        ...state,
        videoInputs: state.devices.filter((d) => d.kind === 'videoinput'),
        audioInputs: state.devices.filter((d) => d.kind === 'audioinput'),
        audioOutputs: state.devices.filter((d) => d.kind === 'audiooutput'),
      });
      return () => {
        subscribers.delete(fn);
      };
    },

    async refresh() {
      if (!deviceManager) return;
      state.isLoading = true;
      state.error = null;
      notify();
      try {
        const devices = await deviceManager.enumerateDevices();
        state.devices = devices;
      } catch (err) {
        state.error = err instanceof Error ? err.message : String(err);
      } finally {
        state.isLoading = false;
        notify();
      }
    },

    async requestPermissions(options = { video: true, audio: true }) {
      if (!deviceManager) return;
      state.error = null;
      notify();
      try {
        const result = await deviceManager.requestPermissions(options);
        state.hasPermission = result;
        notify();
      } catch (err) {
        state.error = err instanceof Error ? err.message : String(err);
        notify();
      }
    },

    destroy() {
      deviceManager?.destroy();
      deviceManager = null;
      subscribers.clear();
    },
  };
}
