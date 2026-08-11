import { Server } from "lucide-react";
import { EmptyState } from "../../ui/EmptyState";

/**
 * The fleet list's empty state, defined once and rendered by both the table
 * and the card grid. It lived as a copy in each of them, with a comment in
 * each asserting the two must stay identical -- in the one pair of files
 * whose stated purpose is that they cannot drift. Which density toggle a
 * browser happens to remember must not change what "no hosts" says.
 */
export function FleetEmptyState() {
  return (
    <EmptyState
      icon={Server}
      title="No hosts yet"
      body="Once an agent reports in, its host appears here."
    />
  );
}
