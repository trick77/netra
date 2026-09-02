import { Server } from "lucide-react";
import { EmptyState } from "../../ui/EmptyState";

/**
 * The fleet list's empty state, defined once. It lived as a copy in the
 * table and a copy in the card grid that used to sit beside it, with a
 * comment in each asserting the two must stay identical -- in the one pair
 * of files whose stated purpose was that they could not drift. The grid is
 * gone; the copy stays here, where the next renderer will find it rather
 * than write its own.
 *
 * `filtered` is the difference between a hub nobody has pointed an agent at
 * and a hundred-host fleet with a filter that currently matches none of it.
 * Onboarding copy in front of a full fleet is a page contradicting itself,
 * and it is reachable two ways: a name typed into the filter, and a shared
 * "/?attn=oom" link opened after that kind cleared.
 */
export function FleetEmptyState({ filtered = false }: { filtered?: boolean }) {
  if (filtered) {
    return (
      <EmptyState
        icon={Server}
        title="No hosts match"
        body="Every host is filtered out. Clear the filter to see them all."
      />
    );
  }
  return (
    <EmptyState
      icon={Server}
      title="No hosts yet"
      body="Once an agent reports in, its host appears here."
    />
  );
}
