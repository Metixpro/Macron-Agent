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

export function getSavedPaletteId(): string {
  return local.get("palette", "");
}

export function clearPaletteOverrides() {
  const root = document.documentElement;
  for (const [, cssVar] of PALETTE_VARS) {
    root.style.removeProperty(cssVar);
  }
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
