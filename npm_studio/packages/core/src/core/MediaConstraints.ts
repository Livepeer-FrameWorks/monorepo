/**
 * Media Quality Profiles
 * Configurable audio/video processing controls for different use cases
 * Ported from StreamCrafter MediaQualityProfiles.jsx
 */

import type {
  QualityProfile,
  QualityProfileInfo,
  AudioConstraintProfile,
  VideoConstraintProfile,
} from '../types';

/**
 * Get audio constraints for a quality profile
 */
export function getAudioConstraints(profile: QualityProfile = 'professional'): AudioConstraintProfile {
  const profiles: Record<QualityProfile, AudioConstraintProfile> = {
    professional: {
      // Raw audio for professional streaming/recording
      echoCancellation: false,
      noiseSuppression: false,
      autoGainControl: false,
      sampleRate: 48000,
      channelCount: 2,
      latency: 0.01,
    },

    broadcast: {
      // Minimal processing for live streaming
      echoCancellation: false,
      noiseSuppression: true, // Light noise suppression only
      autoGainControl: false,
      sampleRate: 48000,
      channelCount: 2,
      latency: 0.02,
    },

    conference: {
      // Optimized for video calls/meetings
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true,
      sampleRate: 44100,
      channelCount: 1, // Mono for bandwidth
      latency: 0.05,
    },

    auto: {
      // Let browser decide (default browser behavior)
      echoCancellation: false,
      noiseSuppression: false,
      autoGainControl: false,
      sampleRate: 48000,
      channelCount: 2,
    },
  };

  return profiles[profile] || profiles.professional;
}

/**
 * Get video constraints for a quality profile
 */
export function getVideoConstraints(profile: QualityProfile = 'professional'): VideoConstraintProfile {
  const profiles: Record<QualityProfile, VideoConstraintProfile> = {
    professional: {
      // Raw video for professional streaming
      width: { ideal: 1920 },
      height: { ideal: 1080 },
      frameRate: { ideal: 30 },
    },

    broadcast: {
      // Optimized for streaming
      width: { ideal: 1920 },
      height: { ideal: 1080 },
      frameRate: { ideal: 30 },
    },

    conference: {
      // Optimized for video calls
      width: { ideal: 1280 },
      height: { ideal: 720 },
      frameRate: { ideal: 24 },
    },

    auto: {
      // Standard constraints, let browser optimize
      width: { ideal: 1920 },
      height: { ideal: 1080 },
      frameRate: { ideal: 30 },
    },
  };

  return profiles[profile] || profiles.professional;
}

/**
 * Get available quality profiles with descriptions
 */
export function getAvailableProfiles(): QualityProfileInfo[] {
  return [
    {
      id: 'professional',
      name: 'Professional',
      description: 'Raw quality for content creators - disables all browser processing',
    },
    {
      id: 'broadcast',
      name: 'Broadcast',
      description: 'Minimal processing optimized for live streaming',
    },
    {
      id: 'conference',
      name: 'Conference',
      description: 'Full processing for video calls and meetings',
    },
    {
      id: 'auto',
      name: 'Auto',
      description: 'Let browser decide optimal settings',
    },
  ];
}

/**
 * Build full MediaStreamConstraints from a quality profile
 */
export function buildMediaConstraints(
  profile: QualityProfile,
  options?: {
    videoDeviceId?: string;
    audioDeviceId?: string;
    facingMode?: 'user' | 'environment';
  }
): MediaStreamConstraints {
  const audioProfile = getAudioConstraints(profile);
  const videoProfile = getVideoConstraints(profile);

  const constraints: MediaStreamConstraints = {
    audio: {
      echoCancellation: audioProfile.echoCancellation,
      noiseSuppression: audioProfile.noiseSuppression,
      autoGainControl: audioProfile.autoGainControl,
      sampleRate: audioProfile.sampleRate,
      channelCount: audioProfile.channelCount,
      ...(options?.audioDeviceId && { deviceId: { exact: options.audioDeviceId } }),
    },
    video: {
      width: videoProfile.width,
      height: videoProfile.height,
      frameRate: videoProfile.frameRate,
      ...(options?.videoDeviceId && { deviceId: { exact: options.videoDeviceId } }),
      ...(options?.facingMode && { facingMode: options.facingMode }),
    },
  };

  return constraints;
}

/**
 * Merge quality profile with custom constraints
 */
export function mergeWithCustomConstraints(
  profile: QualityProfile,
  customConstraints: Partial<MediaStreamConstraints> = {}
): MediaStreamConstraints {
  const baseConstraints = buildMediaConstraints(profile);

  return {
    audio:
      typeof baseConstraints.audio === 'object' && typeof customConstraints.audio === 'object'
        ? { ...baseConstraints.audio, ...customConstraints.audio }
        : customConstraints.audio ?? baseConstraints.audio,
    video:
      typeof baseConstraints.video === 'object' && typeof customConstraints.video === 'object'
        ? { ...baseConstraints.video, ...customConstraints.video }
        : customConstraints.video ?? baseConstraints.video,
  };
}

/**
 * Get encoder settings for a quality profile
 */
export function getEncoderSettings(profile: QualityProfile = 'professional') {
  const settings = {
    professional: {
      video: {
        codec: 'avc1.42E01E', // H.264 Baseline
        width: 1920,
        height: 1080,
        bitrate: 4_000_000, // 4 Mbps
        framerate: 30,
      },
      audio: {
        codec: 'opus',
        sampleRate: 48000,
        numberOfChannels: 2,
        bitrate: 128_000, // 128 kbps
      },
    },

    broadcast: {
      video: {
        codec: 'avc1.42E01E',
        width: 1920,
        height: 1080,
        bitrate: 2_500_000, // 2.5 Mbps
        framerate: 30,
      },
      audio: {
        codec: 'opus',
        sampleRate: 48000,
        numberOfChannels: 2,
        bitrate: 128_000,
      },
    },

    conference: {
      video: {
        codec: 'avc1.42E01E',
        width: 1280,
        height: 720,
        bitrate: 1_500_000, // 1.5 Mbps
        framerate: 24,
      },
      audio: {
        codec: 'opus',
        sampleRate: 48000,
        numberOfChannels: 2,
        bitrate: 128_000, // 128 kbps - same as other profiles
      },
    },

    auto: {
      video: {
        codec: 'avc1.42E01E',
        width: 1920,
        height: 1080,
        bitrate: 2_000_000, // 2 Mbps
        framerate: 30,
      },
      audio: {
        codec: 'opus',
        sampleRate: 48000,
        numberOfChannels: 2,
        bitrate: 128_000,
      },
    },
  };

  return settings[profile] || settings.professional;
}
