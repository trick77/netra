/**
 * The per-browser preferences, and the one guarded way to read and write
 * them.
 *
 * Every one of these is a preference rather than data: none of it reaches
 * the hub, nothing here can fail in a way worth surfacing, and the default
 * is always a perfectly good answer. That is what justifies swallowing the
 * errors below.
 *
 * The guard is not defensive habit. localStorage THROWS outright in Safari
 * with cookies blocked and in some private modes -- not returns null, throws
 * -- and these reads run inside useState initialisers and, for the theme, at
 * module scope before createRoot. An unguarded throw there renders nothing
 * at all. Losing a preference is acceptable; losing the page is not.
 *
 * This pair existed three times over before it lived here (SettingsPage's
 * stored/remember, FleetPage's readStoredDensity, and a third copy inlined
 * in a click handler), each with its own copy of the comment above and
 * lib/theme.ts with no guard at all.
 */

/** Every key this app writes, in one place so a browser's netra
 * preferences are greppable in devtools as a group. */
export const THEME_KEY = "netra.theme";
export const RANGE_KEY = "netra.range";
/** Density and Settings' "overview view" are ONE preference under one key.
 * They were once two -- Settings wrote "netra.view" while the fleet read
 * "netra.fleet.density" -- and choosing Cards in Settings therefore changed
 * nothing at all: the overview came back as a table on every reload. */
export const DENSITY_KEY = "netra.fleet.density";

export function readPref(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writePref(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // See readPref: an unavailable store costs the preference, never the
    // page and never the click.
  }
}
