// What a mute covers and how it reads, shared by the alert lines that set
// one, the host header that lists them and the admin page's fleet list.
import type { Mute } from "../../lib/api";
import { absolute } from "../../lib/format";
import { CONDITION_LABELS, KERNEL_EVENT_TYPES } from "../events/message";

/** The event types that can notify, so the ones a mute can name. The kernel
 * log's types plus mdraid and the agent's hub-delivery events; mirrors
 * notify.EventTypes in internal/hub/notify/kinds.go, which rejects any
 * other kind. */
export const MUTABLE_EVENT_TYPES: readonly string[] = [
  ...KERNEL_EVENT_TYPES,
  "mdraid",
  "hub",
];

/** One alert a mute can be set on: a condition kind or an event type, and
 * its subject exactly as the hub stored it ("" for one about the host). */
export interface MuteTarget {
  kind: string;
  subject: string;
  /** A condition of this kind and subject is open now, so "until resolved"
   * has something to end with. */
  open: boolean;
  /** Said in the form when the mute reaches further than the line it is set
   * on: one failed-units condition covers every unit the host lists. */
  reach?: string;
}

/** What a mute covers, in words. */
export function muteScope(kind: string, subject: string | null = null): string {
  if (kind === "") return "all notifications";
  const label = CONDITION_LABELS[kind] ?? kind;
  return subject === null || subject === "" ? label : `${label} — ${subject}`;
}

/** "until 18 Nov 2026, 14:30:00", "until resolved" or "permanently". */
export function muteUntil(m: Mute): string {
  if (m.until_resolved) return "until resolved";
  return m.until === null ? "permanently" : `until ${absolute(m.until)}`;
}

/** The whole sentence a mute reads as, its note included. */
export function muteText(m: Mute): string {
  const text = `${muteScope(m.kind, m.subject)} ${muteUntil(m)}`;
  return m.note === "" ? text : `${text} (${m.note})`;
}

/** The mute silencing this alert, the narrowest one when several do: lifting
 * it from the alert's line should lift the one set there, not a host-wide
 * one set somewhere else. Mirrors mutedSQL in internal/hub/store/notify.go,
 * except that a mute bound to a condition is taken to cover the open one
 * (the list drops it once that resolves). */
export function coveringMute(
  mutes: readonly Mute[],
  hostId: number,
  target: Pick<MuteTarget, "kind" | "subject">,
): Mute | undefined {
  const rank = (m: Mute): number =>
    m.kind === "" ? 2 : m.subject === null ? 1 : 0;
  return mutes
    .filter(
      (m) =>
        m.host_id === hostId &&
        (m.kind === "" || m.kind === target.kind) &&
        (m.subject === null || m.subject === target.subject),
    )
    .sort((a, b) => rank(a) - rank(b))[0];
}
