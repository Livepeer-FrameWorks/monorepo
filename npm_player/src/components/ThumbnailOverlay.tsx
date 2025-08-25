import React, { useEffect } from "react";
import { ThumbnailOverlayProps } from '../types';

const ThumbnailOverlay: React.FC<ThumbnailOverlayProps> = ({ 
  thumbnailUrl, 
  onPlay, 
  message,
  showUnmuteMessage = false,
  style = {}
}) => {
  const handleClick = () => {
    if (onPlay) {
      onPlay();
    }
  };

  // Inject CSS animation
  useEffect(() => {
    const styleId = 'thumbnail-overlay-animations';
    if (!document.getElementById(styleId)) {
      const styleElement = document.createElement('style');
      styleElement.id = styleId;
      styleElement.textContent = `
        @keyframes borderGlow {
          0%, 100% {
            opacity: 0.3;
          }
          50% {
            opacity: 0.6;
          }
        }
      `;
      document.head.appendChild(styleElement);
    }
  }, []);

  return (
    <div
      onClick={handleClick}
      style={{
        position: "relative",
        width: "100%",
        height: "100%",
        minHeight: "300px",
        cursor: "pointer",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        backgroundColor: thumbnailUrl ? "#1a1b26" : "transparent",
        backgroundImage: thumbnailUrl ? `url(${thumbnailUrl})` : "none",
        backgroundSize: "cover",
        backgroundPosition: "center",
        backgroundRepeat: "no-repeat",
        borderRadius: "1px",
        overflow: "hidden",
        userSelect: "none",
        WebkitUserSelect: "none",
        MozUserSelect: "none",
        msUserSelect: "none",
        ...style
      }}
    >
      {/* Dark overlay for better text/button visibility - only when there's a thumbnail */}
      {thumbnailUrl && (
        <div
          style={{
            position: "absolute",
            top: 0,
            left: 0,
            right: 0,
            bottom: 0,
            backgroundColor: showUnmuteMessage ? "rgba(0, 0, 0, 0.3)" : "rgba(26, 27, 38, 0.4)",
            zIndex: 1,
          }}
        />
      )}

      {/* Play button or unmute message */}
      <div
        style={{
          position: "relative",
          zIndex: 2,
          display: "flex",
          flexDirection: "column",
          alignItems: "center",
          justifyContent: "center",
          textAlign: "center",
          color: "#a9b1d6",
          fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif",
          height: "100%",
          padding: "0",
        }}
      >
        {showUnmuteMessage ? (
          // Unmute message for autoplay scenario - smaller and at bottom
          <div
            style={{
              padding: "12px 20px",
              backgroundColor: "rgba(36, 40, 59, 0.95)",
              borderRadius: "6px",
              border: "2px solid rgba(122, 162, 247, 0.4)",
              backdropFilter: "blur(10px)",
              WebkitBackdropFilter: "blur(10px)",
              transition: "all 0.3s ease",
              maxWidth: "250px",
            }}
          >
            <div
              style={{
                fontSize: "16px",
                fontWeight: "600",
                marginBottom: "4px",
                color: "#7aa2f7",
              }}
            >
              🔇 Click to unmute
            </div>
            <div
              style={{
                fontSize: "12px",
                opacity: 0.8,
              }}
            >
              Stream is playing with sound muted
            </div>
          </div>
        ) : (
          // Play button for click-to-play scenario
          <>
            {/* Large play button */}
            <div
              style={{
                width: "80px",
                height: "80px",
                borderRadius: "50%",
                backgroundColor: "rgba(122, 162, 247, 0.9)",
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                marginBottom: "16px",
                transition: "all 0.3s ease",
                boxShadow: "0 8px 32px rgba(122, 162, 247, 0.3)",
              }}
            >
              <svg
                width="32"
                height="32"
                viewBox="0 0 24 24"
                fill="white"
                style={{ marginLeft: "4px" }} // Slight offset to center the triangle
              >
                <path d="M8 5v14l11-7z" />
              </svg>
            </div>

            {/* Stream name and play message */}
            <div
              style={{
                padding: "16px 24px",
                backgroundColor: "rgba(36, 40, 59, 0.9)",
                borderRadius: "8px",
                border: "2px solid rgba(122, 162, 247, 0.3)",
                backdropFilter: "blur(10px)",
                WebkitBackdropFilter: "blur(10px)",
                maxWidth: "300px",
              }}
            >
              <div
                style={{
                  fontSize: "18px",
                  fontWeight: "600",
                  marginBottom: "8px",
                  color: "#7aa2f7",
                }}
              >
                {message || "Click to play"}
              </div>
              <div
                style={{
                  fontSize: "14px",
                  opacity: 0.8,
                }}
              >
                {message ? "Click to start streaming" : "Start watching"}
              </div>
            </div>
          </>
        )}
      </div>

      {/* Subtle animated border effect - only for click-to-play with thumbnail */}
      {thumbnailUrl && !showUnmuteMessage && (
        <div
          style={{
            position: "absolute",
            top: 0,
            left: 0,
            right: 0,
            bottom: 0,
            border: "2px solid transparent",
            borderRadius: "1px",
            background: "linear-gradient(45deg, rgba(122, 162, 247, 0.2), rgba(187, 154, 247, 0.2), rgba(122, 162, 247, 0.2))",
            backgroundSize: "200% 200%",
            animation: "borderGlow 3s ease-in-out infinite",
            mask: "linear-gradient(#fff 0 0) content-box, linear-gradient(#fff 0 0)",
            maskComposite: "xor",
            WebkitMask: "linear-gradient(#fff 0 0) content-box, linear-gradient(#fff 0 0)",
            WebkitMaskComposite: "xor",
            pointerEvents: "none",
          }}
        />
      )}
    </div>
  );
};

export default ThumbnailOverlay; 