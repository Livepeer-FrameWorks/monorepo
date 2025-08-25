import React from 'react';

interface LogoOverlayProps {
  src: string;
  show?: boolean;
  position?: 'top-left' | 'top-right' | 'bottom-left' | 'bottom-right';
  width?: number;
  height?: number | 'auto';
  clickUrl?: string;
}

const LogoOverlay: React.FC<LogoOverlayProps> = ({
  src,
  show = true,
  position = 'bottom-right',
  width = 96,
  height = 'auto',
  clickUrl
}) => {
  if (!show) return null;
  const style: React.CSSProperties = {
    position: 'absolute',
    top: position.startsWith('top') ? 8 : undefined,
    bottom: position.startsWith('bottom') ? 8 : 8,
    left: position.endsWith('left') ? 8 : undefined,
    right: position.endsWith('right') ? 8 : 8,
    width,
    height,
    opacity: 0.9,
    cursor: clickUrl ? 'pointer' : 'default'
  };
  const img = <img src={src} alt="FrameWorks" style={style} onClick={() => clickUrl && window.open(clickUrl, '_blank')} />;
  return img;
};

export default LogoOverlay;
