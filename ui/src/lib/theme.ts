export type ThemePref = "light" | "dark" | "system";

const KEY = "netra.theme";

/**
 * Applies a theme preference.
 *
 * "system" REMOVES the stamp rather than resolving it to light or dark. The
 * CSS carries a prefers-color-scheme block for exactly this case, so an
 * unstamped root follows the OS live -- resolving it here would freeze the
 * page at whatever the OS said on load.
 */
export function applyTheme(pref: ThemePref): void {
  if (pref === "system") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.dataset.theme = pref;
  localStorage.setItem(KEY, pref);
}

export function loadTheme(): ThemePref {
  const v = localStorage.getItem(KEY);
  return v === "light" || v === "dark" ? v : "system";
}
