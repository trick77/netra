import type { ReactNode } from "react";

export interface CardProps {
  title?: string;
  action?: ReactNode;
  children: ReactNode;
}

/**
 * The `.card` / `.card > header` / `.card .body` classes come from
 * index.css (Wave 0). `.card > header` is a direct-child ELEMENT selector,
 * so the header must be a literal <header>, and it's only rendered when
 * there's a title or action — otherwise it'd be an empty bar with a
 * border that has nothing to say.
 */
export function Card({ title, action, children }: CardProps) {
  const hasHeader = Boolean(title || action);
  return (
    <div className="card">
      {hasHeader ? (
        <header>
          {title ? <h3>{title}</h3> : null}
          {action}
        </header>
      ) : null}
      <div className="body">{children}</div>
    </div>
  );
}
