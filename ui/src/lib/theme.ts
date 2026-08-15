import { readPref, THEME_KEY, writePref } from "./prefs";

export type ThemePref = "light" | "dark" | "system";

/**
 * Applies a theme preference.
 *
 * "system" REMOVES the stamp rather than resolving it to light or dark. The
 * CSS carries a prefers-color-scheme block for exactly this case, so an
 * unstamped root follows the OS live -- resolving it here would freeze the
 * page at whatever the OS said on load.
 *
 * The store write goes through writePref: this runs at module scope from
 * main.tsx before createRoot, where a bare localStorage throw -- Safari with
 * cookies blocked -- blanked the entire app rather than losing a theme.
 */
export function applyTheme(pref: ThemePref): void {
  if (pref === "system") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.dataset.theme = pref;
  writePref(THEME_KEY, pref);
}

export function loadTheme(): ThemePref {
  const v = readPref(THEME_KEY);
  return v === "light" || v === "dark" ? v : "system";
}
