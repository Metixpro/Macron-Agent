import { memo, useEffect, useId, useRef, useState } from "react";
import { useStore } from "../store";

export type StarBuddyMood =
  | "idle"
  | "typing"
  | "working"
  | "streaming"
  | "asking"
  | "success"
  | "error"
  | "offline"
  | "speaking";

export type StarBuddySize = "xs" | "sm" | "md" | "lg" | "xl";
export type StarBuddyVariant = "card" | "peek" | "stage";

const PIXELS: Record<StarBuddySize, number> = {
  xs: 22,
  sm: 28,
  md: 42,
  lg: 76,
  xl: 104,
};

function prefersReducedMotion(motionPref: string): boolean {
  if (motionPref === "reduced") return true;
  if (typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) return true;
  return false;
}

/**
 * Derives StarBuddy's live behavioral mood strictly from real Macron engine
 * events, connection state, and composer activity. Never fabricates activity.
 */
export function useStarBuddyMood(): StarBuddyMood {
  const online = useStore((s) => s.online);
  const running = useStore((s) => s.live.running);
  const streaming = useStore((s) => Boolean(s.live.html));
  const asking = useStore((s) => Boolean(s.live.ask));
  const errorAlert = useStore((s) => s.errorAlert);
  const composerTyping = useStore((s) => s.composerTyping);
  const lastTurnResult = useStore((s) => s.lastTurnResult);
  const audioLevel = useStore((s) => s.audioLevel);

  const [celebrating, setCelebrating] = useState<"success" | "error" | null>(null);

  useEffect(() => {
    if (!lastTurnResult) return;
    const age = Date.now() - lastTurnResult.at;
    if (age > 2800) return;
    setCelebrating(lastTurnResult.ok ? "success" : "error");
    const timer = window.setTimeout(() => setCelebrating(null), Math.max(200, 2800 - age));
    return () => window.clearTimeout(timer);
  }, [lastTurnResult]);

  if (online === false) return "offline";
  if (asking) return "asking";
  if (audioLevel > 0.02) return "speaking";
  if (streaming) return "streaming";
  if (running) return "working";
  if (errorAlert) return "error";
  if (celebrating) return celebrating;
  if (composerTyping) return "typing";
  return "idle";
}

function moodPalette(mood: StarBuddyMood) {
  switch (mood) {
    case "asking":
      return { aura: "#f59e0b", rim: "#fde68a", star: "#fef3c7" };
    case "success":
      return { aura: "#34d399", rim: "#a7f3d0", star: "#d1fae5" };
    case "error":
      return { aura: "#f87171", rim: "#fecaca", star: "#fee2e2" };
    case "offline":
      return { aura: "#64748b", rim: "#94a3b8", star: "#cbd5e1" };
    case "speaking":
      return { aura: "#7dd3fc", rim: "#bae6fd", star: "#e0f2fe" };
    case "streaming":
      return { aura: "#60a5fa", rim: "#cbd4eb", star: "#e0f2fe" };
    case "working":
      return { aura: "#60a5fa", rim: "#bfdbfe", star: "#dbeafe" };
    default:
      return { aura: "#7c9cf5", rim: "#cbd4eb", star: "#e8eefc" };
  }
}

/**
 * Spring-damped eye tracking & natural randomized blinking hook.
 * Automatically pauses when document is hidden or reduced-motion is enabled.
 */
function useLivingMotion(
  ref: React.RefObject<HTMLElement | null>,
  enabled: boolean,
  mood: StarBuddyMood,
  caretRatio: number,
) {
  const [gaze, setGaze] = useState<{ x: number; y: number }>({ x: 0, y: 0 });
  const [blinking, setBlinking] = useState(false);
  const [hidden, setHidden] = useState(() => (typeof document !== "undefined" ? document.hidden : false));

  useEffect(() => {
    const onVis = () => setHidden(document.hidden);
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, []);

  // Organic randomized blinking (non-mechanical)
  useEffect(() => {
    if (!enabled || hidden || mood === "offline" || mood === "success") {
      setBlinking(false);
      return;
    }
    let alive = true;
    let t1 = 0;
    let t2 = 0;
    const schedule = () => {
      const wait = 2600 + Math.random() * 4400;
      t1 = window.setTimeout(() => {
        if (!alive) return;
        setBlinking(true);
        t2 = window.setTimeout(() => {
          if (!alive) return;
          setBlinking(false);
          // Occasional double-blink for lifelike charm
          if (Math.random() < 0.18) {
            t1 = window.setTimeout(() => {
              if (!alive) return;
              setBlinking(true);
              t2 = window.setTimeout(() => {
                if (!alive) return;
                setBlinking(false);
                schedule();
              }, 110);
            }, 130);
          } else {
            schedule();
          }
        }, 125);
      }, wait);
    };
    schedule();
    return () => {
      alive = false;
      window.clearTimeout(t1);
      window.clearTimeout(t2);
    };
  }, [enabled, hidden, mood]);

  // Smooth spring-damped pointer eye tracking (self-pausing rAF)
  useEffect(() => {
    if (!enabled || hidden || mood === "offline") {
      setGaze({ x: 0, y: 0 });
      return;
    }
    let targetX = 0;
    let targetY = 0;
    let curX = 0;
    let curY = 0;
    let velX = 0;
    let velY = 0;
    let raf = 0;

    if (mood === "typing") {
      targetX = (caretRatio - 0.5) * 4.4;
      targetY = 2.6;
    }

    const step = () => {
      const stiffness = 0.14;
      const damping = 0.72;
      velX = (velX + (targetX - curX) * stiffness) * damping;
      velY = (velY + (targetY - curY) * stiffness) * damping;
      curX += velX;
      curY += velY;
      setGaze({ x: Math.round(curX * 100) / 100, y: Math.round(curY * 100) / 100 });
      if (Math.abs(targetX - curX) > 0.03 || Math.abs(targetY - curY) > 0.03 || Math.abs(velX) > 0.02 || Math.abs(velY) > 0.02) {
        raf = window.requestAnimationFrame(step);
      } else {
        raf = 0;
      }
    };

    const kick = () => {
      if (!raf) raf = window.requestAnimationFrame(step);
    };

    const onMove = (e: PointerEvent) => {
      const el = ref.current;
      if (!el) return;
      const rect = el.getBoundingClientRect();
      if (!rect.width || !rect.height) return;
      const cx = rect.left + rect.width / 2;
      const cy = rect.top + rect.height / 2;
      const dx = e.clientX - cx;
      const dy = e.clientY - cy;
      const dist = Math.hypot(dx, dy);
      if (dist > 460) {
        targetX = mood === "typing" ? (caretRatio - 0.5) * 4.4 : 0;
        targetY = mood === "typing" ? 2.6 : 0;
      } else {
        const mag = Math.min(1, dist / 180);
        const angle = Math.atan2(dy, dx);
        targetX = Math.cos(angle) * 3.8 * mag;
        targetY = Math.sin(angle) * 2.8 * mag;
      }
      kick();
    };

    const onLeave = () => {
      targetX = mood === "typing" ? (caretRatio - 0.5) * 4.4 : 0;
      targetY = mood === "typing" ? 2.6 : 0;
      kick();
    };

    kick();
    window.addEventListener("pointermove", onMove, { passive: true });
    document.addEventListener("pointerleave", onLeave);
    return () => {
      window.removeEventListener("pointermove", onMove);
      document.removeEventListener("pointerleave", onLeave);
      if (raf) window.cancelAnimationFrame(raf);
    };
  }, [enabled, hidden, mood, caretRatio, ref]);

  return { gaze, blinking, hidden };
}

function FacialExpression({
  mood,
  poked,
  blinking,
  gaze,
  audioLevel,
  cxL,
  cxR,
  cy,
}: {
  mood: StarBuddyMood;
  poked: boolean;
  blinking: boolean;
  gaze: { x: number; y: number };
  audioLevel: number;
  cxL: number;
  cxR: number;
  cy: number;
}) {
  const midX = (cxL + cxR) / 2;
  const happy = poked || mood === "success";
  const sleepy = mood === "offline";
  const focused = mood === "working" || mood === "streaming";
  const worried = mood === "error";
  const curious = mood === "asking";
  const speaking = mood === "speaking" || audioLevel > 0.02;

  const gx = sleepy ? 0 : gaze.x;
  const gy = sleepy ? 0 : gaze.y;

  const mouthOpen = speaking ? Math.max(2.2, Math.min(7.5, 2.2 + audioLevel * 6.5)) : 0;

  return (
    <g className="sb-face" style={{ transform: `translate(${gx * 0.65}px, ${gy * 0.65}px)` }}>
      {/* Soft warm cheeks for happy/success/poked states */}
      {(happy || mood === "idle" || mood === "typing") && (
        <g className="sb-cheeks" opacity={happy ? 0.72 : 0.28}>
          <ellipse cx={cxL - 7.5} cy={cy + 6.5} rx="4.2" ry="2.2" fill={happy ? "#fda4af" : "#93c5fd"} />
          <ellipse cx={cxR + 7.5} cy={cy + 6.5} rx="4.2" ry="2.2" fill={happy ? "#fda4af" : "#93c5fd"} />
        </g>
      )}

      {/* Eyes */}
      {happy ? (
        <g className="sb-eyes-happy">
          <path d={`M${cxL - 5} ${cy + 1} Q${cxL} ${cy - 6.5} ${cxL + 5} ${cy + 1}`} stroke="#0b1021" strokeWidth="3.1" strokeLinecap="round" fill="none" />
          <path d={`M${cxR - 5} ${cy + 1} Q${cxR} ${cy - 6.5} ${cxR + 5} ${cy + 1}`} stroke="#0b1021" strokeWidth="3.1" strokeLinecap="round" fill="none" />
        </g>
      ) : sleepy ? (
        <g className="sb-eyes-sleep">
          <path d={`M${cxL - 4.5} ${cy + 1} Q${cxL} ${cy + 5.2} ${cxL + 4.5} ${cy + 1}`} stroke="#1e293b" strokeWidth="2.6" strokeLinecap="round" fill="none" />
          <path d={`M${cxR - 4.5} ${cy + 1} Q${cxR} ${cy + 5.2} ${cxR + 4.5} ${cy + 1}`} stroke="#1e293b" strokeWidth="2.6" strokeLinecap="round" fill="none" />
        </g>
      ) : (
        <g
          className={"sb-eyes" + (blinking ? " is-blinking" : "")}
          style={{
            transform: `translate(${gx * 0.55}px, ${gy * 0.55}px) scaleY(${blinking ? 0.1 : focused ? 0.86 : 1})`,
            transformOrigin: `${midX}px ${cy}px`,
          }}
        >
          <ellipse cx={cxL} cy={cy} rx={curious ? 4.6 : 4.2} ry={curious ? 8.3 : 7.8} fill="#090e20" />
          <ellipse cx={cxR} cy={cy} rx={curious ? 4.8 : 4.2} ry={curious ? 8.7 : 7.8} fill="#090e20" />
          {/* Specular highlights track slightly faster for depth */}
          <ellipse cx={cxL - 1.3 + gx * 0.18} cy={cy - 3.6 + gy * 0.16} rx="1.45" ry="2.25" fill="#f0f7ff" opacity="0.95" />
          <ellipse cx={cxR - 1.3 + gx * 0.18} cy={cy - 3.6 + gy * 0.16} rx="1.45" ry="2.25" fill="#f0f7ff" opacity="0.95" />
          <circle cx={cxL + 1.5} cy={cy + 3.6} r="0.95" fill="#93c5fd" opacity="0.58" />
          <circle cx={cxR + 1.5} cy={cy + 3.6} r="0.95" fill="#93c5fd" opacity="0.58" />
        </g>
      )}

      {/* Expressive micro-brows for focused / approval / error */}
      {focused && !blinking && (
        <g className="sb-brows" stroke="#0f172a" strokeWidth="2.1" strokeLinecap="round" opacity="0.72">
          <path d={`M${cxL - 4.5} ${cy - 9.8} L${cxL + 3.5} ${cy - 8.4}`} />
          <path d={`M${cxR + 4.5} ${cy - 9.8} L${cxR - 3.5} ${cy - 8.4}`} />
        </g>
      )}
      {curious && !blinking && (
        <g className="sb-brows" stroke="#1e293b" strokeWidth="2.1" strokeLinecap="round" opacity="0.78">
          <path d={`M${cxL - 4.2} ${cy - 10} Q${cxL} ${cy - 11.4} ${cxL + 4} ${cy - 9.8}`} fill="none" />
          <path d={`M${cxR - 4} ${cy - 11.8} Q${cxR} ${cy - 14.4} ${cxR + 4.5} ${cy - 11.2}`} fill="none" />
        </g>
      )}
      {worried && !blinking && (
        <g className="sb-brows" stroke="#1e293b" strokeWidth="2.1" strokeLinecap="round" opacity="0.82">
          <path d={`M${cxL - 4.5} ${cy - 8.5} L${cxL + 3.5} ${cy - 10.6}`} />
          <path d={`M${cxR + 4.5} ${cy - 8.5} L${cxR - 3.5} ${cy - 10.6}`} />
        </g>
      )}

      {/* Mouth expressions: speaking lip-sync, success smile, error concern, curious o */}
      {speaking ? (
        <ellipse className="sb-mouth speaking" cx={midX} cy={cy + 9.2} rx={3.6 + audioLevel * 1.4} ry={mouthOpen * 0.55} fill="#090e20" />
      ) : happy ? (
        <path className="sb-mouth" d={`M${midX - 4.2} ${cy + 8.2} Q${midX} ${cy + 12.6} ${midX + 4.2} ${cy + 8.2}`} stroke="#0b1021" strokeWidth="2.2" strokeLinecap="round" fill="none" />
      ) : worried ? (
        <path className="sb-mouth" d={`M${midX - 3.6} ${cy + 10.8} Q${midX} ${cy + 7.8} ${midX + 3.6} ${cy + 10.8}`} stroke="#0b1021" strokeWidth="2.1" strokeLinecap="round" fill="none" />
      ) : curious ? (
        <circle className="sb-mouth" cx={midX} cy={cy + 9.6} r="2.1" fill="#0b1021" opacity="0.82" />
      ) : null}
    </g>
  );
}

export const StarBuddy = memo(function StarBuddy({
  mood: explicitMood,
  size = "md",
  variant = "card",
  tucked = false,
  interactive = false,
  trackPointer = false,
  audioLevel: explicitAudio,
  className = "",
  title,
  onClick,
}: {
  mood?: StarBuddyMood;
  size?: StarBuddySize;
  variant?: StarBuddyVariant;
  tucked?: boolean;
  interactive?: boolean;
  trackPointer?: boolean;
  audioLevel?: number;
  className?: string;
  title?: string;
  onClick?: () => void;
}) {
  const autoMood = useStarBuddyMood();
  const mood = explicitMood || autoMood;
  const motionPref = useStore((s) => s.motion);
  const caretRatio = useStore((s) => s.composerCaretRatio);
  const storeAudio = useStore((s) => s.audioLevel);
  const streamLen = useStore((s) => (s.live.html ? s.live.html.length : 0));
  const audioLevel = explicitAudio ?? storeAudio;
  const reduced = prefersReducedMotion(motionPref);

  const uid = useId().replace(/:/g, "");
  const hostRef = useRef<HTMLElement | null>(null);
  const [poked, setPoked] = useState(false);
  const [pulseTick, setPulseTick] = useState(false);
  const timerRef = useRef<number | null>(null);

  const enableTracking = !reduced && (trackPointer || variant === "peek" || variant === "stage");
  const { gaze, blinking, hidden } = useLivingMotion(hostRef, enableTracking, mood, caretRatio);

  // Subtle rhythm pulse when real streaming chunks arrive
  useEffect(() => {
    if (mood !== "streaming" || !streamLen || reduced) return;
    setPulseTick((p) => !p);
  }, [streamLen, mood, reduced]);

  useEffect(() => {
    return () => {
      if (timerRef.current !== null) window.clearTimeout(timerRef.current);
    };
  }, []);

  const triggerPoke = () => {
    onClick?.();
    if (!interactive) return;
    setPoked(true);
    if (timerRef.current !== null) window.clearTimeout(timerRef.current);
    timerRef.current = window.setTimeout(() => setPoked(false), 740);
  };

  const pal = moodPalette(mood);
  const pausedClass = hidden || reduced ? " sb-paused" : "";
  const streamPulseClass = mood === "streaming" && pulseTick ? " sb-pulse" : "";

  /* ── PEEK VARIANT (perched over Composer top rim) ── */
  if (variant === "peek") {
    return (
      <button
        ref={(el) => {
          hostRef.current = el;
        }}
        type="button"
        className={`starbuddy-peek mood-${mood}${tucked ? " tucked" : ""}${poked ? " poked" : ""}${pausedClass}${streamPulseClass}${className ? " " + className : ""}`}
        title={title || "Macron StarBuddy"}
        aria-label={title || "Macron StarBuddy"}
        tabIndex={tucked ? -1 : undefined}
        aria-hidden={tucked || undefined}
        onClick={triggerPoke}
      >
        <svg viewBox="0 0 120 76" width="68" height="44" fill="none" xmlns="http://www.w3.org/2000/svg">
          <defs>
            <radialGradient id={`sbp-halo-${uid}`} cx="50%" cy="62%" r="50%">
              <stop offset="0%" stopColor={pal.aura} stopOpacity="0.62" />
              <stop offset="55%" stopColor={pal.aura} stopOpacity="0.22" />
              <stop offset="100%" stopColor={pal.aura} stopOpacity="0" />
            </radialGradient>
            <radialGradient id={`sbp-body-${uid}`} cx="42%" cy="28%" r="72%">
              <stop offset="0%" stopColor="#ffffff" />
              <stop offset="52%" stopColor="#f3f6fc" />
              <stop offset="84%" stopColor="#d4dff5" />
              <stop offset="100%" stopColor="#a9bde8" />
            </radialGradient>
            <linearGradient id={`sbp-paw-${uid}`} x1="0" y1="54" x2="0" y2="72" gradientUnits="userSpaceOnUse">
              <stop offset="0%" stopColor="#ffffff" />
              <stop offset="68%" stopColor="#e2eaf9" />
              <stop offset="100%" stopColor="#aec2eb" />
            </linearGradient>
          </defs>

          {/* Ambient sparkles + state micro-glyphs */}
          <g className="sb-stars">
            <path className="sb-s1" d="M16 18 L17.6 13.8 L19.2 18 L23.4 19.6 L19.2 21.2 L17.6 25.4 L16 21.2 L11.8 19.6 Z" fill={pal.star} />
            <circle className="sb-s2" cx="102" cy="17" r="2.1" fill={pal.star} />
            <circle className="sb-s3" cx="92" cy="9" r="1.4" fill={pal.rim} />
            <circle className="sb-s1" cx="30" cy="9.5" r="1.3" fill={pal.rim} />
          </g>

          {mood === "offline" && (
            <g className="sb-zzz" fill={pal.rim} fontFamily="var(--font)" fontWeight="600">
              <text x="86" y="22" fontSize="10" className="sb-z1">z</text>
              <text x="95" y="13" fontSize="13" className="sb-z2">Z</text>
            </g>
          )}
          {mood === "asking" && (
            <text x="90" y="21" fontSize="14" fontWeight="700" fill="#fcd34d" className="sb-qmark">?</text>
          )}

          {/* Soft aura */}
          <ellipse className="sb-aura" cx="60" cy="48" rx="45" ry="26" fill={`url(#sbp-halo-${uid})`} />

          {/* Plush star body */}
          <g className="sb-character">
            <path
              className="sb-star-body"
              d="M60 10 C65.2 10 69.8 18.8 73.6 26.2 C76.2 30.8 80.4 32.3 86.6 33 C94.4 33.8 98 38 95.4 44.6 C93.2 50 88.4 55 86 60.5 C84.7 63.6 84.2 66.2 83.2 68.5 L36.8 68.5 C35.8 66.2 35.3 63.6 34 60.5 C31.6 55 26.8 50 24.6 44.6 C22 38 25.6 33.8 33.4 33 C39.6 32.3 43.8 30.8 46.4 26.2 C50.2 18.8 54.8 10 60 10 Z"
              fill={`url(#sbp-body-${uid})`}
              stroke={pal.rim}
              strokeWidth="1.3"
              strokeOpacity="0.82"
            />
            <FacialExpression mood={mood} poked={poked} blinking={blinking} gaze={gaze} audioLevel={audioLevel} cxL={51} cxR={69} cy={45} />
          </g>

          {/* Rounded paws gripping the composer top chrome */}
          <g className="sb-paws">
            <path
              className="sb-paw sb-paw-l"
              d="M24 68.5 C23.5 60.5 31.2 56 39.2 57.5 C44.4 58.5 47.4 62.2 46.8 66.4 C46.2 69.4 30 71.2 24 68.5 Z"
              fill={`url(#sbp-paw-${uid})`}
              stroke={pal.rim}
              strokeWidth="1.05"
              strokeOpacity="0.68"
            />
            <path
              className="sb-paw sb-paw-r"
              d="M96 68.5 C96.5 60.5 88.8 56 80.8 57.5 C75.6 58.5 72.6 62.2 73.2 66.4 C73.8 69.4 90 71.2 96 68.5 Z"
              fill={`url(#sbp-paw-${uid})`}
              stroke={pal.rim}
              strokeWidth="1.05"
              strokeOpacity="0.68"
            />
          </g>
        </svg>
      </button>
    );
  }

  const px = PIXELS[size];

  /* ── STAGE VARIANT (floating living hero character without boxy card frame) ── */
  if (variant === "stage") {
    return (
      <span
        ref={(el) => {
          hostRef.current = el;
        }}
        role={interactive ? "button" : undefined}
        tabIndex={interactive ? 0 : undefined}
        className={`starbuddy-stage starbuddy-${size} mood-${mood}${poked ? " poked" : ""}${pausedClass}${streamPulseClass}${className ? " " + className : ""}`}
        style={{ width: px, height: px }}
        title={title}
        aria-label={title}
        onClick={interactive ? triggerPoke : onClick}
        onKeyDown={
          interactive
            ? (e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  triggerPoke();
                }
              }
            : undefined
        }
      >
        <svg viewBox="0 0 128 128" width={px} height={px} fill="none" xmlns="http://www.w3.org/2000/svg">
          <defs>
            <radialGradient id={`sbs-halo-${uid}`} cx="50%" cy="54%" r="48%">
              <stop offset="0%" stopColor={pal.aura} stopOpacity="0.54" />
              <stop offset="56%" stopColor={pal.aura} stopOpacity="0.16" />
              <stop offset="100%" stopColor={pal.aura} stopOpacity="0" />
            </radialGradient>
            <radialGradient id={`sbs-body-${uid}`} cx="40%" cy="28%" r="72%">
              <stop offset="0%" stopColor="#ffffff" />
              <stop offset="50%" stopColor="#f3f6fc" />
              <stop offset="82%" stopColor="#d2ddf4" />
              <stop offset="100%" stopColor="#a4bae6" />
            </radialGradient>
            <radialGradient id={`sbs-shadow-${uid}`} cx="50%" cy="50%" r="50%">
              <stop offset="0%" stopColor="#000000" stopOpacity="0.34" />
              <stop offset="100%" stopColor="#000000" stopOpacity="0" />
            </radialGradient>
          </defs>

          {/* Floating shadow beneath character */}
          <ellipse className="sb-ground" cx="64" cy="116" rx="28" ry="5.5" fill={`url(#sbs-shadow-${uid})`} />

          {/* Ambient radiant halo */}
          <circle className="sb-aura" cx="64" cy="64" r="54" fill={`url(#sbs-halo-${uid})`} />

          {/* Floating stardust */}
          <g className="sb-stars">
            <path className="sb-s1" d="M20 28 L21.7 23.4 L23.4 28 L28 29.7 L23.4 31.4 L21.7 36 L20 31.4 L15.4 29.7 Z" fill={pal.star} />
            <path className="sb-s2" d="M104 22 L105.2 18.6 L106.4 22 L109.8 23.2 L106.4 24.4 L105.2 27.8 L104 24.4 L100.6 23.2 Z" fill={pal.star} />
            <circle className="sb-s3" cx="108" cy="84" r="2.2" fill={pal.rim} />
            <circle className="sb-s1" cx="22" cy="88" r="1.8" fill={pal.rim} />
          </g>

          {/* Celebratory sparkle burst when poked or success */}
          {(poked || mood === "success") && (
            <g className="sb-burst" fill={pal.star}>
              <circle cx="34" cy="18" r="2.6" />
              <circle cx="94" cy="18" r="2.4" />
              <circle cx="112" cy="54" r="2.1" />
              <circle cx="16" cy="54" r="2.1" />
            </g>
          )}

          {mood === "offline" && (
            <g className="sb-zzz" fill={pal.rim} fontFamily="var(--font)" fontWeight="600">
              <text x="92" y="34" fontSize="11" className="sb-z1">z</text>
              <text x="103" y="23" fontSize="14" className="sb-z2">Z</text>
            </g>
          )}
          {mood === "asking" && (
            <text x="96" y="31" fontSize="16" fontWeight="700" fill="#fcd34d" className="sb-qmark">?</text>
          )}

          {/* Full 5-point sculpted plush star body */}
          <g className="sb-character">
            <path
              className="sb-star-body"
              d="M64 16 C70 16 74.8 27.2 78.6 35.6 C81.2 41.2 85.4 43.2 92.6 44 C102.2 45.2 106.8 50.6 103.2 58.6 C100.2 65.2 94.2 70.6 91.8 76.6 C89.8 81.8 92.4 89.2 92.8 96.4 C93.2 104.2 87.2 108.4 79.8 104.6 C74.2 101.8 68.8 97.6 64 97.6 C59.2 97.6 53.8 101.8 48.2 104.6 C40.8 108.4 34.8 104.2 35.2 96.4 C35.6 89.2 38.2 81.8 36.2 76.6 C33.8 70.6 27.8 65.2 24.8 58.6 C21.2 50.6 25.8 45.2 35.4 44 C42.6 43.2 46.8 41.2 49.4 35.6 C53.2 27.2 58 16 64 16 Z"
              fill={`url(#sbs-body-${uid})`}
              stroke={pal.rim}
              strokeWidth="1.5"
              strokeOpacity="0.88"
            />
            {/* Top specular rim highlight */}
            <path
              d="M55 28 C58.5 21 63 19.5 67 24"
              stroke="#ffffff"
              strokeWidth="2.4"
              strokeLinecap="round"
              opacity="0.78"
            />
            <FacialExpression mood={mood} poked={poked} blinking={blinking} gaze={gaze} audioLevel={audioLevel} cxL={54.5} cxR={73.5} cy={61.5} />
          </g>
        </svg>
      </span>
    );
  }

  /* ── CARD / EMBLEM VARIANT (Sidebar brand mark & compact inline badge) ── */
  return (
    <span
      ref={(el) => {
        hostRef.current = el;
      }}
      className={`starbuddy starbuddy-${size} mood-${mood}${poked ? " poked" : ""}${pausedClass}${streamPulseClass}${className ? " " + className : ""}`}
      style={{ width: px, height: px }}
      title={title}
      aria-hidden={title ? undefined : true}
      onClick={interactive ? triggerPoke : onClick}
    >
      <svg viewBox="0 0 120 120" width={px} height={px} fill="none" xmlns="http://www.w3.org/2000/svg">
        <defs>
          <radialGradient id={`sb-sky-${uid}`} cx="50%" cy="58%" r="72%">
            <stop offset="0%" stopColor="#1b264a" />
            <stop offset="58%" stopColor="#12172b" />
            <stop offset="100%" stopColor="#0b0d17" />
          </radialGradient>
          <radialGradient id={`sb-halo-${uid}`} cx="50%" cy="54%" r="45%">
            <stop offset="0%" stopColor={pal.aura} stopOpacity="0.86" />
            <stop offset="56%" stopColor={pal.aura} stopOpacity="0.32" />
            <stop offset="100%" stopColor={pal.aura} stopOpacity="0" />
          </radialGradient>
          <radialGradient id={`sb-body-${uid}`} cx="44%" cy="30%" r="70%">
            <stop offset="0%" stopColor="#ffffff" />
            <stop offset="55%" stopColor="#f2f6fc" />
            <stop offset="88%" stopColor="#cad7f2" />
            <stop offset="100%" stopColor="#9fb5e6" />
          </radialGradient>
          <linearGradient id={`sb-paw-${uid}`} x1="0" y1="70" x2="0" y2="92" gradientUnits="userSpaceOnUse">
            <stop offset="0%" stopColor="#ffffff" />
            <stop offset="65%" stopColor="#e2eaf9" />
            <stop offset="100%" stopColor="#a7bde8" />
          </linearGradient>
          <radialGradient id={`sb-planet-${uid}`} cx="50%" cy="98%" r="65%">
            <stop offset="0%" stopColor="#090c18" />
            <stop offset="72%" stopColor="#101730" />
            <stop offset="100%" stopColor="#253666" />
          </radialGradient>
          <linearGradient id={`sb-rim-${uid}`} x1="6" y1="84" x2="114" y2="84" gradientUnits="userSpaceOnUse">
            <stop offset="0%" stopColor={pal.aura} stopOpacity="0.28" />
            <stop offset="28%" stopColor={pal.rim} />
            <stop offset="50%" stopColor="#ffffff" />
            <stop offset="72%" stopColor={pal.rim} />
            <stop offset="100%" stopColor={pal.aura} stopOpacity="0.28" />
          </linearGradient>
          <clipPath id={`sb-clip-${uid}`}>
            <rect x="4" y="4" width="112" height="112" rx="28" />
          </clipPath>
        </defs>

        <g clipPath={`url(#sb-clip-${uid})`}>
          <rect x="4" y="4" width="112" height="112" rx="28" fill={`url(#sb-sky-${uid})`} />
          <g className="sb-stars">
            <circle className="sb-s1" cx="24" cy="24" r="1.35" fill={pal.star} />
            <circle className="sb-s2" cx="94" cy="20" r="1.5" fill="#ffffff" />
            <circle className="sb-s3" cx="53" cy="15" r="1.7" fill={pal.rim} />
            <circle className="sb-s1" cx="82" cy="33" r="1.15" fill={pal.rim} />
            <circle className="sb-s2" cx="34" cy="39" r="1.1" fill={pal.star} />
          </g>
          <ellipse className="sb-aura" cx="60" cy="61" rx="45" ry="39" fill={`url(#sb-halo-${uid})`} />
          <g className="sb-character">
            <path
              className="sb-star-body"
              d="M60 25 C65 25 69.5 34 73.5 41.5 C76 46 80 47.5 86 48.2 C93.5 49 97 53 94.5 59.5 C92.5 64.5 87.5 69.5 85 75.5 C83.5 79 83 84 82 88 L38 88 C37 84 36.5 79 35 75.5 C32.5 69.5 27.5 64.5 25.5 59.5 C23 53 26.5 49 34 48.2 C40 47.5 44 46 46.5 41.5 C50.5 34 55 25 60 25 Z"
              fill={`url(#sb-body-${uid})`}
              stroke={pal.rim}
              strokeWidth="1.35"
              strokeOpacity="0.72"
            />
            <FacialExpression mood={mood} poked={poked} blinking={blinking} gaze={gaze} audioLevel={audioLevel} cxL={51} cxR={69} cy={60.5} />
          </g>
          <path d="M -8 96 Q 60 60 128 96 L 128 124 L -8 124 Z" fill={`url(#sb-planet-${uid})`} />
          <path className="sb-horizon" d="M -6 95 Q 60 61 126 95" stroke={`url(#sb-rim-${uid})`} strokeWidth="2.6" strokeLinecap="round" />
          <g className="sb-paws">
            <path
              className="sb-paw sb-paw-l"
              d="M27 83.5 C26.5 76 34 71.5 41.5 73 C46.5 74 49.5 77.5 49 81.5 C48.5 84.2 33 86.2 27 83.5 Z"
              fill={`url(#sb-paw-${uid})`}
              stroke={pal.rim}
              strokeWidth="0.9"
              strokeOpacity="0.6"
            />
            <path
              className="sb-paw sb-paw-r"
              d="M93 83.5 C93.5 76 86 71.5 78.5 73 C73.5 74 70.5 77.5 71 81.5 C71.5 84.2 87 86.2 93 83.5 Z"
              fill={`url(#sb-paw-${uid})`}
              stroke={pal.rim}
              strokeWidth="0.9"
              strokeOpacity="0.6"
            />
          </g>
        </g>
        <rect x="4.75" y="4.75" width="110.5" height="110.5" rx="27.25" stroke={pal.rim} strokeOpacity="0.34" strokeWidth="1.4" />
      </svg>
    </span>
  );
});
