package notify

import "github.com/trick77/netra/internal/hub/conditions"

// EventTypes are the event types that can be critical and so notify: the
// agent's kernel log types (agent/collector/kmsg.go), mdraid and the agent's
// own hub-delivery events. Mirrored by MUTABLE_EVENT_TYPES in
// ui/src/features/host/mutes.ts, which offers a mute on these events only.
var EventTypes = []string{
	"disk_error", "ata_error", "scsi_error", "nvme_error", "md_fail",
	"fs_error", "oom_kill", "hw_error", "thermal", "kernel_fault",
	"link_change", "mdraid", "hub",
}

// MutableKind reports whether a mute on kind can ever match anything: a
// condition kind, an event type that notifies, or "" for the whole host. A
// typo such as "disks" would otherwise be stored, listed as a mute, and
// silence nothing.
func MutableKind(kind string) bool {
	if kind == "" || ConditionKind(kind) {
		return true
	}
	for _, t := range EventTypes {
		if t == kind {
			return true
		}
	}
	return false
}

// ConditionKind reports whether kind names a condition, so something that can
// resolve, rather than an event type.
func ConditionKind(kind string) bool {
	for _, k := range conditions.Catalogue() {
		if k.Kind == kind {
			return true
		}
	}
	return false
}
