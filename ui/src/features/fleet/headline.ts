/**
 * What the fleet page calls itself: the set on screen, then what is wrong
 * with it.
 *
 * The heading used to be the word "Fleet" (or "Containers"), which is what
 * the nav rail already said and the one thing on the line that could not
 * change. It states the fleet instead -- "4 hosts, 2 need attention" -- so
 * the first line of the page is the thing the page exists to say.
 *
 * The SET leads and the trouble is the clause, rather than "2 of 4 hosts
 * need attention", for three reasons. The stem does not move between polls,
 * so an <h1> that repaints every few seconds does not restructure itself
 * under a screen reader. The all-clear is the same sentence with its tail
 * swapped ("4 hosts, all steady") rather than a second construction. And the
 * singular needs no special wording: "1 host, needs attention" agrees on its
 * own, where "1 of 1 host" and "All 1 host" do not.
 *
 * A split-out module because it is the one part of the head that is pure --
 * counts in, a sentence out -- and every state it has to survive (nothing
 * fetched, one host, a filtered container list) is a case worth naming in a
 * test rather than a branch buried in a page render.
 */

/** The sentence, and the part of it that carries the severity colour. */
export interface Headline {
  /** What the page holds: "4 hosts". Never empty. */
  stem: string;
  /**
   * What is wrong with it: "2 need attention". Null when nothing is -- the
   * caller then reads `steady` for the all-clear tail.
   */
  clause: string | null;
  /** The all-clear tail, when `clause` is null: "all steady". */
  steady: string | null;
}

/**
 * Neither tab has anything to state before its first fetch lands, and an
 * empty fleet's own empty state already says "No hosts yet" directly below
 * this line. So the heading falls back to naming the page, which is the one
 * time that word says something the rest of the screen does not.
 */
export function hostHeadline(total: number, troubled: number): Headline {
  if (total === 0) return { stem: "Fleet", clause: null, steady: null };
  const stem = `${total} host${total === 1 ? "" : "s"}`;
  if (troubled === 0) {
    // "steady", not "all steady", for a fleet of one: there is no "all" of a
    // single host.
    return {
      stem,
      clause: null,
      steady: total === 1 ? "steady" : "all steady",
    };
  }
  // The clause agrees with what it counts, not with the stem: on a one-host
  // fleet it is that host that needs attention.
  const clause =
    troubled === 1 && total === 1
      ? "needs attention"
      : `${troubled} need${troubled === 1 ? "s" : ""} attention`;
  return { stem, clause, steady: null };
}

/**
 * The container tab's sentence, in the container vocabulary.
 *
 * "not reporting normally" rather than "need attention": the container
 * states are measurements (Gone, Silent, Near mem_limit, Host offline) and
 * nothing is being asked of a container. It is also the word the states
 * themselves turn on, which keeps the heading and the chips under it in one
 * language.
 *
 * `known` is the difference between a fleet that runs no containers and a
 * fan-out that has not answered yet -- the same distinction the figure beside
 * it makes, and the reason this does not simply read `total === 0`.
 */
export function containerHeadline(
  total: number,
  troubled: number,
  known: boolean,
): Headline {
  if (!known || total === 0) {
    return { stem: "Containers", clause: null, steady: null };
  }
  const stem = `${total} container${total === 1 ? "" : "s"}`;
  if (troubled === 0) {
    return {
      stem,
      clause: null,
      steady: total === 1 ? "reporting" : "all reporting",
    };
  }
  const clause =
    troubled === 1 && total === 1
      ? "not reporting normally"
      : `${troubled} not reporting normally`;
  return { stem, clause, steady: null };
}
