// Severity never rides on colour alone (spec §3.3): netra's accent and its
// critical red measure ΔE 7.2 at normal vision and 2.2 under deuteranopia --
// not reliably distinguishable, and no re-stepping fixes it. What preserves
// meaning is that every status carries a dot AND a word, so `children` is
// required by the type -- a caller cannot construct a bare coloured dot that
// means "critical" without a label attached to it.
//
// The hue lived in a tinted chip ground for three commits and is back in the
// dot. A tint is a filled object, and a page listing fifty warned hosts drew
// fifty of them: the ground did the shouting the word is supposed to do, and
// it could not be put in a table cell or mid-sentence without repainting the
// row it sat in. A dot costs 12px and changes no ground it lands on. Only
// the STATUS badge sheds the chip -- see .badge in index.css for the neutral
// one, which is a label rather than a severity and keeps its own.
import type { ReactNode } from "react";

export type Severity = "ok" | "warning" | "serious" | "critical" | "neutral";

const SEVERITY_CLASS: Record<Severity, string | null> = {
  ok: "st-ok",
  warning: "st-warn",
  serious: "st-serious",
  critical: "st-crit",
  neutral: null,
};

export interface BadgeProps {
  severity?: Severity;
  // Deliberately required, not `children?: ReactNode` -- see file header.
  children: ReactNode;
}

export function Badge({ severity = "neutral", children }: BadgeProps) {
  const severityClass = SEVERITY_CLASS[severity];
  const className = severityClass ? `badge ${severityClass}` : "badge";
  return (
    <span className={className}>
      <span className="dot" aria-hidden="true" />
      {children}
    </span>
  );
}
