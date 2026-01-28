import * as React from "react";
import * as SliderPrimitive from "@radix-ui/react-slider";
import { cn } from "@livepeer-frameworks/player-core";

export interface SliderProps extends React.ComponentPropsWithoutRef<typeof SliderPrimitive.Root> {
  showTrack?: boolean;
  trackClassName?: string;
  thumbClassName?: string;
  /** Show thumb only on hover (YouTube-style) - but always shows a smaller thumb when not hovered */
  hoverThumb?: boolean;
  /** Use cyan accent color (matches SeekBar styling) */
  accentColor?: boolean;
}

const Slider = React.forwardRef<React.ElementRef<typeof SliderPrimitive.Root>, SliderProps>(
  (
    {
      className,
      trackClassName,
      thumbClassName,
      showTrack = true,
      hoverThumb: _hoverThumb = false,
      accentColor = false,
      orientation = "horizontal",
      ...props
    },
    ref
  ) => {
    return (
      <SliderPrimitive.Root
        ref={ref}
        orientation={orientation}
        className={cn("fw-slider", orientation === "vertical" && "fw-slider--vertical", className)}
        {...props}
      >
        {showTrack && (
          <SliderPrimitive.Track className={cn("fw-slider-track", trackClassName)}>
            <SliderPrimitive.Range
              className={cn("fw-slider-range", accentColor && "fw-slider-range--accent")}
            />
          </SliderPrimitive.Track>
        )}
        <SliderPrimitive.Thumb
          className={cn(
            "fw-slider-thumb",
            accentColor && "fw-slider-thumb--accent",
            thumbClassName
          )}
        />
      </SliderPrimitive.Root>
    );
  }
);
Slider.displayName = SliderPrimitive.Root.displayName;

export { Slider };
