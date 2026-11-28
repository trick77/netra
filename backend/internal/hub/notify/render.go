package notify

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/trick77/netra/internal/hub/conditions"
)

// labels are the condition kinds by the names the catalogue gives them, the
// same words the UI prints.
var labels = func() map[string]string {
	m := map[string]string{}
	for _, k := range conditions.Catalogue() {
		m[k.Kind] = k.Label
	}
	return m
}()

// Render builds one host's message: a subject line and a plain-text body
// with one line per condition or event, and a link to the host page.
//
// Every line is cleaned of control characters. Event messages are kernel log
// text the agent unescaped, so a process name can carry a CR/LF, and the
// title becomes a mail Subject header: unclean, it would inject headers.
func Render(hostname string, hostID int32, conds []Condition, events []Event, hubURL string, now time.Time) (title, body string) {
	hostname = clean(hostname)
	var lines []string
	for _, c := range conds {
		lines = append(lines, clean(conditionLine(c, now)))
	}
	for _, e := range events {
		lines = append(lines, clean(eventLine(e)))
	}

	if len(lines) == 1 {
		title = fmt.Sprintf("[Netra] %s: %s", hostname, lines[0])
	} else {
		title = fmt.Sprintf("[Netra] %s: %d notifications", hostname, len(lines))
	}

	var sb strings.Builder
	sb.WriteString(hostname)
	sb.WriteString("\n\n")
	for _, l := range lines {
		sb.WriteString("- ")
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	if hubURL != "" {
		fmt.Fprintf(&sb, "\n%s/hosts/%d\n", hubURL, hostID)
	}
	return title, sb.String()
}

func conditionLine(c Condition, now time.Time) string {
	what := labels[c.Kind]
	if what == "" {
		what = c.Kind
	}
	if c.Subject != "" {
		what += " " + c.Subject
	}
	if note := detailNote(c.Kind, c.Detail); note != "" {
		what += " (" + note + ")"
	}

	// An onset that is a floor (the walk back hit the end of retention) is
	// said as one, as the UI says "over 7 d": naming the floor as a moment
	// would report an onset that never happened.
	over, before := "", ""
	if c.OpenedAtLeast {
		over, before = "over ", "before "
	}

	switch c.What {
	case Escalated:
		return fmt.Sprintf("WORSE %s: %s", c.Severity, what)
	case Reminder:
		return fmt.Sprintf("STILL OPEN %s: %s, open for %s%s", c.Severity, what, over, Duration(now.Sub(c.OpenedTS)))
	case Cleared:
		how := "CLEARED"
		if c.ResolvedReason == conditions.ReasonVanished {
			how = "NO LONGER REPORTED"
		}
		end := now
		if c.ResolvedTS != nil {
			end = *c.ResolvedTS
		}
		return fmt.Sprintf("%s: %s, after %s%s", how, what, over, Duration(end.Sub(c.OpenedTS)))
	default:
		return fmt.Sprintf("OPEN %s: %s, since %s%s", c.Severity, what, before, c.OpenedTS.UTC().Format("2006-01-02 15:04 UTC"))
	}
}

// clean replaces every control character with a space.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func eventLine(e Event) string {
	what := e.Type
	if e.Subject != "" {
		what += " " + e.Subject
	}
	line := fmt.Sprintf("EVENT %s: %s at %s", e.Severity, what, e.TS.UTC().Format("15:04:05 UTC"))
	if msg, ok := e.Detail["message"].(string); ok && msg != "" {
		line += ": " + truncate(msg, 300)
	}
	return line
}

// detailNote is the one number a reader wants beside each kind, when the
// condition carries it.
func detailNote(kind string, d map[string]any) string {
	switch kind {
	case conditions.KindDisk:
		if pct, ok := number(d["pct"]); ok {
			return fmt.Sprintf("%.0f%% used", pct)
		}
	case conditions.KindFailedUnits:
		if units, ok := d["units"].([]any); ok && len(units) > 0 {
			names := make([]string, 0, len(units))
			for _, u := range units {
				if s, ok := u.(string); ok {
					names = append(names, s)
				}
			}
			return strings.Join(names, ", ")
		}
		if n, ok := number(d["count"]); ok {
			return fmt.Sprintf("%.0f failed", n)
		}
	case conditions.KindDrive:
		if t, ok := d["text"].(string); ok {
			return truncate(t, 200)
		}
	case conditions.KindTemperature, conditions.KindProcesses, conditions.KindLoad:
		note := ""
		if v, ok := number(d["value"]); ok {
			note = trim(v)
			if normal, ok := number(d["normal"]); ok {
				note = fmt.Sprintf("%s, normal under %s", trim(v), trim(normal))
			}
		}
		// The md arrays at work while the load is high (store/deviationscan.go):
		// a scrub waits on disks, and the wait counts as load.
		if syncing := mdSync(d); syncing != "" {
			if note != "" {
				note += "; "
			}
			note += syncing + " running"
		}
		return note
	}
	return ""
}

// mdSync is a detail's md_sync list as one phrase, "md1 check, md2 check", or
// "" when it carries none.
func mdSync(d map[string]any) string {
	if names, ok := d["md_sync"].([]string); ok {
		return strings.Join(names, ", ")
	}
	list, _ := d["md_sync"].([]any)
	names := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok && s != "" {
			names = append(names, s)
		}
	}
	return strings.Join(names, ", ")
}

func number(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok && !math.IsNaN(f) && !math.IsInf(f, 0)
}

func trim(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", f), "0"), ".")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Duration renders d the way the UI prints ages: "2 d 4 h", "3 h 12 m",
// "45 m", "30 s".
func Duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d / (24 * time.Hour))
	hours := int(d % (24 * time.Hour) / time.Hour)
	mins := int(d % time.Hour / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%d d %d h", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d h %d m", hours, mins)
	case mins > 0:
		return fmt.Sprintf("%d m", mins)
	default:
		return fmt.Sprintf("%d s", int(d/time.Second))
	}
}
