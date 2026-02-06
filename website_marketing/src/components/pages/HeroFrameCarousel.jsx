import { useMemo } from "react";

// Designed layout — fixed per frame
const FRAMES = [
  {
    color: "122, 162, 247",
    size: "lg",
    top: "25%",
    left: "12%",
    orbit: "left",
    sway: "a",
    orbitDur: 80,
    swayDur: 18,
  },
  {
    color: "69, 208, 255",
    size: "md",
    top: "15%",
    left: "25%",
    orbit: "mid",
    sway: "b",
    orbitDur: 70,
    swayDur: 22,
  },
  {
    color: "255, 158, 100",
    size: "xl",
    top: "40%",
    left: "38%",
    orbit: "wide",
    sway: "c",
    orbitDur: 90,
    swayDur: 20,
  },
  {
    color: "158, 206, 106",
    size: "sm",
    top: "18%",
    left: "5%",
    orbit: "tight",
    sway: "a",
    orbitDur: 55,
    swayDur: 25,
  },
  {
    color: "224, 175, 104",
    size: "md",
    top: "60%",
    left: "22%",
    orbit: "mid",
    sway: "b",
    orbitDur: 65,
    swayDur: 20,
  },
  {
    color: "122, 162, 247",
    size: "sm",
    top: "30%",
    left: "32%",
    orbit: "tight",
    sway: "c",
    orbitDur: 50,
    swayDur: 28,
  },
  {
    color: "224, 175, 104",
    size: "md",
    top: "50%",
    left: "8%",
    orbit: "left",
    sway: "a",
    orbitDur: 75,
    swayDur: 22,
  },
];

// Seeded PRNG — deterministic per page load, varies between loads
const createRng = (seed) => {
  let s = seed | 0;
  return () => {
    s = (s * 1103515245 + 12345) & 0x7fffffff;
    return s / 0x7fffffff;
  };
};

const range = (rng, min, max) => min + (max - min) * rng();

const buildFrameStyles = (seed) => {
  const rng = createRng(seed);

  return FRAMES.map((f) => {
    // Randomized entrance vector — shoot in from a random direction
    const enterAngle = range(rng, 0, Math.PI * 2);
    const enterDist = range(rng, 250, 450);
    const enterX = Math.cos(enterAngle) * enterDist;
    const enterY = Math.sin(enterAngle) * enterDist;

    // Staggered entrance timing
    const enterDelay = range(rng, 0.1, 0.7);

    // Random orbit phase — each frame starts at a different point in its loop
    const orbitPhase = range(rng, 0, f.orbitDur);
    const swayPhase = range(rng, 0, f.swayDur);

    return {
      ...f,
      enterX: `${enterX.toFixed(0)}px`,
      enterY: `${enterY.toFixed(0)}px`,
      enterDelay: `${enterDelay.toFixed(2)}s`,
      orbitDelay: `-${orbitPhase.toFixed(1)}s`,
      swayDelay: `-${swayPhase.toFixed(1)}s`,
    };
  });
};

// Seed once at module load — fresh per page load, stable across re-renders
const loadSeed = Math.floor(performance.now());

const HeroFrameCarousel = () => {
  const frames = useMemo(() => buildFrameStyles(loadSeed), []);

  return (
    <div className="hero-frame-scene" aria-hidden="true">
      {frames.map((f, i) => (
        <div
          key={i}
          className={`hero-frame hero-frame--${f.size}`}
          style={{
            "--frame-color": f.color,
            "--enter-x": f.enterX,
            "--enter-y": f.enterY,
            top: f.top,
            left: f.left,
            animation: [
              `frame-enter 1.2s ease-out ${f.enterDelay} both`,
              `orbit-${f.orbit} ${f.orbitDur}s linear ${f.orbitDelay} infinite`,
              `sway-${f.sway} ${f.swayDur}s ease-in-out ${f.swayDelay} infinite`,
            ].join(", "),
          }}
        />
      ))}
    </div>
  );
};

export default HeroFrameCarousel;
