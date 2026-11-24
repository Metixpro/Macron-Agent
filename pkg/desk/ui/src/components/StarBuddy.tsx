import { memo, useEffect, useId, useRef, useState } from "react";
import { useStore } from "../store";

export type StarBuddyMood = "idle" | "typing" | "working" | "streaming" | "asking" | "offline";
export type StarBuddySize = "xs" | "sm" | "md" | "lg" | "xl";
export type StarBuddyVariant = "card" | "peek";

const PIXELS: Record<StarBuddySize, number> = {
  xs: 22,
  sm: 26,
  md: 36,
  lg: 68,
  xl: 92,
};

export function useStarBuddyMood(): StarBuddyMood {
  const online = useStore((s) => s.online);
  const running = useStore((s) => s.live.running);
  const streaming = useStore((s) => !!s.live.html);
  const asking = useStore((s) => !!s.live.ask);
  if (online === false) return "offline";
  if (asking) return "asking";
  if (streaming) return "streaming";
  if (running) return "working";
  return "idle";
}

export const StarBuddy = memo(function StarBuddy({
  mood: explicitMood,
  size = "md",
  variant = "card",
  tucked = false,
  interactive = false,
  className = "",
  title,
}: {
  mood?: StarBuddyMood;
  size?: StarBuddySize;
  variant?: StarBuddyVariant;
  tucked?: boolean;
  interactive?: boolean;
  className?: string;
  title?: string;
}) {
  const autoMood = useStarBuddyMood();
  const mood = explicitMood || autoMood;
  const uid = useId().replace(/:/g, "");
  const [poked, setPoked] = useState(false);
  const timerRef = useRef<number | null>(null);

  useEffect(() => {
    return () => {
      if (timerRef.current !== null) window.clearTimeout(timerRef.current);
    };
  }, []);

  const triggerPoke = () => {
    if (!interactive) return;
    setPoked(true);
    if (timerRef.current !== null) window.clearTimeout(timerRef.current);
    timerRef.current = window.setTimeout(() => setPoked(false), 680);
  };

  if (variant === "peek") {
    const auraColor = mood === "asking" ? "#f59e0b" : mood === "offline" ? "#64748b" : "#3b82f6";
    const rimColor = mood === "asking" ? "#fcd34d" : "#93c5fd";
    return (
      <button
        type="button"
        className={`starbuddy-peek mood-${mood}${tucked ? " tucked" : ""}${poked ? " poked" : ""}${className ? " " + className : ""}`}
        title={title || "Macron StarBuddy"}
        aria-label={title || "Macron StarBuddy"}
        onClick={triggerPoke}
      >
        <svg viewBox="0 0 116 74" width="66" height="42" fill="none" xmlns="http://www.w3.org/2000/svg">
          <defs>
            <radialGradient id={`sbp-halo-${uid}`} cx="50%" cy="62%" r="48%">
              <stop offset="0%" stopColor={auraColor} stopOpacity="0.88" />
              <stop offset="55%" stopColor={auraColor} stopOpacity="0.34" />
              <stop offset="100%" stopColor={auraColor} stopOpacity="0" />
            </radialGradient>
            <radialGradient id={`sbp-body-${uid}`} cx="44%" cy="32%" r="68%">
              <stop offset="0%" stopColor="#ffffff" />
              <stop offset="56%" stopColor="#f0f6ff" />
              <stop offset="86%" stopColor="#c5dcff" />
              <stop offset="100%" stopColor="#89b4ff" />
            </radialGradient>
            <linearGradient id={`sbp-paw-${uid}`} x1="0" y1="54" x2="0" y2="70" gradientUnits="userSpaceOnUse">
              <stop offset="0%" stopColor="#ffffff" />
              <stop offset="68%" stopColor="#dbeafe" />
              <stop offset="100%" stopColor="#8bb6ff" />
            </linearGradient>
            <filter id={`sbp-glow-${uid}`} x="-25%" y="-25%" width="150%" height="150%">
              <feDropShadow dx="0" dy="0" stdDeviation="4.5" floodColor={auraColor} floodOpacity="0.72" />
            </filter>
          </defs>

          {/* Twinkling mini sparkles around StarBuddy */}
          <g className="sb-stars">
            <path className="sb-s1" d="M17 18 L18.5 14 L20 18 L24 19.5 L20 21 L18.5 25 L17 21 L13 19.5 Z" fill="#bfdbfe" />
            <circle className="sb-s2" cx="98" cy="17" r="2.1" fill="#e0f2fe" />
            <circle className="sb-s3" cx="89" cy="9" r="1.4" fill="#93c5fd" />
            <circle className="sb-s1" cx="31" cy="10" r="1.3" fill="#dbeafe" />
          </g>

          {/* Soft cobalt aura */}
          <ellipse className="sb-aura" cx="58" cy="46" rx="44" ry="26" fill={`url(#sbp-halo-${uid})`} />

          {/* StarBuddy plush star body peeking over the top rim of the chat box */}
          <g className="sb-character" filter={`url(#sbp-glow-${uid})`}>
            <path
              className="sb-star-body"
              d="M58 10 C63.2 10 67.6 18.8 71.4 26.2 C74 30.8 78.2 32.3 84.4 33 C92.2 33.8 95.8 38 93.2 44.6 C91 50 86.2 55 83.8 60.5 C82.5 63.6 82 65.5 81 67 L35 67 C34 65.5 33.5 63.6 32.2 60.5 C29.8 55 25 50 22.8 44.6 C20.2 38 23.8 33.8 31.6 33 C37.8 32.3 42 30.8 44.6 26.2 C48.4 18.8 52.8 10 58 10 Z"
              fill={`url(#sbp-body-${uid})`}
              stroke={rimColor}
              strokeWidth="1.3"
              strokeOpacity="0.72"
            />

            {/* Expressive dark vertical oval eyes (or happy wink arcs when poked!) */}
            {poked ? (
              <g className="sb-eyes-happy">
                <path d="M44 45 Q49 38.5 54 45" stroke="#050b21" strokeWidth="3.2" strokeLinecap="round" fill="none" />
                <path d="M62 45 Q67 38.5 72 45" stroke="#050b21" strokeWidth="3.2" strokeLinecap="round" fill="none" />
              </g>
            ) : (
              <g className="sb-eyes">
                <ellipse className="sb-eye" cx="49" cy="45.5" rx="4.5" ry="8.4" fill="#050b21" />
                <ellipse className="sb-eye" cx="67" cy="45.5" rx="4.5" ry="8.4" fill="#050b21" />
                <ellipse className="sb-glint" cx="47.6" cy="41.4" rx="1.4" ry="2.4" fill="#e0f2fe" opacity="0.92" />
                <ellipse className="sb-glint" cx="65.6" cy="41.4" rx="1.4" ry="2.4" fill="#e0f2fe" opacity="0.92" />
                <circle cx="50.4" cy="49.5" r="0.9" fill="#93c5fd" opacity="0.55" />
                <circle cx="68.4" cy="49.5" r="0.9" fill="#93c5fd" opacity="0.55" />
              </g>
            )}
          </g>

          {/* Rounded paws gripping the top chrome rim of the chat box */}
          <g className="sb-paws">
            <path
              className="sb-paw sb-paw-l"
              d="M23 67.5 C22.5 59.5 30.2 55 38.2 56.5 C43.4 57.5 46.4 61.2 45.8 65.4 C45.2 68.4 29 70.2 23 67.5 Z"
              fill={`url(#sbp-paw-${uid})`}
              stroke="#60a5fa"
              strokeWidth="1.05"
              strokeOpacity="0.62"
            />
            <path
              className="sb-paw sb-paw-r"
              d="M93 67.5 C93.5 59.5 85.8 55 77.8 56.5 C72.6 57.5 69.6 61.2 70.2 65.4 C70.8 68.4 87 70.2 93 67.5 Z"
              fill={`url(#sbp-paw-${uid})`}
              stroke="#60a5fa"
              strokeWidth="1.05"
              strokeOpacity="0.62"
            />
          </g>
        </svg>
      </button>
    );
  }

  const px = PIXELS[size];

  return (
    <span
      className={`starbuddy starbuddy-${size} mood-${mood}${poked ? " poked" : ""}${className ? " " + className : ""}`}
      style={{ width: px, height: px }}
      title={title}
      aria-hidden={title ? undefined : true}
      onClick={interactive ? triggerPoke : undefined}
    >
      <svg viewBox="0 0 120 120" width={px} height={px} fill="none" xmlns="http://www.w3.org/2000/svg">
        <defs>
          <radialGradient id={`sb-sky-${uid}`} cx="50%" cy="58%" r="72%">
            <stop offset="0%" stopColor="#142c7e" />
            <stop offset="55%" stopColor="#081342" />
            <stop offset="100%" stopColor="#040824" />
          </radialGradient>
          <radialGradient id={`sb-halo-${uid}`} cx="50%" cy="54%" r="45%">
            <stop offset="0%" stopColor={mood === "asking" ? "#fbbf24" : "#60a5fa"} stopOpacity="0.94" />
            <stop offset="55%" stopColor={mood === "asking" ? "#f59e0b" : "#2563eb"} stopOpacity="0.46" />
            <stop offset="100%" stopColor="#1d4ed8" stopOpacity="0" />
          </radialGradient>
          <radialGradient id={`sb-body-${uid}`} cx="44%" cy="32%" r="68%">
            <stop offset="0%" stopColor="#ffffff" />
            <stop offset="56%" stopColor="#eef5ff" />
            <stop offset="88%" stopColor="#bad6ff" />
            <stop offset="100%" stopColor="#89b4ff" />
          </radialGradient>
          <linearGradient id={`sb-paw-${uid}`} x1="0" y1="70" x2="0" y2="92" gradientUnits="userSpaceOnUse">
            <stop offset="0%" stopColor="#ffffff" />
            <stop offset="65%" stopColor="#dbeafe" />
            <stop offset="100%" stopColor="#8ab4ff" />
          </linearGradient>
          <radialGradient id={`sb-planet-${uid}`} cx="50%" cy="98%" r="65%">
            <stop offset="0%" stopColor="#050b24" />
            <stop offset="72%" stopColor="#091545" />
            <stop offset="100%" stopColor="#1e3a8a" />
          </radialGradient>
          <linearGradient id={`sb-rim-${uid}`} x1="6" y1="84" x2="114" y2="84" gradientUnits="userSpaceOnUse">
            <stop offset="0%" stopColor="#3b82f6" stopOpacity="0.35" />
            <stop offset="25%" stopColor="#93c5fd" />
            <stop offset="50%" stopColor="#eff6ff" />
            <stop offset="75%" stopColor="#93c5fd" />
            <stop offset="100%" stopColor="#3b82f6" stopOpacity="0.35" />
          </linearGradient>
          <clipPath id={`sb-clip-${uid}`}>
            <rect x="4" y="4" width="112" height="112" rx="28" />
          </clipPath>
        </defs>

        <g clipPath={`url(#sb-clip-${uid})`}>
          {/* Deep midnight-indigo cosmic sky */}
          <rect x="4" y="4" width="112" height="112" rx="28" fill={`url(#sb-sky-${uid})`} />

          {/* Twinkling starfield */}
          <g className="sb-stars">
            <circle className="sb-s1" cx="24" cy="24" r="1.35" fill="#e0f2fe" />
            <circle className="sb-s2" cx="94" cy="20" r="1.5" fill="#ffffff" />
            <circle className="sb-s3" cx="53" cy="15" r="1.8" fill="#bae6fd" />
            <circle className="sb-s1" cx="82" cy="33" r="1.15" fill="#93c5fd" />
            <circle className="sb-s2" cx="34" cy="39" r="1.1" fill="#bfdbfe" />
            <circle className="sb-s3" cx="18" cy="54" r="1.2" fill="#93c5fd" opacity="0.75" />
            <circle className="sb-s1" cx="101" cy="48" r="1.25" fill="#dbeafe" opacity="0.8" />
          </g>

          {/* Back sapphire aura behind the star character */}
          <ellipse className="sb-aura" cx="60" cy="61" rx="45" ry="39" fill={`url(#sb-halo-${uid})`} />

          {/* Living Star character peeking from behind the planet horizon */}
          <g className="sb-character">
            <path
              className="sb-star-body"
              d="M60 25 C65 25 69.5 34 73.5 41.5 C76 46 80 47.5 86 48.2 C93.5 49 97 53 94.5 59.5 C92.5 64.5 87.5 69.5 85 75.5 C83.5 79 83 84 82 88 L38 88 C37 84 36.5 79 35 75.5 C32.5 69.5 27.5 64.5 25.5 59.5 C23 53 26.5 49 34 48.2 C40 47.5 44 46 46.5 41.5 C50.5 34 55 25 60 25 Z"
              fill={`url(#sb-body-${uid})`}
              stroke="#7dd3fc"
              strokeWidth="1.35"
              strokeOpacity="0.68"
            />

            {/* Expressive dark vertical oval eyes with specular highlights */}
            <g className="sb-eyes">
              <ellipse className="sb-eye" cx="51" cy="60.5" rx="4.4" ry="8.2" fill="#050b21" />
              <ellipse className="sb-eye" cx="69" cy="60.5" rx="4.4" ry="8.2" fill="#050b21" />
              <ellipse className="sb-glint" cx="49.7" cy="56.5" rx="1.35" ry="2.2" fill="#e0f2fe" opacity="0.9" />
              <ellipse className="sb-glint" cx="67.7" cy="56.5" rx="1.35" ry="2.2" fill="#e0f2fe" opacity="0.9" />
            </g>
          </g>

          {/* Planet silhouette & glowing cobalt horizon arc */}
          <path d="M -8 96 Q 60 60 128 96 L 128 124 L -8 124 Z" fill={`url(#sb-planet-${uid})`} />
          <path className="sb-horizon" d="M -6 95 Q 60 61 126 95" stroke={`url(#sb-rim-${uid})`} strokeWidth="2.8" strokeLinecap="round" />

          {/* Rounded paws resting on the planet rim */}
          <g className="sb-paws">
            <path
              className="sb-paw sb-paw-l"
              d="M27 83.5 C26.5 76 34 71.5 41.5 73 C46.5 74 49.5 77.5 49 81.5 C48.5 84.2 33 86.2 27 83.5 Z"
              fill={`url(#sb-paw-${uid})`}
              stroke="#60a5fa"
              strokeWidth="0.9"
              strokeOpacity="0.55"
            />
            <path
              className="sb-paw sb-paw-r"
              d="M93 83.5 C93.5 76 86 71.5 78.5 73 C73.5 74 70.5 77.5 71 81.5 C71.5 84.2 87 86.2 93 83.5 Z"
              fill={`url(#sb-paw-${uid})`}
              stroke="#60a5fa"
              strokeWidth="0.9"
              strokeOpacity="0.55"
            />
          </g>
        </g>

        {/* Subtle outer squircle glass rim */}
        <rect x="4.75" y="4.75" width="110.5" height="110.5" rx="27.25" stroke="#60a5fa" strokeOpacity="0.4" strokeWidth="1.5" />
      </svg>
    </span>
  );
});
