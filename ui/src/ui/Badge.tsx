// Severity never rides on colour alone (spec §3.3): netra's accent and its
// critical red measure ΔE 7.2 at normal vision and 2.2 under deuteranopia --
// not reliably distinguishable, and no re-stepping fixes it. What preserves
// meaning is that every status carries a dot AND a word, so `children` is
// required by the type -- a caller cannot construct a bare coloured dot
// that means "critical" without a label attached to it.
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
