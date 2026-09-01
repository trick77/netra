import type { MouseEvent } from "react";
import type { Severity } from "../../ui/Badge";
/**
 * One entry in the counts line.
 *
 * Structural, not `KindGroup`: hosts and containers both have kinds of thing
 * that are wrong, and a second component drawing the same chips in the same
 * CSS would be the drift this app keeps removing. The caller adapts its own
 * groups into these -- see FleetPage, which does it in one map for each
 * entity.
 */
export interface CountTile {
  /** What `?attn=` carries for this entry. */
  kind: string;
  label: string;
  severity: Severity;
  /** The rows carrying it, by id, in the order they were read. Ids rather
   * than a number so a caller cannot count one row twice for a kind it
   * carries twice. */
  ids: readonly string[];
  /**
   * The severity, as a word, when the word says something the label does not.
   *
   * The caller decides, because only the caller knows whether the severity is
   * a property of the kind or a reading off the rows. A container kind's
   * severity is a constant of the kind (state.ts KIND_SEVERITY) -- "Silent"
   * is always serious -- so the word was a fourth span that changed for no
   * container that ever existed. A host kind's is not: groupByKind takes the
   * worst condition in the group, so a filesystem chip enters at Warning and
   * escalates to Critical when one host crosses, and there the word is the
   * only thing separating the two without colour (spec 3.3).
   *
   * Absent, the chip is dot, label and count. The accessible name below
   * carries the severity either way, so this trims what is DRAWN and never
   * what is announced.
   */
  severityWord?: string;
}

/**
 * One CHIP per kind of thing that is wrong, with how many hosts have it.
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
 * A chip rather than the line of grey text this shipped as, and rather than
 * the 200px card it became: the line was --text-label in --ink-2 with no
 * ground and the reader's own word for it was "almost invisible", while the
 * cards gave the page's first band the weight of a dashboard above a list
 * that had not started. One line each, so the row is the same height at one
 * kind and at five -- and on a healthy fleet, where there are none, the list
 * does not jump up the screen by a card's height between polls.
 *
 * Every chip is a real anchor carrying the filter it applies. App delegates
 * in-origin anchor clicks to the router, so middle-click, cmd-click and
 * copy-link all work without this component knowing anything about
 * navigation, and a reader can send someone "the fleet, filtered to failed
 * units".
 */
export interface AttentionCountsProps {
  kinds: readonly CountTile[];
  /** The kind currently filtered to, when one is. */
  active: string | null;
  /** Uncontrolled fallback for a page that owns its own filter state (tests,
   * and FleetPage rendered on its own). The href is what does the work when
   * the URL is in charge. */
  onSelect: (next: string) => void;
  /**
   * Where each entry points. Supplied by the page, because the URL this link
   * belongs in carries the reader's entity as well -- and a cmd-click goes to
   * the href without ever reaching onSelect, so an href that names only the
   * filter silently resets it.
   */
  href?: (next: string) => string;
}

const SEVERITY_CLASS: Record<Severity, string> = {
  critical: "st-crit",
  serious: "st-serious",
  warning: "st-warn",
  ok: "st-ok",
  neutral: "",
};

/**
 * The severity, as a word.
 *
 * Exported because the caller decides whether to DRAW it (see CountTile
 * .severityWord) while this component always ANNOUNCES it: severity never
 * rides on colour alone (spec §3.3), and a dot is a mark, so the word has to
 * exist somewhere on every chip. On chips where the label already fixes the
 * severity -- "Silent" is serious and nothing else -- somewhere is the
 * accessible name, and the visible chip reads "Silent 8" rather than
 * "Silent 8 Serious", which is three spans and one fact.
 */
export const SEVERITY_WORD: Record<Severity, string> = {
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
        const next: string = chosen ? "all" : kind.kind;
        const rows = kind.ids.length;
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
              // The whole chip in one string, because the severity word is
              // not always drawn and the count carries no noun. Read out in
              // the order the chip is read: what is wrong, how many, how bad.
              aria-label={`${kind.label}, ${rows}, ${SEVERITY_WORD[kind.severity]}`}
              // Not aria-pressed: this is a link, not a toggle button, and a
              // pressed link is a control a screen reader announces twice.
              // aria-current says "this is the one you are looking at", which
              // is what it is.
              aria-current={chosen ? "true" : undefined}
              onClick={handleClick}
            >
              {/* The severity's mark. A dot rather than the edge the tile
                  carried: on a pill the edge reads as part of the border,
                  and a dot is the shape this app already uses for a state.
                  It is never the only channel -- see the word after it. */}
              <span className="dot" aria-hidden="true" />
              <span className="k">{kind.label}</span>
              {/* The count, with no noun. "Failed units 3" beside a fleet of
                  hosts is three hosts; spelling out "3 hosts" put the widest
                  word in the chip on the one thing a reader already knows
                  they are counting. The accessible name still says it: the
                  kind, the number and the severity read in order. */}
              <span className="v">{rows}</span>
              {/* Only where it adds something -- see CountTile.severityWord.
                  Colour is never the only carrier of severity (spec 3.3/3.5)
                  and it is not here either: on a chip without this span the
                  label names the severity by naming the state, and the
                  aria-label above says the word outright. */}
              {kind.severityWord === undefined ? null : (
                <span className="d">{kind.severityWord}</span>
              )}
            </a>
          </li>
        );
      })}
    </ul>
  );
}
