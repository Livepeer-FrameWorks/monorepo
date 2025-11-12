import React, { useRef, useEffect, useState } from "react";
import DvdLogo from "./DvdLogo";
import logomarkAsset from "../../public/logomark.svg";

interface AnimatedBubbleProps {
  index: number;
}

const AnimatedBubble: React.FC<AnimatedBubbleProps> = ({ index }) => {
  const [position, setPosition] = useState({ top: 0, left: 0 });
  const [size, setSize] = useState(40);
  const [opacity, setOpacity] = useState(0);

  const getRandomPosition = () => ({
    top: Math.random() * 80 + 10, // 10% to 90%
    left: Math.random() * 80 + 10, // 10% to 90%
  });

  const getRandomSize = () => Math.random() * 60 + 30; // 30px to 90px

  useEffect(() => {
    // Initial random position and size
    setPosition(getRandomPosition());
    setSize(getRandomSize());

    const animationCycle = () => {
      // Fade in
      setOpacity(0.15);

      setTimeout(() => {
        // Fade out
        setOpacity(0);

        setTimeout(() => {
          // Move to new random position with new size while invisible
          setPosition(getRandomPosition());
          setSize(getRandomSize());
          // Start next cycle after a brief delay to ensure position change is complete
          setTimeout(() => {
            animationCycle();
          }, 200);
        }, 1500); // Wait for fade out to complete
      }, 4000 + Math.random() * 3000); // Stay visible for 4-7 seconds (was 2-4)
    };

    // Start the animation cycle with staggered timing
    const timeout = setTimeout(animationCycle, index * 500);

    return () => {
      clearTimeout(timeout);
    };
  }, [index]);

  // Tokyo Night inspired pastel colors
  const bubbleColors = [
    "rgba(122, 162, 247, 0.2)", // Terminal Blue
    "rgba(187, 154, 247, 0.2)", // Terminal Magenta
    "rgba(158, 206, 106, 0.2)", // Strings/CSS classes  
    "rgba(115, 218, 202, 0.2)", // Terminal Green
    "rgba(125, 207, 255, 0.2)", // Terminal Cyan
    "rgba(247, 118, 142, 0.2)", // Keywords/Terminal Red
    "rgba(224, 175, 104, 0.2)", // Terminal Yellow
    "rgba(42, 195, 222, 0.2)",  // Language functions
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
        opacity: opacity,
        transition: "opacity 1s ease-in-out",
        pointerEvents: "none",
        userSelect: "none",
        WebkitUserSelect: "none",
        MozUserSelect: "none",
        msUserSelect: "none",
      } as React.CSSProperties}
    />
  );
};

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
    e.stopPropagation(); // Prevent event bubbling to container
    if (onHitmarker) {
      // Get the exact click position on the logo
      onHitmarker({ clientX: e.clientX, clientY: e.clientY });
    }
  };

  const handleMouseMove = (e: MouseEvent) => {
    if (!containerRef.current) return;

    const rect = containerRef.current.getBoundingClientRect();
    const centerX = rect.left + rect.width / 2;
    const centerY = rect.top + rect.height / 2;

    const mouseX = e.clientX;
    const mouseY = e.clientY;

    // Calculate distance from center
    const deltaX = mouseX - centerX;
    const deltaY = mouseY - centerY;
    const distance = Math.sqrt(deltaX * deltaX + deltaY * deltaY);

    // If mouse is close to logo, move it away
    const maxDistance = logoSize * 1.5; // Detection radius
    if (distance < maxDistance && distance > 0) {
      const pushStrength = (maxDistance - distance) / maxDistance;
      const pushDistance = 50 * pushStrength; // Max push distance

      // Normalize direction and apply push
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
        WebkitUserSelect: "none",
        MozUserSelect: "none",
        msUserSelect: "none",
      }}
    >
      {/* Pulsing circle background */}
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
          userSelect: "none",
          WebkitUserSelect: "none",
          MozUserSelect: "none",
          msUserSelect: "none",
          pointerEvents: "none",
        }}
      />

      {/* Logo - only the SVG is clickable */}
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
          WebkitUserSelect: "none",
          MozUserSelect: "none",
          msUserSelect: "none",
          WebkitUserDrag: "none",
          WebkitTouchCallout: "none",
        } as React.CSSProperties}
      />
    </div>
  );
};

interface LoadingScreenProps {
  message?: string;
}

interface Hitmarker {
  id: number;
  x: number;
  y: number;
}

const LoadingScreen: React.FC<LoadingScreenProps> = ({ message = "Waiting for source..." }) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const [hitmarkers, setHitmarkers] = useState<Hitmarker[]>([]);

  const playHitmarkerSound = () => {
    try {
      // Embedded hitmarker sound as base64 data URL
      const hitmarkerDataUrl = 'data:audio/mpeg;base64,SUQzBAAAAAAANFRDT04AAAAHAAADT3RoZXIAVFNTRQAAAA8AAANMYXZmNTcuODMuMTAwAAAAAAAAAAAA' +
        'AAD/+1QAAAAAAAAAAAAAAAAAAAAA' +
        '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' +
        'qqqqqqqqqqqqqqqqqqqqqqqqqqo=';
      
      const audio = new Audio(hitmarkerDataUrl);
      audio.volume = 0.3;
      audio.play().catch(() => {
        // Fallback to synthetic sound if data URL fails
        createSyntheticHitmarkerSound();
      });
    } catch (error) {
      // Fallback to synthetic sound
      createSyntheticHitmarkerSound();
    }
  };

  const createSyntheticHitmarkerSound = () => {
    try {
      const audioContext = new (window.AudioContext || (window as any).webkitAudioContext)();
      
      // Create a more realistic hitmarker sound with noise and metallic ring
      const oscillator1 = audioContext.createOscillator();
      const oscillator2 = audioContext.createOscillator();
      const noiseBuffer = audioContext.createBuffer(1, audioContext.sampleRate * 0.1, audioContext.sampleRate);
      const noiseSource = audioContext.createBufferSource();
      
      // Generate white noise for the initial "crack"
      const noiseData = noiseBuffer.getChannelData(0);
      for (let i = 0; i < noiseData.length; i++) {
        noiseData[i] = Math.random() * 2 - 1;
      }
      noiseSource.buffer = noiseBuffer;
      
      const gainNode1 = audioContext.createGain();
      const gainNode2 = audioContext.createGain();
      const noiseGain = audioContext.createGain();
      const masterGain = audioContext.createGain();
      
      // Connect everything
      oscillator1.connect(gainNode1);
      oscillator2.connect(gainNode2);
      noiseSource.connect(noiseGain);
      
      gainNode1.connect(masterGain);
      gainNode2.connect(masterGain);
      noiseGain.connect(masterGain);
      masterGain.connect(audioContext.destination);
      
      // Sharp metallic frequencies
      oscillator1.frequency.setValueAtTime(1800, audioContext.currentTime);
      oscillator1.frequency.exponentialRampToValueAtTime(900, audioContext.currentTime + 0.08);
      
      oscillator2.frequency.setValueAtTime(3600, audioContext.currentTime);
      oscillator2.frequency.exponentialRampToValueAtTime(1800, audioContext.currentTime + 0.04);
      
      oscillator1.type = 'triangle';
      oscillator2.type = 'sine';
      
      // Sharp attack, quick decay
      gainNode1.gain.setValueAtTime(0, audioContext.currentTime);
      gainNode1.gain.linearRampToValueAtTime(0.4, audioContext.currentTime + 0.002);
      gainNode1.gain.exponentialRampToValueAtTime(0.001, audioContext.currentTime + 0.12);
      
      gainNode2.gain.setValueAtTime(0, audioContext.currentTime);
      gainNode2.gain.linearRampToValueAtTime(0.3, audioContext.currentTime + 0.001);
      gainNode2.gain.exponentialRampToValueAtTime(0.001, audioContext.currentTime + 0.06);
      
      // Noise burst for the initial crack
      noiseGain.gain.setValueAtTime(0, audioContext.currentTime);
      noiseGain.gain.linearRampToValueAtTime(0.2, audioContext.currentTime + 0.001);
      noiseGain.gain.exponentialRampToValueAtTime(0.001, audioContext.currentTime + 0.01);
      
      masterGain.gain.setValueAtTime(0.5, audioContext.currentTime);
      
      const startTime = audioContext.currentTime;
      const stopTime = startTime + 0.15;
      
      oscillator1.start(startTime);
      oscillator2.start(startTime);
      noiseSource.start(startTime);
      
      oscillator1.stop(stopTime);
      oscillator2.stop(stopTime);
      noiseSource.stop(startTime + 0.02);
      
    } catch (error) {
      console.log('Audio context not available');
    }
  };

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
    
    // Play sound
    playHitmarkerSound();
    
    // Remove hitmarker after animation
    setTimeout(() => {
      setHitmarkers(prev => prev.filter(h => h.id !== newHitmarker.id));
    }, 600);
  };

  // Inject CSS animations
  useEffect(() => {
    const styleId = 'loading-screen-animations';
    if (!document.getElementById(styleId)) {
      const style = document.createElement('style');
      style.id = styleId;
      style.textContent = `
        @keyframes fadeInOut {
          0%, 100% {
            opacity: 0.6;
          }
          50% {
            opacity: 0.9;
          }
        }

        @keyframes logoPulse {
          0%, 100% {
            opacity: 0.15;
            transform: scale(1);
          }
          50% {
            opacity: 0.25;
            transform: scale(1.05);
          }
        }

        @keyframes shimmer {
          0% {
            background-position: -200px 0;
          }
          100% {
            background-position: calc(200px + 100%) 0;
          }
        }

        @keyframes floatUp {
          0% {
            transform: translateY(100vh) rotate(0deg);
            opacity: 0;
          }
          10% {
            opacity: 0.6;
          }
          90% {
            opacity: 0.6;
          }
          100% {
            transform: translateY(-100px) rotate(360deg);
            opacity: 0;
          }
        }

        @keyframes gradientShift {
          0%, 100% {
            background-position: 0% 50%;
          }
          50% {
            background-position: 100% 50%;
          }
        }

        @keyframes hitmarkerFade {
          0% {
            opacity: 1;
            transform: scale(0.5);
          }
          20% {
            opacity: 1;
            transform: scale(1.2);
          }
          100% {
            opacity: 0;
            transform: scale(1);
          }
        }

        @keyframes hitmarkerFade45 {
          0% {
            opacity: 1;
            transform: translate(-50%, -50%) rotate(45deg) scale(0.5);
          }
          20% {
            opacity: 1;
            transform: translate(-50%, -50%) rotate(45deg) scale(1.2);
          }
          100% {
            opacity: 0;
            transform: translate(-50%, -50%) rotate(45deg) scale(1);
          }
        }

        @keyframes hitmarkerFadeNeg45 {
          0% {
            opacity: 1;
            transform: translate(-50%, -50%) rotate(-45deg) scale(0.5);
          }
          20% {
            opacity: 1;
            transform: translate(-50%, -50%) rotate(-45deg) scale(1.2);
          }
          100% {
            opacity: 0;
            transform: translate(-50%, -50%) rotate(-45deg) scale(1);
          }
        }
      `;
      document.head.appendChild(style);
    }
  }, []);

  return (
    <div
      ref={containerRef}
      style={{
        position: "relative",
        width: "100%",
        height: "100%",
        minHeight: "300px",
        background: `
          linear-gradient(135deg, 
            #1a1b26 0%, 
            #24283b 25%, 
            #1a1b26 50%, 
            #24283b 75%, 
            #1a1b26 100%
          )
        `,
        backgroundSize: "400% 400%",
        animation: "gradientShift 16s ease-in-out infinite",
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        justifyContent: "center",
        overflow: "hidden",
        borderRadius: "1px",
        userSelect: "none",
        WebkitUserSelect: "none",
        MozUserSelect: "none",
        msUserSelect: "none",
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
          {/* Top-left diagonal line */}
          <div
            style={{
              position: "absolute",
              top: "25%",
              left: "25%",
              width: "12px",
              height: "3px",
              backgroundColor: "#ffffff",
              transform: "translate(-50%, -50%) rotate(45deg)",
              animation: "hitmarkerFade45 0.6s ease-out forwards",
              boxShadow: "0 0 8px rgba(255, 255, 255, 0.8)",
              borderRadius: "1px",
            }}
          />
          {/* Top-right diagonal line */}
          <div
            style={{
              position: "absolute",
              top: "25%",
              left: "75%",
              width: "12px",
              height: "3px",
              backgroundColor: "#ffffff",
              transform: "translate(-50%, -50%) rotate(-45deg)",
              animation: "hitmarkerFadeNeg45 0.6s ease-out forwards",
              boxShadow: "0 0 8px rgba(255, 255, 255, 0.8)",
              borderRadius: "1px",
            }}
          />
          {/* Bottom-left diagonal line */}
          <div
            style={{
              position: "absolute",
              top: "75%",
              left: "25%",
              width: "12px",
              height: "3px",
              backgroundColor: "#ffffff",
              transform: "translate(-50%, -50%) rotate(-45deg)",
              animation: "hitmarkerFadeNeg45 0.6s ease-out forwards",
              boxShadow: "0 0 8px rgba(255, 255, 255, 0.8)",
              borderRadius: "1px",
            }}
          />
          {/* Bottom-right diagonal line */}
          <div
            style={{
              position: "absolute",
              top: "75%",
              left: "75%",
              width: "12px",
              height: "3px",
              backgroundColor: "#ffffff",
              transform: "translate(-50%, -50%) rotate(45deg)",
              animation: "hitmarkerFade45 0.6s ease-out forwards",
              boxShadow: "0 0 8px rgba(255, 255, 255, 0.8)",
              borderRadius: "1px",
            }}
          />
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
            background: [
              "#7aa2f7", // Terminal Blue
              "#bb9af7", // Terminal Magenta  
              "#9ece6a", // Strings/CSS classes
              "#73daca", // Terminal Green
              "#7dcfff", // Terminal Cyan
              "#f7768e", // Keywords/Terminal Red
              "#e0af68", // Terminal Yellow
              "#2ac3de", // Language functions
            ][index % 8],
            opacity: 0,
            animation: `floatUp ${8 + Math.random() * 4}s linear infinite`,
            animationDelay: `${Math.random() * 8}s`,
            pointerEvents: "none",
            userSelect: "none",
          }}
        />
      ))}

      {/* Animated bubbles with Tokyo Night colors */}
      {[...Array(8)].map((_, index) => (
        <AnimatedBubble key={index} index={index} />
      ))}

      {/* Center logo */}
      <CenterLogo containerRef={containerRef as React.RefObject<HTMLDivElement>} onHitmarker={createHitmarker} />

      {/* Bouncing DVD Logo */}
      <DvdLogo parentRef={containerRef as React.RefObject<HTMLDivElement>} scale={0.08} />

      {/* Message */}
      <div
        style={{
          position: "absolute",
          bottom: "20%",
          left: "50%",
          transform: "translateX(-50%)",
          color: "#a9b1d6",
          fontSize: "16px",
          fontWeight: "500",
          textAlign: "center",
          animation: "fadeInOut 2s ease-in-out infinite",
          textShadow: "0 2px 4px rgba(36, 40, 59, 0.5)",
          fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif",
          userSelect: "none",
          WebkitUserSelect: "none",
          MozUserSelect: "none",
          msUserSelect: "none",
          pointerEvents: "none",
        }}
      >
        {message}
      </div>

      {/* Subtle overlay texture */}
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
          userSelect: "none",
        }}
      />
    </div>
  );
};

export default LoadingScreen; 
