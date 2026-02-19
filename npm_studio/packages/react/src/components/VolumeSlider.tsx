/**
 * VolumeSlider - Reusable volume slider component with popup and snap-to-100%
 */

import React, { useState, useRef, useCallback } from "react";

interface VolumeSliderProps {
  /** Current value (0-2 for 0-200%) */
  value: number;
  /** Callback when value changes */
  onChange: (value: number) => void;
  /** Min value (default 0) */
  min?: number;
  /** Max value (default 2 for 200%) */
  max?: number;
  /** Snap threshold around 100% (default 0.05 = 5%) */
  snapThreshold?: number;
  /** Optional className for the container */
  className?: string;
  /** Compact mode for inline use */
  compact?: boolean;
}

export const VolumeSlider: React.FC<VolumeSliderProps> = ({
  value,
  onChange,
  min = 0,
  max = 2,
  snapThreshold = 0.05,
  className,
  compact = false,
}) => {
  const [isDragging, setIsDragging] = useState(false);
  const [popupPosition, setPopupPosition] = useState(0);
  const sliderRef = useRef<HTMLInputElement>(null);

  const handleChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      let newValue = parseInt(e.target.value, 10) / 100;

      // Snap to 100% if within threshold
      if (Math.abs(newValue - 1) <= snapThreshold) {
        newValue = 1;
      }

      onChange(newValue);

      // Update popup position
      if (sliderRef.current) {
        const rect = sliderRef.current.getBoundingClientRect();
        const percent = (newValue - min) / (max - min);
        setPopupPosition(percent * rect.width);
      }
    },
    [onChange, min, max, snapThreshold]
  );

  const handleMouseDown = useCallback(() => {
    setIsDragging(true);
    // Update initial position
    if (sliderRef.current) {
      const rect = sliderRef.current.getBoundingClientRect();
      const percent = (value - min) / (max - min);
      setPopupPosition(percent * rect.width);
    }
  }, [value, min, max]);

  const handleMouseUp = useCallback(() => {
    setIsDragging(false);
  }, []);

  const displayValue = Math.round(value * 100);
  const isBoost = value > 1;
  const isDefault = value === 1;

  return (
    <div
      className={className}
      style={{ position: "relative", flex: 1, minWidth: compact ? "60px" : "100px" }}
    >
      {/* Popup tooltip */}
      {isDragging && (
        <div
          style={{
            position: "absolute",
            bottom: "100%",
            left: `${popupPosition}px`,
            transform: "translateX(-50%)",
            marginBottom: "8px",
            padding: "4px 8px",
            background: isBoost
              ? "hsl(var(--fw-sc-warning))"
              : isDefault
                ? "hsl(var(--fw-sc-success))"
                : "hsl(var(--fw-sc-accent))",
            color: "hsl(var(--fw-sc-on-accent))",
            borderRadius: "4px",
            fontSize: "12px",
            fontWeight: 600,
            fontFamily: "monospace",
            whiteSpace: "nowrap",
            pointerEvents: "none",
            zIndex: 100,
            boxShadow: "0 2px 8px rgba(0,0,0,0.3)",
          }}
        >
          {displayValue}%{isDefault && " (default)"}
          {/* Arrow */}
          <div
            style={{
              position: "absolute",
              top: "100%",
              left: "50%",
              transform: "translateX(-50%)",
              width: 0,
              height: 0,
              borderLeft: "6px solid transparent",
              borderRight: "6px solid transparent",
              borderTop: `6px solid ${isBoost ? "hsl(var(--fw-sc-warning))" : isDefault ? "hsl(var(--fw-sc-success))" : "hsl(var(--fw-sc-accent))"}`,
            }}
          />
        </div>
      )}

      {/* Slider track with 100% marker */}
      <div style={{ position: "relative" }}>
        {/* 100% marker line */}
        <div
          style={{
            position: "absolute",
            left: `${(1 / max) * 100}%`,
            top: "0",
            bottom: "0",
            width: "2px",
            background: isDefault ? "hsl(var(--fw-sc-success))" : "hsl(var(--fw-sc-success) / 0.3)",
            borderRadius: "1px",
            zIndex: 1,
            pointerEvents: "none",
            transform: "translateX(-50%)",
          }}
        />
        <input
          ref={sliderRef}
          type="range"
          min={min * 100}
          max={max * 100}
          value={Math.round(value * 100)}
          onChange={handleChange}
          onMouseDown={handleMouseDown}
          onMouseUp={handleMouseUp}
          onMouseLeave={handleMouseUp}
          onTouchStart={handleMouseDown}
          onTouchEnd={handleMouseUp}
          style={{
            width: "100%",
            height: "6px",
            borderRadius: "3px",
            cursor: "pointer",
            accentColor: isBoost ? "hsl(var(--fw-sc-warning))" : "hsl(var(--fw-sc-accent))",
          }}
        />
      </div>
    </div>
  );
};

export default VolumeSlider;
