import React, { useEffect, useState } from "react";
import { DvdLogoProps } from '../types';

const DvdLogo: React.FC<DvdLogoProps> = ({ parentRef, scale = 0.15 }) => {
  const [position, setPosition] = useState<{ top: number; left: number }>({ top: 0, left: 0 });
  const [velocity, setVelocity] = useState<{ x: number; y: number }>({ x: 1.5, y: 1.5 });
  const [dimensions, setDimensions] = useState<{ width: number; height: number }>({ width: 153, height: 69 });

  const getRandomColor = (currentColor?: string): string => {
    const colors = [
      "#7aa2f7", // Terminal Blue
      "#bb9af7", // Terminal Magenta
      "#9ece6a", // Strings/CSS classes (green)
      "#73daca", // Terminal Green (cyan-green)
      "#7dcfff", // Terminal Cyan
      "#f7768e", // Keywords/Terminal Red (pink)
      "#e0af68", // Terminal Yellow
      "#2ac3de"  // Language functions (bright cyan)
    ];
    let newColor: string;
    do {
      newColor = colors[Math.floor(Math.random() * colors.length)];
    } while (newColor === currentColor);
    return newColor;
  };

  const [color, setColor] = useState<string>(getRandomColor());

  // Calculate dimensions and initial position when parentRef becomes available
  useEffect(() => {
    if (parentRef.current) {
      const parentWidth = parentRef.current.clientWidth;
      const parentHeight = parentRef.current.clientHeight;
      
      // Scale the logo to be a fraction of the parent size
      // Original aspect ratio is 153:69 ≈ 2.22:1
      const maxWidth = parentWidth * scale;
      const maxHeight = parentHeight * scale;
      
      // Maintain aspect ratio
      const aspectRatio = 153 / 69;
      let logoWidth: number, logoHeight: number;
      
      if (maxWidth / aspectRatio <= maxHeight) {
        logoWidth = maxWidth;
        logoHeight = maxWidth / aspectRatio;
      } else {
        logoHeight = maxHeight;
        logoWidth = maxHeight * aspectRatio;
      }
      
      setDimensions({ width: logoWidth, height: logoHeight });
      
      setPosition({
        top: Math.random() * (parentHeight - logoHeight),
        left: Math.random() * (parentWidth - logoWidth),
      });
    }
  }, [parentRef, scale]);

  // Update position continuously for bouncing
  useEffect(() => {
    const updatePosition = () => {
      setPosition((prevPosition) => {
        if (!parentRef.current) return prevPosition;

        const newTop = prevPosition.top + velocity.y;
        const newLeft = prevPosition.left + velocity.x;

        let newVelocity = { ...velocity };
        let newColor = color;

        // Check for collision with the edges of the parent component
        if (newTop <= 0 || newTop >= parentRef.current.clientHeight - dimensions.height) {
          newVelocity.y = -newVelocity.y;
          newColor = getRandomColor(color);
        }
        if (newLeft <= 0 || newLeft >= parentRef.current.clientWidth - dimensions.width) {
          newVelocity.x = -newVelocity.x;
          newColor = getRandomColor(color);
        }

        setVelocity(newVelocity);
        setColor(newColor);

        return { top: newTop, left: newLeft };
      });
    };

    if (parentRef.current && dimensions.width > 0) {
      const interval = setInterval(updatePosition, 16);
      return () => clearInterval(interval);
    }
  }, [velocity, color, parentRef, dimensions]);

  return (
    <div
      className="dvd-logo"
      style={{
        position: "absolute",
        top: `${position.top}px`,
        left: `${position.left}px`,
        width: `${dimensions.width}px`,
        height: `${dimensions.height}px`,
        userSelect: "none",
        WebkitUserSelect: "none",
        MozUserSelect: "none",
        msUserSelect: "none",
        pointerEvents: "none", // Prevent any interaction
      }}
    >
      <svg width="100%" height="100%" viewBox="0 0 153 69" fill={color} style={{
        userSelect: "none",
        WebkitUserSelect: "none",
        MozUserSelect: "none",
        msUserSelect: "none",
      }}>
        <g>
          <path d="M140.186,63.52h-1.695l-0.692,5.236h-0.847l0.77-5.236h-1.693l0.076-0.694h4.158L140.186,63.52L140.186,63.52z M146.346,68.756h-0.848v-4.545l0,0l-2.389,4.545l-1-4.545l0,0l-1.462,4.545h-0.771l1.924-5.931h0.695l0.924,4.006l2.078-4.006 h0.848V68.756L146.346,68.756z M126.027,0.063H95.352c0,0-8.129,9.592-9.654,11.434c-8.064,9.715-9.523,12.32-9.779,13.02 c0.063-0.699-0.256-3.304-3.686-13.148C71.282,8.7,68.359,0.062,68.359,0.062H57.881V0L32.35,0.063H13.169l-1.97,8.131 l14.543,0.062h3.365c9.336,0,15.055,3.747,13.467,10.354c-1.717,7.24-9.91,10.416-18.545,10.416h-3.24l4.191-17.783H10.502 L4.34,37.219h20.578c15.432,0,30.168-8.13,32.709-18.608c0.508-1.906,0.443-6.67-0.764-9.527c0-0.127-0.063-0.191-0.127-0.444 c-0.064-0.063-0.127-0.509,0.127-0.571c0.128-0.062,0.383,0.189,0.445,0.254c0.127,0.317,0.19,0.57,0.19,0.57l13.083,36.965 l33.344-37.6h14.1h3.365c9.337,0,15.055,3.747,13.528,10.354c-1.778,7.24-9.972,10.416-18.608,10.416h-3.238l4.191-17.783h-14.481 l-6.159,25.976h20.576c15.434,0,30.232-8.13,32.709-18.608C152.449,8.193,141.523,0.063,126.027,0.063L126.027,0.063z M71.091,45.981c-39.123,0-70.816,4.512-70.816,10.035c0,5.59,31.693,10.034,70.816,10.034c39.121,0,70.877-4.444,70.877-10.034 C141.968,50.493,110.212,45.981,71.091,45.981L71.091,45.981z M68.55,59.573c-8.956,0-16.196-1.523-16.196-3.365 c0-1.84,7.239-3.303,16.196-3.303c8.955,0,16.195,1.463,16.195,3.303C84.745,58.050,77.505,59.573,68.55,59.573L68.55,59.573z" />
        </g>
      </svg>
    </div>
  );
};

export default DvdLogo; 