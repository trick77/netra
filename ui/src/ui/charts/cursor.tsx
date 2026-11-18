// One cursor shared by every chart of a page section.
//
// Hovering one panel draws the crosshair at the same MOMENT in every other
// panel of the tab, so a spike in CPU lines up with the disk or network chart
// beside it without the reader matching time labels by eye.
//
// Shared as a timestamp, never as a bucket index. Two families on one tab can
// be answered at different tiers or over windows that differ by a bucket (see
// the foreign() note in chartSpecs' bandsFor), so "index 41" names a different
// minute in each; each chart turns the moment back into its own bucket.
import {
  createContext,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from "react";

export interface SharedCursor {
  /** The moment under the pointer, epoch ms, or null when nothing is hovered. */
  at: number | null;
  /** Which chart the pointer is over: the one that shows the readout. */
  owner: string | null;
  set: (at: number, owner: string) => void;
  clear: (owner: string) => void;
}

const CursorContext = createContext<SharedCursor | null>(null);

/** Shares one cursor between every chart rendered inside it. */
export function ChartCursorProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<{
    at: number | null;
    owner: string | null;
  }>({ at: null, owner: null });
  const value = useMemo<SharedCursor>(
    () => ({
      ...state,
      set: (at, owner) =>
        setState((prev) =>
          prev.at === at && prev.owner === owner ? prev : { at, owner },
        ),
      // Only the chart that holds the cursor may drop it: crossing from one
      // panel into the next can deliver the old panel's leave after the new
      // panel's first move, and an unconditional clear would erase it.
      clear: (owner) =>
        setState((prev) =>
          prev.owner === owner ? { at: null, owner: null } : prev,
        ),
    }),
    [state],
  );
  return (
    <CursorContext.Provider value={value}>{children}</CursorContext.Provider>
  );
}

/** The section's shared cursor, or null outside a provider. */
export function useSharedCursor(): SharedCursor | null {
  return useContext(CursorContext);
}

export interface Window {
  from: string;
  to: string;
}

/** An event at a moment, before a chart has placed it on its own axis. */
export interface TimedEvent {
  /** Epoch ms. */
  at: number;
  severity: "critical" | "warning" | "info";
  label: string;
}

/** Where a moment falls across a window, 0-1, or null outside it. */
export function fractionAt(at: number, window: Window | null): number | null {
  const s = span(window);
  if (s === null || at < s.from || at > s.to) return null;
  return (at - s.from) / (s.to - s.from);
}

function span(window: Window | null): { from: number; to: number } | null {
  if (window === null) return null;
  const from = Date.parse(window.from);
  const to = Date.parse(window.to);
  if (!Number.isFinite(from) || !Number.isFinite(to) || to <= from) return null;
  return { from, to };
}

/**
 * The moment a bucket stands for, spread evenly across the answered window.
 *
 * The same placement the time axis and ChartFigure's cursor column use, so
 * the crosshair, the tick under it and the readout's time cannot disagree.
 */
export function timeAt(
  index: number,
  window: Window | null,
  count: number,
): number | null {
  const s = span(window);
  if (s === null || count <= 1) return null;
  return s.from + (index / (count - 1)) * (s.to - s.from);
}

/**
 * The bucket nearest a moment, or null when the moment lies outside this
 * chart's window: a panel answered over a shorter window must not pin its
 * crosshair to an edge it does not cover.
 */
export function indexAt(
  at: number | null,
  window: Window | null,
  count: number,
): number | null {
  const s = span(window);
  if (at === null || s === null || count <= 1) return null;
  if (at < s.from || at > s.to) return null;
  return Math.round(((at - s.from) / (s.to - s.from)) * (count - 1));
}
