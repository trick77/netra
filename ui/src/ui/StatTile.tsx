import type { MouseEvent, ReactNode } from "react";

// The one large number on a page uses proportional figures (design system
// default, spec §3.6) -- tabular-nums is reserved for columns of numbers
// that need to line up vertically, which a single headline value is not.
// index.css's `.tile .v` rule deliberately carries no font-variant-numeric,
// so this component must not add one inline.
export interface StatTileProps {
  label: string;
  value: string | number;
  unit?: string;
  detail?: string;
  /**
   * Where this tile leads, when it counts something the page can show.
   *
   * A tile reading "Containers 22" above a Containers tab is a control
   * whether or not it was built as one: it names a set, states its size, and
   * sits directly above the list of that set. Given an href it becomes a
   * real link -- not a div with a click handler -- so middle-click,
   * cmd-click, copy-link and keyboard focus all work without this component
   * reimplementing any of them. Same reasoning, and the same interception
   * rules, as ui/Tabs.tsx.
   *
   * Tiles that count nothing navigable (fleet traffic is a rate, not a set)
   * simply omit it and stay inert.
   */
  href?: string;
  /** Client-side navigation hook, matching Tabs: a plain left click is
   * intercepted so the app can handle it without a full page load, while the
   * href stays real for every other kind of click. */
  onSelect?: () => void;
}

export function StatTile({
  label,
  value,
  unit,
  detail,
  href,
  onSelect,
}: StatTileProps) {
  const body: ReactNode = (
    <>
      <div className="k">{label}</div>
      <div className="v">
        {value}
        {unit !== undefined && <span> {unit}</span>}
      </div>
      {detail !== undefined && <div className="d">{detail}</div>}
    </>
  );

  if (href === undefined) return <div className="tile">{body}</div>;

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
    <a className="tile tile-link" href={href} onClick={handleClick}>
      {body}
    </a>
  );
}
