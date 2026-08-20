import type { MouseEvent } from "react";
import type { Severity } from "../../ui/Badge";
import type { AttentionFilter, ConditionKind, KindGroup } from "./conditions";

/**
 * One TILE per kind of thing that is wrong, with how many hosts have it.
 *
 * Grouping by kind is what replaced the attention band, and the reason is a
 * hundred-host fleet with fifty warnings on it: the band was a block per
 * host, capped at twenty, with the overflow written as "+30 more hosts" that
 * was not a link. Thirty-one hosts that all failed the same unit are ONE
 * entry here -- which is also the truth about them, since thirty-one hosts
 * failing the same unit at the same minute is one problem and not thirty-one.
 * Bounded by the seven kinds netra has rather than by the fleet, so this is
 * the same height at four hosts and four hundred.
 *
 * A tile rather than the line of grey text this shipped as. The line was
 * --text-label in --ink-2 with no ground, sitting directly above three cards
 * spending --text-display on "Hosts reporting 98" and "Containers 0" -- so
 * the page set its inventory in 28px and what was actually wrong in 14px, and
 * the reader's own word for the result was "almost invisible". These are the
 * same grid, the same type and the same shape as the stat tiles below them,
 * because that is the page's existing way of saying "this number matters".
 *
 * Every tile is a real anchor carrying the filter it applies. App delegates
 * in-origin anchor clicks to the router, so middle-click, cmd-click and
 * copy-link all work without this component knowing anything about
 * navigation, and a reader can send someone "the fleet, filtered to failed
 * units".
 */
export interface AttentionCountsProps {
  kinds: readonly KindGroup[];
  /** The kind currently filtered to, when one is. */
  active: ConditionKind | null;
  /** Uncontrolled fallback for a page that owns its own filter state (tests,
   * and FleetPage rendered on its own). The href is what does the work when
   * the URL is in charge. */
  onSelect: (next: AttentionFilter) => void;
  /**
   * Where each entry points. Supplied by the page, because the URL this link
   * belongs in carries the reader's density, entity and range as well -- and
   * a cmd-click goes to the href without ever reaching onSelect, so an href
   * that names only the filter silently resets the other three.
   */
  href?: (next: AttentionFilter) => string;
}

const SEVERITY_CLASS: Record<Severity, string> = {
  critical: "st-crit",
  serious: "st-serious",
  warning: "st-warn",
  ok: "st-ok",
  neutral: "",
};

/**
 * The severity, as a word, on every tile.
 *
 * Not decoration and not a duplicate of the hue: severity never rides on
 * colour alone (spec §3.3), and a tile carries no dot -- its status ink is
 * spread across the count and its edge. The kind's own name does not supply
 * it either, since "Failed units" says what is wrong and not how bad it is.
 */
const SEVERITY_WORD: Record<Severity, string> = {
  critical: "Critical",
  serious: "Serious",
  warning: "Warning",
  ok: "OK",
  neutral: "Unknown",
};

export function AttentionCounts({
  kinds,
  active,
  onSelect,
  href = (next) => (next === "all" ? "/" : `/?attn=${next}`),
}: AttentionCountsProps) {
  if (kinds.length === 0) return null;

  return (
    // A real list, so a screen reader says how many different things are
    // wrong before reading them out. The grid is CSS; the semantics are not.
    <ul className="atiles" aria-label="What is wrong, by kind">
      {kinds.map((kind) => {
        const chosen = kind.kind === active;
        const next: AttentionFilter = chosen ? "all" : kind.kind;
        const hosts = kind.hostIds.length;
        const severityClass = SEVERITY_CLASS[kind.severity];
        const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
          // Modified and non-primary clicks belong to the browser -- the same
          // rule Tabs and StatFigure follow, for the same reason: an href is
          // used precisely so cmd-click still opens a tab.
          if (event.defaultPrevented || event.button !== 0) return;
          if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey)
            return;
          event.preventDefault();
          onSelect(next);
        };
        return (
          <li key={kind.kind}>
            <a
              className={`atile ${severityClass}`}
              href={href(next)}
              // Not aria-pressed: this is a link, not a toggle button, and a
              // pressed link is a control a screen reader announces twice.
              // aria-current says "this is the one you are looking at", which
              // is what it is.
              aria-current={chosen ? "true" : undefined}
              onClick={handleClick}
            >
              <span className="k">{kind.label}</span>
              {/* The noun rides the number: "3" beside "Failed units"
                  reads as three failed units, and it is three HOSTS -- one
                  of which may have five. */}
              <span className="v">
                {hosts}
                <span className="u"> host{hosts === 1 ? "" : "s"}</span>
              </span>
              <span className="d">{SEVERITY_WORD[kind.severity]}</span>
            </a>
          </li>
        );
      })}
    </ul>
  );
}
