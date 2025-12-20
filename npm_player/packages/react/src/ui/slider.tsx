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
  ({ className, trackClassName, thumbClassName, showTrack = true, hoverThumb = false, accentColor = false, orientation = "horizontal", ...props }, ref) => {
    // Colors based on accentColor prop
    const rangeColorClass = accentColor ? "bg-[hsl(var(--tn-cyan,195_100%_50%))]" : "bg-white/90";
    const thumbColorClass = accentColor ? "bg-[hsl(var(--tn-cyan,195_100%_50%))]" : "bg-white";

    return (
      <SliderPrimitive.Root
        ref={ref}
        orientation={orientation}
        className={cn(
          "group relative flex touch-none select-none items-center cursor-pointer",
          orientation === "horizontal" ? "w-full h-5" : "h-full flex-col w-5",
          className
        )}
        {...props}
      >
        {showTrack && (
          <SliderPrimitive.Track
            className={cn(
              "absolute rounded-full bg-white/30 transition-all duration-150",
              orientation === "horizontal"
                ? "inset-x-0 h-1 group-hover:h-1.5"
                : "inset-y-0 w-1 group-hover:w-1.5",
              trackClassName
            )}
          >
            <SliderPrimitive.Range
              className={cn(
                "absolute rounded-full transition-all duration-150",
                orientation === "horizontal" ? "h-full" : "w-full bottom-0",
                rangeColorClass
              )}
            />
          </SliderPrimitive.Track>
        )}
        <SliderPrimitive.Thumb
          className={cn(
            "block rounded-full border-0 cursor-pointer shadow-md transition-all duration-150",
            "w-2.5 h-2.5 group-hover:w-3.5 group-hover:h-3.5",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/50",
            "disabled:pointer-events-none disabled:opacity-50",
            thumbColorClass,
            thumbClassName
          )}
        />
      </SliderPrimitive.Root>
    );
  }
);
Slider.displayName = SliderPrimitive.Root.displayName;

export { Slider };
