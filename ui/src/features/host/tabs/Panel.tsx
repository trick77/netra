import type { ReactNode } from "react";
import { Card } from "../../../ui/Card";

/** A labelled landmark around a Card, so each summary is reachable by name
 * (Card itself renders a plain div and has no labelling of its own).
 *
 * Its own file since the Limits card and the Collectors tab left Overview.tsx
 * and still have to draw the same box: three tabs sharing one definition is
 * what stops a card on one of them growing a heading the others do not have.
 * Graphs.tsx's Panel is a different thing with the same name -- it wraps a
 * chart spec -- and stays where it is. */
export function Panel({
  label,
  title,
  action,
  className,
  children,
}: {
  label: string;
  title: string;
  action?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <section aria-label={label} className={className}>
      <Card title={title} action={action}>
        {children}
      </Card>
    </section>
  );
}
