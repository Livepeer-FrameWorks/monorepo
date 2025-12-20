import React, { useRef, useEffect, useState } from "react";
import DvdLogo from "./DvdLogo";
import logomarkAsset from "../assets/logomark.svg";
import type { StreamStatus } from "../types";

// ============================================================================
// AnimatedBubble Component
// ============================================================================

interface AnimatedBubbleProps {
  index: number;
}

const AnimatedBubble: React.FC<AnimatedBubbleProps> = ({ index }) => {
  const [position, setPosition] = useState({ top: 0, left: 0 });
  const [size, setSize] = useState(40);
  const [opacity, setOpacity] = useState(0);

  const getRandomPosition = () => ({
    top: Math.random() * 80 + 10,
    left: Math.random() * 80 + 10,
  });

  const getRandomSize = () => Math.random() * 60 + 30;

  useEffect(() => {
    setPosition(getRandomPosition());
    setSize(getRandomSize());

    const animationCycle = () => {
      setOpacity(0.15);
      setTimeout(() => {
        setOpacity(0);
        setTimeout(() => {
          setPosition(getRandomPosition());
          setSize(getRandomSize());
          setTimeout(() => {
            animationCycle();
          }, 200);
        }, 1500);
      }, 4000 + Math.random() * 3000);
    };

    const timeout = setTimeout(animationCycle, index * 500);
    return () => clearTimeout(timeout);
  }, [index]);

  const bubbleColors = [
    "rgba(122, 162, 247, 0.2)",
    "rgba(187, 154, 247, 0.2)",
    "rgba(158, 206, 106, 0.2)",
    "rgba(115, 218, 202, 0.2)",
    "rgba(125, 207, 255, 0.2)",
    "rgba(247, 118, 142, 0.2)",
    "rgba(224, 175, 104, 0.2)",
    "rgba(42, 195, 222, 0.2)",
  ];

  return (
    <div
      style={{
        position: "absolute",
        top: `${position.top}%`,
        left: `${position.left}%`,
        width: `${size}px`,
        height: `${size}px`,
        borderRadius: "50%",
        background: bubbleColors[index % bubbleColors.length],
        opacity,
        transition: "opacity 1s ease-in-out",
        pointerEvents: "none",
        userSelect: "none",
      } as React.CSSProperties}
    />
  );
};

// ============================================================================
// CenterLogo Component
// ============================================================================

interface CenterLogoProps {
  containerRef: React.RefObject<HTMLDivElement>;
  scale?: number;
  onHitmarker?: (e: { clientX: number; clientY: number }) => void;
}

const CenterLogo: React.FC<CenterLogoProps> = ({ containerRef, scale = 0.2, onHitmarker }) => {
  const [logoSize, setLogoSize] = useState(100);
  const [offset, setOffset] = useState({ x: 0, y: 0 });
  const [isHovered, setIsHovered] = useState(false);

  useEffect(() => {
    if (containerRef.current) {
      const containerWidth = containerRef.current.clientWidth;
      const containerHeight = containerRef.current.clientHeight;
      const minDimension = Math.min(containerWidth, containerHeight);
      setLogoSize(minDimension * scale);
    }
  }, [containerRef, scale]);

  const handleLogoClick = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (onHitmarker) {
      onHitmarker({ clientX: e.clientX, clientY: e.clientY });
    }
  };

  const handleMouseMove = (e: MouseEvent) => {
    if (!containerRef.current) return;

    const rect = containerRef.current.getBoundingClientRect();
    const centerX = rect.left + rect.width / 2;
    const centerY = rect.top + rect.height / 2;
    const deltaX = e.clientX - centerX;
    const deltaY = e.clientY - centerY;
    const distance = Math.sqrt(deltaX * deltaX + deltaY * deltaY);

    const maxDistance = logoSize * 1.5;
    if (distance < maxDistance && distance > 0) {
      const pushStrength = (maxDistance - distance) / maxDistance;
      const pushDistance = 50 * pushStrength;
      const pushX = -(deltaX / distance) * pushDistance;
      const pushY = -(deltaY / distance) * pushDistance;
      setOffset({ x: pushX, y: pushY });
      setIsHovered(true);
    } else {
      setOffset({ x: 0, y: 0 });
      setIsHovered(false);
    }
  };

  const handleMouseLeave = () => {
    setOffset({ x: 0, y: 0 });
    setIsHovered(false);
  };

  useEffect(() => {
    if (containerRef.current) {
      const container = containerRef.current;
      container.addEventListener('mousemove', handleMouseMove);
      container.addEventListener('mouseleave', handleMouseLeave);
      return () => {
        container.removeEventListener('mousemove', handleMouseMove);
        container.removeEventListener('mouseleave', handleMouseLeave);
      };
    }
  }, [logoSize, containerRef]);

  return (
    <div
      style={{
        position: "absolute",
        top: "50%",
        left: "50%",
        transform: `translate(-50%, -50%) translate(${offset.x}px, ${offset.y}px)`,
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        zIndex: 10,
        transition: "transform 0.3s ease-out",
        userSelect: "none",
      }}
    >
      <div
        style={{
          position: "absolute",
          width: `${logoSize * 1.4}px`,
          height: `${logoSize * 1.4}px`,
          borderRadius: "50%",
          background: "rgba(122, 162, 247, 0.15)",
          animation: isHovered ? "logoPulse 1s ease-in-out infinite" : "logoPulse 3s ease-in-out infinite",
          transform: isHovered ? "scale(1.2)" : "scale(1)",
          transition: "transform 0.3s ease-out",
          pointerEvents: "none",
        }}
      />
      <img
        src={logomarkAsset}
        alt="FrameWorks Logo"
        onClick={handleLogoClick}
        style={{
          width: `${logoSize}px`,
          height: `${logoSize}px`,
          position: "relative",
          zIndex: 1,
          filter: isHovered
            ? "drop-shadow(0 6px 12px rgba(36, 40, 59, 0.4)) brightness(1.1)"
            : "drop-shadow(0 4px 8px rgba(36, 40, 59, 0.3))",
          transform: isHovered ? "scale(1.1)" : "scale(1)",
          transition: "all 0.3s ease-out",
          cursor: isHovered ? "pointer" : "default",
          userSelect: "none",
          WebkitUserDrag: "none",
        } as React.CSSProperties}
      />
    </div>
  );
};

// ============================================================================
// Status Overlay Component (shows on top of the fancy background)
// ============================================================================

interface StatusOverlayProps {
  status?: StreamStatus;
  message: string;
  percentage?: number;
  error?: string;
  onRetry?: () => void;
}

function getStatusLabel(status?: StreamStatus): string {
  switch (status) {
    case 'ONLINE': return 'ONLINE';
    case 'OFFLINE': return 'OFFLINE';
    case 'INITIALIZING': return 'STARTING';
    case 'BOOTING': return 'STARTING';
    case 'WAITING_FOR_DATA': return 'WAITING';
    case 'SHUTTING_DOWN': return 'ENDING';
    case 'ERROR': return 'ERROR';
    case 'INVALID': return 'ERROR';
    default: return 'CONNECTING';
  }
}

function StatusIcon({ status }: { status?: StreamStatus }) {
  const iconClass = "w-5 h-5";

  // Spinner for loading states
  if (status === 'INITIALIZING' || status === 'BOOTING' || status === 'WAITING_FOR_DATA' || !status) {
    return (
      <svg className={`${iconClass} animate-spin`} fill="none" viewBox="0 0 24 24" style={{ color: 'hsl(var(--tn-yellow, 40 95% 64%))' }}>
        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z" />
      </svg>
    );
  }

  // Offline icon
  if (status === 'OFFLINE') {
    return (
      <svg className={iconClass} fill="none" viewBox="0 0 24 24" stroke="currentColor" style={{ color: 'hsl(var(--tn-red, 348 100% 72%))' }}>
        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M18.364 5.636a9 9 0 010 12.728m0 0l-2.829-2.829m2.829 2.829L21 21M15.536 8.464a5 5 0 010 7.072m0 0l-2.829-2.829m-4.243 2.829a4.978 4.978 0 01-1.414-2.83m-1.414 5.658a9 9 0 01-2.167-9.238m7.824 2.167a1 1 0 111.414 1.414m-1.414-1.414L3 3m8.293 8.293l1.414 1.414" />
      </svg>
    );
  }

  // Error icon
  if (status === 'ERROR' || status === 'INVALID') {
    return (
      <svg className={iconClass} fill="none" viewBox="0 0 24 24" stroke="currentColor" style={{ color: 'hsl(var(--tn-red, 348 100% 72%))' }}>
        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" />
      </svg>
    );
  }

  // Default spinner
  return (
    <svg className={`${iconClass} animate-spin`} fill="none" viewBox="0 0 24 24" style={{ color: 'hsl(var(--tn-cyan, 193 100% 75%))' }}>
      <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
      <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z" />
    </svg>
  );
}

const StatusOverlay: React.FC<StatusOverlayProps> = ({ status, message, percentage, error, onRetry }) => {
  const showRetry = (status === 'ERROR' || status === 'INVALID') && onRetry;
  const showProgress = status === 'INITIALIZING' && percentage !== undefined;
  const displayMessage = error || message;

  return (
    <div
      style={{
        position: "absolute",
        bottom: "16px",
        left: "50%",
        transform: "translateX(-50%)",
        zIndex: 20,
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        gap: "8px",
        maxWidth: "280px",
        textAlign: "center",
      }}
    >
      {/* Subtle status indicator - just icon + message */}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: "8px",
          color: "#787c99",
          fontSize: "13px",
          fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif",
        }}
      >
        <StatusIcon status={status} />
        <span>{displayMessage}</span>
      </div>

      {/* Progress bar */}
      {showProgress && (
        <div
          style={{
            width: "160px",
            height: "4px",
            background: "rgba(65, 72, 104, 0.4)",
            borderRadius: "2px",
            overflow: "hidden",
          }}
        >
          <div
            style={{
              width: `${Math.min(100, percentage)}%`,
              height: "100%",
              background: "hsl(var(--tn-cyan, 193 100% 75%))",
              transition: "width 0.3s ease-out",
            }}
          />
        </div>
      )}

      {/* Retry button - only for errors */}
      {showRetry && (
        <button
          type="button"
          onClick={onRetry}
          style={{
            padding: "6px 16px",
            background: "transparent",
            border: "1px solid rgba(122, 162, 247, 0.4)",
            borderRadius: "4px",
            color: "#7aa2f7",
            fontSize: "11px",
            fontWeight: 500,
            cursor: "pointer",
            transition: "all 0.2s ease",
            fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif",
          }}
          onMouseEnter={(e) => {
            e.currentTarget.style.background = "rgba(122, 162, 247, 0.1)";
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.background = "transparent";
          }}
        >
          Retry
        </button>
      )}
    </div>
  );
};

// ============================================================================
// Hitmarker System
// ============================================================================

interface Hitmarker {
  id: number;
  x: number;
  y: number;
}

const playHitmarkerSound = () => {
  try {
    // Embedded hitmarker sound as base64 data URL
    const hitmarkerDataUrl = 'data:audio/mpeg;base64,SUQzBAAAAAAANFRDT04AAAAHAAADT3RoZXIAVFNTRQAAAA8AAANMYXZmNTcuODMuMTAwAAAAAAAAAAAA' +
      'AAD/+1QAAAAAAAAAAAAAAAAAAAAA' +
      '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' +
      'qqqqqqqqqqqqqqqqqqqqqqqqqqo=';

    const audio = new Audio(hitmarkerDataUrl);
    audio.volume = 0.3;
    audio.play().catch(() => createSyntheticHitmarkerSound());
  } catch {
    createSyntheticHitmarkerSound();
  }
};

const createSyntheticHitmarkerSound = () => {
  try {
    const audioContext = new (window.AudioContext || (window as any).webkitAudioContext)();
    const oscillator1 = audioContext.createOscillator();
    const oscillator2 = audioContext.createOscillator();
    const gainNode1 = audioContext.createGain();
    const gainNode2 = audioContext.createGain();
    const masterGain = audioContext.createGain();

    oscillator1.connect(gainNode1);
    oscillator2.connect(gainNode2);
    gainNode1.connect(masterGain);
    gainNode2.connect(masterGain);
    masterGain.connect(audioContext.destination);

    oscillator1.frequency.setValueAtTime(1800, audioContext.currentTime);
    oscillator1.frequency.exponentialRampToValueAtTime(900, audioContext.currentTime + 0.08);
    oscillator2.frequency.setValueAtTime(3600, audioContext.currentTime);
    oscillator2.frequency.exponentialRampToValueAtTime(1800, audioContext.currentTime + 0.04);

    oscillator1.type = 'triangle';
    oscillator2.type = 'sine';

    gainNode1.gain.setValueAtTime(0, audioContext.currentTime);
    gainNode1.gain.linearRampToValueAtTime(0.4, audioContext.currentTime + 0.002);
    gainNode1.gain.exponentialRampToValueAtTime(0.001, audioContext.currentTime + 0.12);

    gainNode2.gain.setValueAtTime(0, audioContext.currentTime);
    gainNode2.gain.linearRampToValueAtTime(0.3, audioContext.currentTime + 0.001);
    gainNode2.gain.exponentialRampToValueAtTime(0.001, audioContext.currentTime + 0.06);

    masterGain.gain.setValueAtTime(0.5, audioContext.currentTime);

    const startTime = audioContext.currentTime;
    const stopTime = startTime + 0.15;

    oscillator1.start(startTime);
    oscillator2.start(startTime);
    oscillator1.stop(stopTime);
    oscillator2.stop(stopTime);
  } catch {
    // Audio context not available
  }
};

// ============================================================================
// IdleScreen Component (Main Export)
// ============================================================================

export interface IdleScreenProps {
  /** Stream status (OFFLINE, INITIALIZING, ERROR, etc.) */
  status?: StreamStatus;
  /** Human-readable message */
  message?: string;
  /** Processing percentage (for INITIALIZING) */
  percentage?: number;
  /** Error message */
  error?: string;
  /** Callback for retry button */
  onRetry?: () => void;
}

export const IdleScreen: React.FC<IdleScreenProps> = ({
  status,
  message = "Waiting for stream...",
  percentage,
  error,
  onRetry,
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const [hitmarkers, setHitmarkers] = useState<Hitmarker[]>([]);

  const createHitmarker = (e: { clientX: number; clientY: number }) => {
    if (!containerRef.current) return;

    const rect = containerRef.current.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const y = e.clientY - rect.top;

    const newHitmarker: Hitmarker = {
      id: Date.now() + Math.random(),
      x,
      y,
    };

    setHitmarkers(prev => [...prev, newHitmarker]);
    playHitmarkerSound();

    setTimeout(() => {
      setHitmarkers(prev => prev.filter(h => h.id !== newHitmarker.id));
    }, 600);
  };

  // Inject CSS animations
  useEffect(() => {
    const styleId = 'idle-screen-animations';
    if (!document.getElementById(styleId)) {
      const style = document.createElement('style');
      style.id = styleId;
      style.textContent = `
        @keyframes fadeInOut {
          0%, 100% { opacity: 0.6; }
          50% { opacity: 0.9; }
        }
        @keyframes logoPulse {
          0%, 100% { opacity: 0.15; transform: scale(1); }
          50% { opacity: 0.25; transform: scale(1.05); }
        }
        @keyframes floatUp {
          0% { transform: translateY(100vh) rotate(0deg); opacity: 0; }
          10% { opacity: 0.6; }
          90% { opacity: 0.6; }
          100% { transform: translateY(-100px) rotate(360deg); opacity: 0; }
        }
        @keyframes gradientShift {
          0%, 100% { background-position: 0% 50%; }
          50% { background-position: 100% 50%; }
        }
        @keyframes hitmarkerFade45 {
          0% { opacity: 1; transform: translate(-50%, -50%) rotate(45deg) scale(0.5); }
          20% { opacity: 1; transform: translate(-50%, -50%) rotate(45deg) scale(1.2); }
          100% { opacity: 0; transform: translate(-50%, -50%) rotate(45deg) scale(1); }
        }
        @keyframes hitmarkerFadeNeg45 {
          0% { opacity: 1; transform: translate(-50%, -50%) rotate(-45deg) scale(0.5); }
          20% { opacity: 1; transform: translate(-50%, -50%) rotate(-45deg) scale(1.2); }
          100% { opacity: 0; transform: translate(-50%, -50%) rotate(-45deg) scale(1); }
        }
      `;
      document.head.appendChild(style);
    }
  }, []);

  return (
    <div
      ref={containerRef}
      className="fw-player-root"
      style={{
        position: "absolute",
        inset: 0,
        zIndex: 5,
        background: `
          linear-gradient(135deg,
            hsl(var(--tn-bg-dark, 235 21% 11%)) 0%,
            hsl(var(--tn-bg, 233 23% 17%)) 25%,
            hsl(var(--tn-bg-dark, 235 21% 11%)) 50%,
            hsl(var(--tn-bg, 233 23% 17%)) 75%,
            hsl(var(--tn-bg-dark, 235 21% 11%)) 100%
          )
        `,
        backgroundSize: "400% 400%",
        animation: "gradientShift 16s ease-in-out infinite",
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        justifyContent: "center",
        overflow: "hidden",
        borderRadius: "0",
        userSelect: "none",
      } as React.CSSProperties}
    >
      {/* Hitmarkers */}
      {hitmarkers.map(hitmarker => (
        <div
          key={hitmarker.id}
          style={{
            position: "absolute",
            left: `${hitmarker.x}px`,
            top: `${hitmarker.y}px`,
            transform: "translate(-50%, -50%)",
            pointerEvents: "none",
            zIndex: 100,
            width: "40px",
            height: "40px",
          }}
        >
          <div style={{ position: "absolute", top: "25%", left: "25%", width: "12px", height: "3px", backgroundColor: "#ffffff", transform: "translate(-50%, -50%) rotate(45deg)", animation: "hitmarkerFade45 0.6s ease-out forwards", boxShadow: "0 0 8px rgba(255, 255, 255, 0.8)", borderRadius: "1px" }} />
          <div style={{ position: "absolute", top: "25%", left: "75%", width: "12px", height: "3px", backgroundColor: "#ffffff", transform: "translate(-50%, -50%) rotate(-45deg)", animation: "hitmarkerFadeNeg45 0.6s ease-out forwards", boxShadow: "0 0 8px rgba(255, 255, 255, 0.8)", borderRadius: "1px" }} />
          <div style={{ position: "absolute", top: "75%", left: "25%", width: "12px", height: "3px", backgroundColor: "#ffffff", transform: "translate(-50%, -50%) rotate(-45deg)", animation: "hitmarkerFadeNeg45 0.6s ease-out forwards", boxShadow: "0 0 8px rgba(255, 255, 255, 0.8)", borderRadius: "1px" }} />
          <div style={{ position: "absolute", top: "75%", left: "75%", width: "12px", height: "3px", backgroundColor: "#ffffff", transform: "translate(-50%, -50%) rotate(45deg)", animation: "hitmarkerFade45 0.6s ease-out forwards", boxShadow: "0 0 8px rgba(255, 255, 255, 0.8)", borderRadius: "1px" }} />
        </div>
      ))}

      {/* Floating particles */}
      {[...Array(12)].map((_, index) => (
        <div
          key={`particle-${index}`}
          style={{
            position: "absolute",
            left: `${Math.random() * 100}%`,
            width: `${Math.random() * 4 + 2}px`,
            height: `${Math.random() * 4 + 2}px`,
            borderRadius: "50%",
            background: ["#7aa2f7", "#bb9af7", "#9ece6a", "#73daca", "#7dcfff", "#f7768e", "#e0af68", "#2ac3de"][index % 8],
            opacity: 0,
            animation: `floatUp ${8 + Math.random() * 4}s linear infinite`,
            animationDelay: `${Math.random() * 8}s`,
            pointerEvents: "none",
          }}
        />
      ))}

      {/* Animated bubbles */}
      {[...Array(8)].map((_, index) => (
        <AnimatedBubble key={index} index={index} />
      ))}

      {/* Center logo */}
      <CenterLogo containerRef={containerRef as React.RefObject<HTMLDivElement>} onHitmarker={createHitmarker} />

      {/* DVD Logo */}
      <DvdLogo parentRef={containerRef as React.RefObject<HTMLDivElement>} scale={0.08} />

      {/* Status overlay */}
      <StatusOverlay
        status={status}
        message={message}
        percentage={percentage}
        error={error}
        onRetry={onRetry}
      />

      {/* Overlay texture */}
      <div
        style={{
          position: "absolute",
          top: 0,
          left: 0,
          right: 0,
          bottom: 0,
          background: `
            radial-gradient(circle at 20% 80%, rgba(122, 162, 247, 0.03) 0%, transparent 50%),
            radial-gradient(circle at 80% 20%, rgba(187, 154, 247, 0.03) 0%, transparent 50%),
            radial-gradient(circle at 40% 40%, rgba(158, 206, 106, 0.02) 0%, transparent 50%)
          `,
          pointerEvents: "none",
        }}
      />
    </div>
  );
};

export default IdleScreen;
