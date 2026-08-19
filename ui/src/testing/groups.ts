// Reading a grouped list in a test. Shared rather than copied into each test
// file because "how do you read a group heading" is one answer, and three
// copies of it drift the moment the header's markup changes.
//
// expandAllGroups() used to live here, and every assertion on a grouped row
// had to call it first: groups arrived shut. They arrive OPEN now, which made
// it a no-op at all fourteen call sites, and a helper kept "in case" is a
// helper whose comment starts lying about who calls it.
import { screen } from "@testing-library/react";

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
