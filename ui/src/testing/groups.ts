// Helpers for the two lists whose Table groups are collapsible: a group's
// rows are not in the DOM until it is opened, so a test that asserts on rows
// has to open them first. Shared rather than copied into each test file
// because "how do you get at a grouped row" is one answer, and three copies
// of it drift the moment the header's markup changes.
import { fireEvent, screen, within } from "@testing-library/react";

/**
 * Opens every collapsed group in the document.
 *
 * Clicked rather than reached round the back: it is the same control a reader
 * uses, so a test that stops passing because the disclosure broke is a test
 * doing its job.
 *
 * fireEvent, not element.click(): the latter dispatches outside React's act()
 * and the state update never flushes, so every assertion after it still sees
 * a closed group.
 */
export function expandAllGroups(): void {
  for (const head of screen.queryAllByRole("rowheader")) {
    for (const button of within(head).queryAllByRole("button", {
      expanded: false,
    })) {
      fireEvent.click(button);
    }
  }
}

/**
 * The group headings, as a reader reads them -- the name and its count, with
 * the disclosure's own accessible name and the group's totals left out.
 *
 * `textContent` on the whole rowheader picks up both, so an assertion written
 * against it reads "db-01 db-01 · 1 container CPU 4% Mem 12 MB". The visible
 * label is its own element for exactly this reason.
 */
export function groupLabels(): string[] {
  return screen
    .getAllByRole("rowheader")
    .map((head) => head.querySelector(".glabel")?.textContent ?? "");
}
