import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";

export interface EmptyStateProps {
  icon: LucideIcon;
  title: string;
  body: string;
  action?: ReactNode;
}

/**
 * No dedicated empty-state class exists in index.css yet (`.smp.na .box` is
 * sparkline-scoped, not general-purpose). Rendered with className="empty"
 * pending that class landing in index.css — see task report.
 * The icon does not receive a `color` prop so it inherits `currentColor`
 * from `--muted` via CSS rather than a hardcoded colour in this file.
 */
export function EmptyState({
  icon: Icon,
  title,
  body,
  action,
}: EmptyStateProps) {
  return (
    <div className="empty">
      <Icon aria-hidden="true" />
      <h3>{title}</h3>
      <p>{body}</p>
      {action ? <div className="empty-action">{action}</div> : null}
    </div>
  );
}
