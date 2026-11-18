package notify

// EventType is the events-log type a message is recorded under, one row per
// message, on the host it was about. The log is how anyone sees what went
// out and for which alerts; the message itself only reaches its channels.
//
// Recorded as info (it reached at least one channel) or warning (it reached
// none), never critical: CriticalEventsSince reads critical rows, and a
// record of a message must not become the cause of the next one. Agents may
// not send this type (InsertEvents drops it), so every row is the hub's.
const EventType = "notification"

// Outcomes of one message.
const (
	Delivered = "sent"
	Partial   = "partial"
	Failed    = "failed"
)

// Occurred is the What of an event in a message: it happened, once.
const Occurred What = "event"

// maxLoggedAlerts caps the alerts one record lists. A fleet-wide condition
// can put dozens on one host's message; the record keeps the first ones and
// counts the rest.
const maxLoggedAlerts = 50

// Notification is the detail of one EventType row: what a message was about
// and whether it got through. Channel labels only ("channel 2 (smtp)"), never
// error text: the events log is readable by every signed-in user and kept for
// months, and an error can quote more of a channel URL than the scrub knows.
type Notification struct {
	Outcome string   `json:"outcome"`
	Failed  []string `json:"failed,omitempty"`
	Alerts  []Alert  `json:"alerts"`
	// More counts the alerts beyond maxLoggedAlerts.
	More int `json:"more,omitempty"`
}

// Alert is one line of a message: a condition and why it was in it, or an
// event.
type Alert struct {
	// Kind is the condition kind or the event type.
	Kind     string `json:"kind"`
	Subject  string `json:"subject,omitempty"`
	What     What   `json:"what"`
	Severity string `json:"severity,omitempty"`
}

// alertsOf lists every line of a message, uncapped.
func alertsOf(conds []Condition, events []Event) []Alert {
	out := make([]Alert, 0, len(conds)+len(events))
	for _, c := range conds {
		out = append(out, Alert{Kind: c.Kind, Subject: c.Subject, What: c.What, Severity: c.Severity})
	}
	for _, e := range events {
		out = append(out, Alert{Kind: e.Type, Subject: e.Subject, What: Occurred, Severity: e.Severity})
	}
	return out
}

// notification describes a message, its alerts capped at maxLoggedAlerts.
func notification(alerts []Alert, outcome string, failed []string) Notification {
	n := Notification{Outcome: outcome, Failed: failed, Alerts: alerts}
	if len(alerts) > maxLoggedAlerts {
		n.Alerts, n.More = alerts[:maxLoggedAlerts], len(alerts)-maxLoggedAlerts
	}
	return n
}
