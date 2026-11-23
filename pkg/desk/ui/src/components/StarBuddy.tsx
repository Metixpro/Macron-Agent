import { memo, useId } from "react";
import { useStore } from "../store";

export type StarBuddyMood = "idle" | "working" | "streaming" | "asking" | "offline";
export type StarBuddySize = "sm" | "md" | "lg" | "xl";

const PIXELS: Record<StarBuddySize, number> = {
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
  className = "",
  title,
}: {
  mood?: StarBuddyMood;
  size?: StarBuddySize;
  className?: string;
  title?: string;
}) {
  const autoMood = useStarBuddyMood();
  const mood = explicitMood || autoMood;
  const uid = useId().replace(/:/g, "");
  const px = PIXELS[size];

  return (
    <span
      className={`starbuddy starbuddy-${size} mood-${mood}${className ? " " + className : ""}`}
      style={{ width: px, height: px }}
      title={title}
      aria-hidden={title ? undefined : true}
    >
      <svg viewBox="0 0 120 120" width={px} height={px} fill="none" xmlns="http://www.w3.org/2000/svg">
        <defs>
          <radialGradient id={`sb-sky-${uid}`} cx="50%" cy="58%" r="72%">
            <stop offset="0%" stopColor="#142c7e" />
            <stop offset="55%" stopColor="#081342" />
            <stop offset="100%" stopColor="#040824" />
          </radialGradient>
          <radialGradient id={`sb-halo-${uid}`} cx="50%" cy="54%" r="44%">
            <stop offset="0%" stopColor={mood === "asking" ? "#fbbf24" : "#60a5fa"} stopOpacity="0.92" />
            <stop offset="55%" stopColor={mood === "asking" ? "#f59e0b" : "#2563eb"} stopOpacity="0.42" />
            <stop offset="100%" stopColor="#1d4ed8" stopOpacity="0" />
          </radialGradient>
          <linearGradient id={`sb-body-${uid}`} x1="36" y1="26" x2="84" y2="88" gradientUnits="userSpaceOnUse">
            <stop offset="0%" stopColor="#ffffff" />
            <stop offset="58%" stopColor="#eef5ff" />
            <stop offset="100%" stopColor="#9ec2ff" />
          </linearGradient>
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
          <ellipse className="sb-aura" cx="60" cy="61" rx="44" ry="38" fill={`url(#sb-halo-${uid})`} />

          {/* Living Star character peeking from behind the planet horizon */}
          <g className="sb-character">
            {/* Rounded 5-point star silhouette (top crown + two side arms) */}
            <path
              className="sb-star-body"
              d="M60 25 C65 25 69.5 34 73.5 41.5 C76 46 80 47.5 86 48.2 C93.5 49 97 53 94.5 59.5 C92.5 64.5 87.5 69.5 85 75.5 C83.5 79 83 84 82 88 L38 88 C37 84 36.5 79 35 75.5 C32.5 69.5 27.5 64.5 25.5 59.5 C23 53 26.5 49 34 48.2 C40 47.5 44 46 46.5 41.5 C50.5 34 55 25 60 25 Z"
              fill={`url(#sb-body-${uid})`}
              stroke="#7dd3fc"
              strokeWidth="1.35"
              strokeOpacity="0.65"
            />

            {/* Expressive dark vertical oval eyes with specular highlights */}
            <g className="sb-eyes">
              <ellipse className="sb-eye" cx="51" cy="60.5" rx="4.4" ry="8.2" fill="#050b21" />
              <ellipse className="sb-eye" cx="69" cy="60.5" rx="4.4" ry="8.2" fill="#050b21" />
              <ellipse className="sb-glint" cx="49.7" cy="56.5" rx="1.35" ry="2.2" fill="#e0f2fe" opacity="0.88" />
              <ellipse className="sb-glint" cx="67.7" cy="56.5" rx="1.35" ry="2.2" fill="#e0f2fe" opacity="0.88" />
            </g>
          </g>

          {/* Planet silhouette & glowing cobalt horizon arc */}
          <path
            d="M -8 96 Q 60 60 128 96 L 128 124 L -8 124 Z"
            fill={`url(#sb-planet-${uid})`}
          />
          <path
            className="sb-horizon"
            d="M -6 95 Q 60 61 126 95"
            stroke={`url(#sb-rim-${uid})`}
            strokeWidth="2.8"
            strokeLinecap="round"
          />

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
        <rect
          x="4.75"
          y="4.75"
          width="110.5"
          height="110.5"
          rx="27.25"
          stroke="#60a5fa"
          strokeOpacity="0.38"
          strokeWidth="1.5"
        />
      </svg>
    </span>
  );
});
