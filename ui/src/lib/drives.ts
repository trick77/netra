/**
 * What the agent's `smart` capability means, in the reader's terms.
 *
 * "No drives reported" is true of a host with no disks, of a container agent
 * with no device mapped in, and of a host whose smartctl will not run -- and
 * only the last two are something to go and fix. The agent knows which it is
 * and, until these values existed, said nothing: the empty scan produced no
 * rows, no capability and no log, so the Storage tab could only guess.
 *
 *   no-device-access     smartctl itself did not run: missing binary, EACCES,
 *                        no device node. The collector never got a device
 *                        list at all.
 *   no-devices           smartctl ran and scanned nothing. On a container
 *                        agent that is a missing devices: mapping far more
 *                        often than a machine without disks.
 *   no-readable-devices  drives were scanned and not one answered. The
 *                        passthrough works; the drives, or the capability
 *                        behind them, do not.
 *   usb-only-devices     every drive found hangs off a USB bridge, which the
 *                        agent deliberately leaves alone. Nothing is
 *                        misconfigured and nothing failed, so this one names
 *                        no remedy -- it is the one empty Storage tab that is
 *                        working as intended.
 *
 * All three mirror internal/agent/collector/smart.go. The values are free text
 * on the wire -- capabilities is JSONB with no enum and no CHECK -- so an
 * unrecognised one still has to render as something the operator can act on,
 * the same rule hostContainerNote follows.
 */

/**
 * The fix for the two states an operator can actually do something about. Both
 * are compose the setup script writes -- a devices: entry and the capability
 * that goes with it -- not a bug in the agent, so the sentence names the
 * script rather than describing the symptom twice.
 */
const SMART_REMEDY = "Re-run setup-agent.sh on the host.";

/**
 * The explanation for ONE host, for the Storage tab of its own page.
 * Null when the agent reported nothing, which is the healthy case.
 */
export function hostDriveNote(
  capabilities: Record<string, string> | undefined,
): string | null {
  const value = capabilities?.smart;
  if (value === undefined || value === "" || value === "ok") return null;

  switch (value) {
    case "no-device-access":
      return `This host's agent could not run smartctl: it is missing, or the container lacks the SYS_RAWIO capability and the device node it needs. ${SMART_REMEDY}`;
    case "no-devices":
      // Careful not to assert that drives exist: a VM on virtio has none, and
      // reports exactly this. The sentence says what smartctl saw and offers
      // the remedy without claiming the host is broken.
      return `This host's agent ran smartctl and it found no drive to read. A container agent sees only the devices mapped into it, so a host with disks needs them mapped in. ${SMART_REMEDY}`;
    case "usb-only-devices":
      // No remedy offered on purpose. Driving a USB bridge with -d sat is
      // unreliable and can hang the enclosure, and a hung enclosure stalls the
      // scrape for every other collector too -- so this is a decision, not a
      // gap, and telling the operator to "fix" it would be telling them to
      // make their agent worse.
      return "Every drive this host's agent found is USB-attached, and it leaves those alone: driving a USB bridge for SMART is unreliable and can hang the enclosure, stalling the whole scrape. Directly attached drives are read normally.";
    case "no-readable-devices":
      return "This host's agent found drives and not one of them answered SMART. The passthrough is working, so the drives themselves, or the capability behind them, are what to look at.";
    default:
      // A capability netra does not know the wording of is still the agent
      // saying something went wrong, and quoting it verbatim is more use than
      // silence: it is greppable in the agent's source.
      return `This host's agent reported SMART as “${value}”.`;
  }
}
