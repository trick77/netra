// Severity never rides on colour alone (spec §3.3): netra's amber and its
// critical red measure ΔE 7.2 at normal vision and 2.2 under deuteranopia --
// not reliably distinguishable, and no re-stepping fixes it. Badge answers
// that with a WORD beside its dot. This answers it with a mark, for the fleet
// list, where the word was the same four letters repeated down the leftmost
// column and what a reader scans for is which rows have a mark at all.
//
// THE SECOND CHANNEL IS THE INTERIOR, and the first shipped version had
// nothing of the sort. It drew its own paths: a filled triangle, and a filled
// regular octagon at 13px. An octagon has eight sides, and eight sides across
// thirteen pixels are sub-pixel corner cuts -- it rendered as a circle, which
// is precisely the bare coloured dot Badge's required `children` exists to
// prevent, and the triangle beside it was a solid blob. At a glance the pair
// read as "a red thing" and "a yellow thing".
//
// lucide is this app's icon set -- the nav, the palette, the table chevron,
// InfoTip, Button's spinner -- and it draws these properly: an outline with a
// mark inside it, which is what makes an icon legible small. TriangleAlert
// carries an exclamation and OctagonX a cross, so those two differ by
// interior AND by outline, and either survives greyscale on its own. Drawing
// our own was the mistake: a second channel is not something to freehand at
// 13px.
//
// TWO MARKS, not three. Offline is critical, and it draws critical's mark --
// the same octagon, the same hue. It briefly had a bare X of its own, on the
// argument that a host being gone is a different fact from a condition read
// off a host that is answering, and that the two should therefore not share a
// glyph. They are different facts, and the column still does not rank them:
// what a reader takes off this column is how bad, not which kind, and a
// second red glyph asked them to learn a vocabulary to get the same answer.
// The kind is what the row's other cells are for -- the figures are all
// absent on a host that is gone, and present on one that is merely full --
// and it survives here as the accessible name.
//
// role="img" with an aria-label, NOT aria-hidden: OsIcon may hide itself
// because the OS name is spelled out beside it, and here nothing is. The
// label is the row's own word -- "offline", "never seen", "sporadic" -- so a
// screen reader gets the specific fact rather than the severity band it falls
// in, which is more than the mark says by eye.
import { OctagonX, TriangleAlert } from "lucide-react";

export type MarkKind = "warning" | "critical";

const MARKS = {
  warning: { Icon: TriangleAlert, cls: "st-warn" },
  critical: { Icon: OctagonX, cls: "st-crit" },
} as const;

export function SeverityMark({
  kind,
  /** The row's own word for this state, for the accessible name. Defaults to
   * the kind, which is the right word for a plain warning or critical. */
  label,
}: {
  kind: MarkKind;
  label?: string;
}) {
  const { Icon, cls } = MARKS[kind];
  return (
    // Size and stroke come from .smark in index.css rather than from lucide's
    // props: lucide draws at stroke 2 for a 24px box, so every use in this app
    // smaller than that has had to re-weight it, and keeping both numbers in
    // the stylesheet puts them next to the type they sit beside.
    <Icon className={`smark ${cls}`} role="img" aria-label={label ?? kind} />
  );
}
