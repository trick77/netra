import type { MouseEvent } from "react";

export interface TabItem {
  id: string;
  label: string;
  /** Every host-detail tab is a URL (e.g. /hosts/{id}/graphs) — tabs are
   * real links, so each item must carry its own href for middle-click and
   * bookmarking to work. Not in the brief's stated item shape; added here
   * because the brief's own "render <a href>" requirement can't be met
   * without it. Task 11/12 will need the same field. */
  href: string;
  badge?: string;
}

export interface TabsProps {
  items: readonly TabItem[];
  active: string;
  /** Optional client-side navigation hook. When provided, a plain left
   * click is intercepted (preventDefault) so the app's router can handle
   * it without a full page load; href stays real so middle-click, cmd-click
   * and bookmarking still work via the browser's native anchor behaviour. */
  onChange?: (id: string) => void;
}

/**
 * Uses aria-current="page", not aria-selected: these are real <a> links
 * representing "which page in this set is current", which is exactly what
 * aria-current is for (see .nav a[aria-current] elsewhere in this app).
 * aria-selected is only valid on role="tab"/option/row/gridcell — putting
 * role="tab" on an anchor would additionally obligate APG arrow-key
 * handling and would fight the plain-link semantics the brief asks us to
 * preserve (middle-click, bookmark).
 */
export function Tabs({ items, active, onChange }: TabsProps) {
  const handleClick =
    (id: string) => (event: MouseEvent<HTMLAnchorElement>) => {
      if (!onChange) return;
      if (event.defaultPrevented || event.button !== 0) return;
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey)
        return;
      event.preventDefault();
      onChange(id);
    };

  return (
    <nav className="tabs">
      {items.map((item) => {
        const isActive = item.id === active;
        return (
          <a
            key={item.id}
            href={item.href}
            aria-current={isActive ? "page" : undefined}
            onClick={handleClick(item.id)}
          >
            {item.label}
            {item.badge ? (
              <>
                {" "}
                <span className="badge">{item.badge}</span>
              </>
            ) : null}
          </a>
        );
      })}
    </nav>
  );
}
