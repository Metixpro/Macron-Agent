import { server } from "./api";
import { local } from "./format";
import type { ThemePalette } from "./types";

const PALETTE_VARS: [string, string][] = [
  ["frame", "--frame"],
  ["sidebar", "--side"],
  ["canvas", "--bg"],
  ["surface", "--raised"],
  ["sunk", "--sunk"],
  ["accent", "--accent"],
  ["link", "--link"],
];

let cachedPalettes: ThemePalette[] = [];

function hexLuminance(hex: string): number {
  const clean = hex.replace("#", "").trim();
  if (clean.length !== 6 && clean.length !== 8) return 0.5;
  const r = parseInt(clean.slice(0, 2), 16) / 255;
  const g = parseInt(clean.slice(2, 4), 16) / 255;
  const b = parseInt(clean.slice(4, 6), 16) / 255;
  const lin = (c: number) => (c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4));
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

export function getSavedPaletteId(): string {
  return local.get("palette", "");
}

export function clearPaletteOverrides() {
  const root = document.documentElement;
  for (const [, cssVar] of PALETTE_VARS) {
    root.style.removeProperty(cssVar);
  }
  root.style.removeProperty("--on-accent");
}

export function applyPalette(palette: ThemePalette | null) {
  const root = document.documentElement;
  clearPaletteOverrides();
  if (!palette) {
    local.set("palette", "");
    return;
  }
  local.set("palette", palette.id);
  for (const [key, cssVar] of PALETTE_VARS) {
    const val = palette.colors?.[key];
    if (val) root.style.setProperty(cssVar, val);
  }
  const accent = palette.colors?.accent;
  if (accent) {
    root.style.setProperty("--on-accent", hexLuminance(accent) > 0.42 ? "#141418" : "#F8F8FA");
  }
}

export async function loadPalettes(): Promise<{ themes: ThemePalette[]; dir: string }> {
  try {
    const r = await server.themes();
    cachedPalettes = r.themes || [];
    return { themes: cachedPalettes, dir: r.dir || "" };
  } catch {
    return { themes: cachedPalettes, dir: "" };
  }
}

export async function restoreSavedPalette() {
  const saved = getSavedPaletteId();
  if (!saved) return;
  const { themes } = await loadPalettes();
  const found = themes.find((p) => p.id === saved);
  if (found) applyPalette(found);
}
