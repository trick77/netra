import type { ReactNode } from "react";
import { SEVERITY_CLASS, type FillSeverity } from "./Meter";
import type { Severity } from "./Badge";
import { ABSENT, relative } from "../lib/format";

// What is wrong, said once, at the top of the thing it is wrong with.
//
// Extracted from the host page's Overview, which had the only copy. The fleet's
// container list needs the identical band -- same shape, same severity
// headings, same onset clause -- and Overview's own note says why a second copy
// would be a mistake: the fleet and the host page disagreed about one host once
// already (#92), and two renderings of one vocabulary is how that happens.
//
// The band is PROSE. One line per problem, each a sentence naming the thing and
// what is wrong with it, with the age appended rather than given a column. That
// is deliberate and it is the reason this is not a table: a list of different
// problems has no column that means the same thing twice.

/** Worst first. `ok` is not a condition and `neutral` is not a severity
 * anything reaches this band at. */
export const ATTENTION_SEVERITIES: readonly FillSeverity[] = [
  "critical",
  "warning",
];

const SEVERITY_WORD: Record<FillSeverity, string> = {
  critical: "Critical",
  warning: "Warning",
  ok: "OK",
};

export interface AttentionRow {
  /**
   * `Severity`, not `FillSeverity`: callers derive these from vocabularies
   * that include `neutral` (a paused container, a host nobody can currently
   * see) and should not have to narrow before handing them over. Only the two
   * in ATTENTION_SEVERITIES are drawn -- a neutral row is not a thing to act
   * on, and a band that listed it would be answering a question nobody asked.
   */
  severity: Severity;
  /** The sentence. A node, so a caller can link the thing it names without
   * this component knowing what kind of thing that is. */
  what: ReactNode;
  /**
   * When this became true, or null where nothing can say.
   *
   * Null is common and honest: several container states are derived from the
   * current row and have no memory of when they first became true, so they
   * print no clause at all rather than a plausible instant. Naming a made-up
   * onset is worse than naming none -- "since 5 m ago" beside something that
   * has been wrong for a week is a sentence a reader acts on.
   */
  since?: string | null;
  /**
   * `since` is a FLOOR rather than a moment: the walk back through the series
   * hit the end of what is retained, so the row says "over 7 d" instead of
   * naming a bucket where nothing happened.
   */
  sinceAtLeast?: boolean;
}

/**
 * "· since 6 h ago", or "· over 7 d" when the onset is only a floor.
 *
 * Appended to the sentence rather than given a column of its own: the band is
 * prose, one line per problem, and how long it has been true reads as part of
 * the sentence rather than as a second field to line up.
 */
export function sinceClause(row: AttentionRow, now: Date): string {
  if (row.since === null || row.since === undefined) return "";
  const age = relative(row.since, now);
  if (age === ABSENT) return "";
  if (row.sinceAtLeast === true) {
    // "over 7 d", not "over 7 d ago": the floor is a span, and the walk could
    // not see past it. relative() writes an age, so the suffix comes off.
    return ` · over ${age.replace(/ ago$/, "")}`;
  }
  return ` · since ${age}`;
}

export interface AttentionBandProps {
  rows: readonly AttentionRow[];
  /** The clock the onset clauses are measured against, so a test can pin it. */
  now?: Date;
  /**
   * The most rows to draw per severity, and what to say about the rest.
   *
   * A host has a bounded number of things wrong with it; a FLEET does not, and
   * one lossy host can put dozens of container rows in here. That is exactly
   * the unbounded per-item band conditions.ts deleted for hosts, so a caller
   * spanning many subjects passes a cap and an overflow line -- and the
   * overflow must be a LINK, because the failure of the band that was removed
   * was "+30 more hosts" with nothing to click.
   */
  cap?: number;
  /** Renders the overflow line for a severity that was capped. */
  overflow?: (severity: FillSeverity, hidden: number) => ReactNode;
}

export function AttentionBand({
  rows,
  now = new Date(),
  cap,
  overflow,
}: AttentionBandProps) {
  // Nothing wrong renders nothing at all. A permanently visible "all clear"
  // box in the best position on the page is a box people stop reading, which
  // is the rule AttentionCounts and Overview both already follow.
  //
  // Judged on what will actually be DRAWN, not on how many rows arrived: a
  // caller handing over none but neutral ones -- a fleet whose only unusual
  // containers are paused, which is an ordinary Tuesday -- would otherwise get
  // an empty bordered box with nothing in it.
  if (!rows.some((row) => ATTENTION_SEVERITIES.includes(row.severity as never)))
    return null;

  return (
    <section className="attn" aria-label="Needs attention">
      {/* The severity is a heading over the rows at that severity, said once,
          rather than a chip repeated on every one of them. Three critical rows
          do not need the word "critical" three times. The dot on each row is
          the mark; the heading above it is the word spec 3.3 requires, and it
          is a real heading so a screen reader reaches the rows through it. */}
      {ATTENTION_SEVERITIES.map((severity) => {
        // Stable partition, so the caller's written order survives inside each
        // severity -- which for a container list is the ranked order the rest
        // of the app already sorts states by.
        const all = rows.filter((row) => row.severity === severity);
        if (all.length === 0) return null;
        const shown = cap === undefined ? all : all.slice(0, cap);
        const hidden = all.length - shown.length;
        return (
          <div key={severity}>
            <h3 className={`attn-sev ${SEVERITY_CLASS[severity]}`}>
              {SEVERITY_WORD[severity]} <span className="n">{all.length}</span>
            </h3>
            <ul className="attn-list">
              {shown.map((row, index) => (
                <li className="attn-row" key={index}>
                  <span
                    className={`dot ${SEVERITY_CLASS[severity]}`}
                    aria-hidden="true"
                  />
                  <span className="what">
                    {row.what}
                    {/* How long it has been true. Quiet, because the sentence
                        is what to act on and the age is context for it.

                        `since`, not `muted`: index.css has a rule for the
                        first and none at all for the second, so this line was
                        rendering at the sentence's own ink and losing its
                        tabular figures. The class the stylesheet was written
                        for had never been emitted by anything. */}
                    {sinceClause(row, now) === "" ? null : (
                      <span className="since">{sinceClause(row, now)}</span>
                    )}
                  </span>
                </li>
              ))}
              {hidden > 0 && overflow !== undefined ? (
                <li className="attn-row attn-more">
                  <span className="what">{overflow(severity, hidden)}</span>
                </li>
              ) : null}
            </ul>
          </div>
        );
      })}
    </section>
  );
}
