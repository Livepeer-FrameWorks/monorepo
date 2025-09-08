import React from 'react';

interface IconProps {
  size?: number;
  color?: string;
  className?: string;
}

export const PlayIcon: React.FC<IconProps> = ({ size = 16, color = 'currentColor', className = '' }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    className={className}
    aria-hidden="true"
  >
    <path
      d="M8 5v14l11-7z"
      fill={color}
    />
  </svg>
);

export const PauseIcon: React.FC<IconProps> = ({ size = 16, color = 'currentColor', className = '' }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    className={className}
    aria-hidden="true"
  >
    <rect x="6" y="4" width="4" height="16" fill={color} />
    <rect x="14" y="4" width="4" height="16" fill={color} />
  </svg>
);

export const SkipBackIcon: React.FC<IconProps> = ({ size = 16, color = 'currentColor', className = '' }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    className={className}
    aria-hidden="true"
  >
    <path
      d="M6 6h2v12H6V6zm3.5 6l8.5 6V6l-8.5 6z"
      fill={color}
    />
    <text x="12" y="16" fontSize="8" fill={color} textAnchor="middle">10</text>
  </svg>
);

export const SkipForwardIcon: React.FC<IconProps> = ({ size = 16, color = 'currentColor', className = '' }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    className={className}
    aria-hidden="true"
  >
    <path
      d="M16 18h2V6h-2v12zm-3.5-6L4 6v12l8.5-6z"
      fill={color}
    />
    <text x="12" y="16" fontSize="8" fill={color} textAnchor="middle">10</text>
  </svg>
);

export const VolumeUpIcon: React.FC<IconProps> = ({ size = 16, color = 'currentColor', className = '' }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    className={className}
    aria-hidden="true"
  >
    <polygon points="11,5 6,9 2,9 2,15 6,15 11,19" fill={color} />
    <path
      d="M19.07 4.93a10 10 0 0 1 0 14.14M15.54 8.46a5 5 0 0 1 0 7.07"
      stroke={color}
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  </svg>
);

export const VolumeOffIcon: React.FC<IconProps> = ({ size = 16, color = 'currentColor', className = '' }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    className={className}
    aria-hidden="true"
  >
    <polygon points="11,5 6,9 2,9 2,15 6,15 11,19" fill={color} />
    <line x1="23" y1="9" x2="17" y2="15" stroke={color} strokeWidth="2" strokeLinecap="round" />
    <line x1="17" y1="9" x2="23" y2="15" stroke={color} strokeWidth="2" strokeLinecap="round" />
  </svg>
);

export const FullscreenIcon: React.FC<IconProps> = ({ size = 16, color = 'currentColor', className = '' }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    className={className}
    aria-hidden="true"
  >
    <path
      d="M8 3H5a2 2 0 0 0-2 2v3m18 0V5a2 2 0 0 0-2-2h-3M3 16v3a2 2 0 0 0 2 2h3m8 0h3a2 2 0 0 0 2-2v-3"
      stroke={color}
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  </svg>
);

export const FullscreenExitIcon: React.FC<IconProps> = ({ size = 16, color = 'currentColor', className = '' }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    className={className}
    aria-hidden="true"
  >
    <path
      d="M8 3v3a2 2 0 0 1-2 2H3M21 8h-3a2 2 0 0 1-2-2V3M3 16h3a2 2 0 0 1 2 2v3M16 21v-3a2 2 0 0 1 2-2h3"
      stroke={color}
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  </svg>
);

export const PictureInPictureIcon: React.FC<IconProps> = ({ size = 16, color = 'currentColor', className = '' }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    className={className}
    aria-hidden="true"
  >
    <rect x="2" y="3" width="20" height="14" rx="2" ry="2" stroke={color} strokeWidth="2" fill="none" />
    <rect x="8" y="10" width="10" height="6" rx="1" ry="1" fill={color} />
  </svg>
);

export const ClosedCaptionsIcon: React.FC<IconProps> = ({ size = 16, color = 'currentColor', className = '' }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    className={className}
    aria-hidden="true"
  >
    <rect x="2" y="4" width="20" height="16" rx="2" ry="2" stroke={color} strokeWidth="2" fill="none" />
    <path
      d="M8 10c0-.6.4-1 1-1h1c.6 0 1 .4 1 1v4c0 .6-.4 1-1 1H9c-.6 0-1-.4-1-1v-4zM14 10c0-.6.4-1 1-1h1c.6 0 1 .4 1 1v4c0 .6-.4 1-1 1h-1c-.6 0-1-.4-1-1v-4z"
      fill={color}
    />
  </svg>
);

export const LiveIcon: React.FC<IconProps> = ({ size = 16, color = 'currentColor', className = '' }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    className={className}
    aria-hidden="true"
  >
    <circle cx="12" cy="12" r="3" fill={color} />
    <path
      d="M12 1v6M12 17v6M4.22 4.22l4.24 4.24M15.54 15.54l4.24 4.24M1 12h6M17 12h6M4.22 19.78l4.24-4.24M15.54 8.46l4.24-4.24"
      stroke={color}
      strokeWidth="2"
      strokeLinecap="round"
    />
  </svg>
);

export const SettingsIcon: React.FC<IconProps> = ({ size = 16, color = 'currentColor', className = '' }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    className={className}
    aria-hidden="true"
  >
    <circle cx="12" cy="12" r="3" stroke={color} strokeWidth="2" fill="none" />
    <path
      d="M12 1v6M12 17v6M4.22 4.22l4.24 4.24M15.54 15.54l4.24 4.24M1 12h6M17 12h6M4.22 19.78l4.24-4.24M15.54 8.46l4.24-4.24"
      stroke={color}
      strokeWidth="1"
      strokeLinecap="round"
    />
  </svg>
);

// Compound PlayPause icon that switches based on state
interface PlayPauseIconProps extends IconProps {
  isPlaying?: boolean;
}

export const PlayPauseIcon: React.FC<PlayPauseIconProps> = ({ isPlaying, ...props }) => {
  return isPlaying ? <PauseIcon {...props} /> : <PlayIcon {...props} />;
};

// Volume icon that switches based on mute state
interface VolumeIconProps extends IconProps {
  isMuted?: boolean;
}

export const VolumeIcon: React.FC<VolumeIconProps> = ({ isMuted, ...props }) => {
  return isMuted ? <VolumeOffIcon {...props} /> : <VolumeUpIcon {...props} />;
};

// Fullscreen icon that switches based on fullscreen state
interface FullscreenToggleIconProps extends IconProps {
  isFullscreen?: boolean;
}

export const FullscreenToggleIcon: React.FC<FullscreenToggleIconProps> = ({ isFullscreen, ...props }) => {
  return isFullscreen ? <FullscreenExitIcon {...props} /> : <FullscreenIcon {...props} />;
};