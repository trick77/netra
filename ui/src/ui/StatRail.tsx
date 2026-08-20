import type { MouseEvent, ReactNode } from "react";

/**
 * The fleet's ambient figures, on one rail.
 *
 * These were three cards with 28px figures, directly under three ATTENTION
 * cards with 28px figures. Six cards, two rows, all the same weight -- so
 * "1 host stopped reporting" and "84 containers" shouted equally loudly and
 * nothing on the page said which one to read first. The attention row is
 * what this page is for; these three are context for it.
 *
 * So they keep their figures and lose their cards: one box, one line high,
 * under the row it is subordinate to. Still a box rather than a bare line of
 * text, because on an all-clear fleet the loud row above is gone entirely
 * and this is the only thing left -- a page that collapses to one sentence
 * reads as broken rather than as calm.
 */
export function StatRail({ children }: { children: ReactNode }) {
  return <div className="srail">{children}</div>;
}

export interface StatFigureProps {
  /** The figure itself, already formatted -- "3", "84", "155 kB/s". */
  value: string | number;
  /** What the figure is, reading straight on from it: "containers",
   * "of 4 hosts reporting". A phrase rather than a heading, because on a
   * rail the number comes first and the words finish the sentence. */
  label: string;
  /**
   * Where this figure leads, when it counts something the page can show.
   *
   * A figure reading "84 containers" above a Containers tab is a control
   * whether or not it was built as one: it names a set, states its size, and
   * sits directly above the list of that set. Given an href it becomes a
   * real link -- not a div with a click handler -- so middle-click,
   * cmd-click, copy-link and keyboard focus all work without this component
   * reimplementing any of them. Same reasoning, and the same interception
   * rules, as ui/Tabs.tsx.
   *
   * Figures that count nothing navigable (fleet traffic is a rate, not a
   * set) simply omit it and stay inert.
   */
  href?: string;
  /** Client-side navigation hook, matching Tabs: a plain left click is
   * intercepted so the app can handle it without a full page load, while the
   * href stays real for every other kind of click. */
  onSelect?: () => void;
}

export function StatFigure({ value, label, href, onSelect }: StatFigureProps) {
  const body: ReactNode = (
    <>
      <b>{value}</b>
      <span className="l">{label}</span>
    </>
  );

  if (href === undefined) return <div className="s">{body}</div>;

  const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
    if (!onSelect) return;
    // Modified and non-primary clicks belong to the browser: cmd-click opens
    // a tab, middle-click opens a background one, and intercepting either
    // would take away the thing an href was used for.
    if (event.defaultPrevented || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey)
      return;
    event.preventDefault();
    onSelect();
  };

  return (
    <div className="s">
      <a href={href} onClick={handleClick}>
        {body}
      </a>
    </div>
  );
}
