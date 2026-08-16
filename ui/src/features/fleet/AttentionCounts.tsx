import type { MouseEvent } from "react";
import type { AttentionFilter, ConditionKind, KindGroup } from "./conditions";

/**
 * One line per KIND of thing that is wrong, with how many hosts have it.
 *
 * This is what replaced the attention band, and the reason is a hundred-host
 * fleet with fifty warnings on it. The band was a block per host: fifty
 * blocks, capped at twenty, with the overflow written as "+30 more hosts"
 * that was not a link. Grouped by kind instead, thirty-one hosts that all
 * failed the same unit are ONE line -- which is also the truth about them,
 * since thirty-one hosts failing the same unit at the same minute is one
 * problem and not thirty-one.
 *
 * The line is bounded by the number of condition kinds netra has (seven), not
 * by the size of the fleet, so it is the same height at four hosts and four
 * hundred.
 *
 * Every entry is a real anchor carrying the filter it applies. App delegates
 * in-origin anchor clicks to the router, so middle-click, cmd-click and
 * copy-link all work here without this component knowing anything about
 * navigation -- and a reader can send someone "the fleet, filtered to failed
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
}

const SEVERITY_CLASS: Record<string, string> = {
  critical: "st-crit",
  serious: "st-serious",
  warning: "st-warn",
  ok: "st-ok",
  neutral: "",
};

export function AttentionCounts({
  kinds,
  active,
  onSelect,
}: AttentionCountsProps) {
  if (kinds.length === 0) return null;

  return (
    <ul className="kinds" aria-label="What is wrong, by kind">
      {kinds.map((kind) => {
        const chosen = kind.kind === active;
        const next: AttentionFilter = chosen ? "all" : kind.kind;
        const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
          // Modified and non-primary clicks belong to the browser -- the same
          // rule Tabs and StatTile follow, for the same reason: an href is
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
              href={next === "all" ? "/" : `/?attn=${next}`}
              // Not aria-pressed: this is a link, not a toggle button, and a
              // pressed link is a control a screen reader announces twice.
              // aria-current says "this is the one you are looking at", which
              // is what it is.
              aria-current={chosen ? "true" : undefined}
              onClick={handleClick}
            >
              {/* The dot is the severity; the label beside it is the word
                  that has to accompany it (spec §3.3), and it is the kind's
                  name rather than "warning" -- "Failed units" says both what
                  and how bad, once. */}
              <span
                className={`dot ${SEVERITY_CLASS[kind.severity] ?? ""}`}
                aria-hidden="true"
              />
              {kind.label} <b className="tnum">{kind.hostIds.length}</b>
            </a>
          </li>
        );
      })}
    </ul>
  );
}
