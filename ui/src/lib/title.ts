// The browser tab's title, one format for every page.
//
// Product first, then what the page is about, from wide to narrow: "Netra ·
// web01 · Graphs". Several tabs of this app side by side then read apart by
// their host, which is the one thing a tab bar has room for. The middot gets
// an en space each side; a plain space reads cramped at tab-title size.
import { useEffect } from "react";

export const PRODUCT = "Netra";

// U+2002 by code: a literal en space trips eslint no-irregular-whitespace.
const EN_SPACE = String.fromCharCode(0x2002);
export const SEP = `${EN_SPACE}·${EN_SPACE}`;

/** The title for these parts, empty and absent ones dropped. */
export function tabTitle(...parts: (string | null | undefined)[]): string {
  return [PRODUCT, ...parts.filter((p): p is string => !!p)].join(SEP);
}

/**
 * Sets the tab title while the calling page is mounted, and puts the bare
 * product name back when it goes. A part still loading (a hostname before
 * the first poll) is simply left out, so the tab reads "Netra" until the
 * page knows what it is showing.
 */
export function useTabTitle(...parts: (string | null | undefined)[]) {
  const title = tabTitle(...parts);
  useEffect(() => {
    document.title = title;
  }, [title]);
  useEffect(
    () => () => {
      document.title = PRODUCT;
    },
    [],
  );
}
