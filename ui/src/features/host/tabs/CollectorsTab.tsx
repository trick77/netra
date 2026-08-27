// Is the agent doing its job, and which parts of it are not.
//
// The capability list was a card at the foot of the Overview tab, below the
// disk meters and the sensors: the one place a reader learns that `smart` is
// "not permitted" and that the charts they came looking for were never going
// to be drawn. It answers a different question from the cards it sat among --
// those say how the machine is doing, this says how much of that answer to
// trust -- so it has its own tab.
//
// The agent's own charts came with it from System. The list says which
// collectors are failing right now; the charts say for how long, and whether
// the hub is what they are failing to reach. Either half alone leaves the
// other question unanswerable.
import type { HostDetail } from "../../../lib/api";
import { Badge } from "../../../ui/Badge";
import { Facts } from "./Facts";
import { CollectorGraphs, type GraphsProps } from "./Graphs";
import { Panel } from "./Panel";

export interface CollectorsTabProps {
  host: HostDetail;
  /** The two families these panels draw. Named as GraphsProps names them --
   * `host` there is the host METRICS family, which is why the host itself
   * cannot simply be spread in beside them. */
  sources: GraphsProps;
}

export function CollectorsTab({ host, sources }: CollectorsTabProps) {
  const capabilities = Object.entries(host.capabilities);

  return (
    <>
      <Panel label="Collectors" title="Collectors">
        {capabilities.length === 0 ? (
          <p className="note">The agent reported no capabilities.</p>
        ) : (
          <Facts
            rows={capabilities.map(([name, state]) => [
              name,
              // The reason is the value the agent sent; a collector that
              // cannot run says why rather than showing an empty chart.
              state === "ok" ? (
                <Badge severity="ok">ok</Badge>
              ) : (
                <span>{state}</span>
              ),
            ])}
          />
        )}
      </Panel>
      <CollectorGraphs {...sources} />
    </>
  );
}
