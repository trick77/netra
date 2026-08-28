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
 * -- and these reads run inside useState initialisers. An unguarded throw
 * there renders nothing at all. Losing a preference is acceptable; losing
 * the page is not.
 *
 * This pair existed three times over before it lived here (SettingsPage's
 * stored/remember, the fleet's own stored-preference read, and a third copy
 * inlined in a click handler), each with its own copy of the comment above.
 */

/** Every key this app writes, in one place so a browser's netra
 * preferences are greppable in devtools as a group. */
export const RANGE_KEY = "netra.range";

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
