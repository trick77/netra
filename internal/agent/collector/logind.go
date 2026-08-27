package collector

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
)

// logind D-Bus names. The manager object answers ListSessions; each session it
// returns is its own object carrying the Class property this file filters on.
const (
	logindService    = "org.freedesktop.login1"
	logindPath       = dbus.ObjectPath("/org/freedesktop/login1")
	logindManager    = "org.freedesktop.login1.Manager"
	logindSessionIf  = "org.freedesktop.login1.Session"
	logindListMethod = logindManager + ".ListSessions"
	logindClassProp  = logindSessionIf + ".Class"
)

// humanSessionClasses are the session classes that mean a person is logged in.
//
// The list is an ALLOWLIST, and every class systemd defines is decided here
// rather than matched by prefix, because the taxonomy grows and a prefix rule
// would silently adopt whatever is added next:
//
//	user               a login             counted
//	user-early         a login before the user manager is up  counted
//	user-light         a login with no full user manager      counted
//	user-early-light   both of the above at once              counted
//	user-incomplete    PAM has not finished; there is not yet a session
//	greeter            the display manager's own login screen
//	lock-screen        a locked session's screen locker
//	background         `systemd --user` lingering, cron, at
//	background-light   the same, without a user manager
//	manager            the per-user service manager, NOT a login
//	manager-early      the same, early boot
//
// manager and manager-early are the reason a filter exists at all: systemd 256
// gives a single ssh login a `user` session AND a `manager` session, so an
// unfiltered count reports two people where there is one.
var humanSessionClasses = map[string]bool{
	"user":             true,
	"user-early":       true,
	"user-light":       true,
	"user-early-light": true,
}

// logindSession is one entry of ListSessions. Only the object path is used --
// the id, uid, user name and seat are decoded because the D-Bus signature
// requires it, and then dropped. Nothing about who is logged in leaves the
// agent; see the Users collector's own note.
type logindSession struct {
	ID   string
	UID  uint32
	User string
	Seat string
	Path dbus.ObjectPath
}

// LogindSessions is the production SessionLister. It counts the sessions
// logind considers human logins.
//
// logind rather than utmp, and it is not a preference: systemd 257 and the
// distributions shipping it (Ubuntu 25.10 onwards) build without utmp support
// at all, for the Y2038 overflow in its record format. On those hosts
// /run/utmp does not exist and never will, so the utmp parser reports
// "unavailable" for ever while people are logged in. logind is where the
// session list lives now.
//
// A fresh private connection per scrape, closed on the way out, for the same
// reason SystemUnits takes one: the call runs once a minute, so the cost is
// nothing next to smartctl, and a connection that is never reused cannot go
// stale when dbus or logind is restarted under the agent.
//
// The bus socket is the same mount the systemd collector already needs
// (/run/dbus/system_bus_socket). A host without it, or without logind, returns
// an error here and the Users collector falls back to utmp.
func LogindSessions(ctx context.Context) (int, error) {
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return 0, fmt.Errorf("connect to the system bus: %w", err)
	}
	defer conn.Close()

	var sessions []logindSession
	err = conn.Object(logindService, logindPath).
		CallWithContext(ctx, logindListMethod, 0).Store(&sessions)
	if err != nil {
		return 0, fmt.Errorf("list sessions: %w", err)
	}

	classes := make([]string, 0, len(sessions))
	for _, s := range sessions {
		class, err := sessionClass(conn, s.Path)
		if err != nil {
			// A session that ended between ListSessions and this read is
			// gone, not an error: its object is simply no longer on the
			// bus. Skipping it costs one session out of a count that the
			// next scrape recomputes from scratch, while failing the whole
			// scrape would fall back to utmp -- and on these hosts that
			// means reporting nothing at all.
			continue
		}
		classes = append(classes, class)
	}
	return countHumanSessions(classes), nil
}

// countHumanSessions applies the allowlist. Split from the bus call so the
// filter -- the only decision in this file -- is testable on a machine with
// no logind on it.
func countHumanSessions(classes []string) int {
	count := 0
	for _, class := range classes {
		if humanSessionClasses[class] {
			count++
		}
	}
	return count
}

// sessionClass reads one session's Class property.
//
// Read per session rather than taken from ListSessionsEx, which returns the
// class inline but exists only from systemd 256. The hosts that still have
// utmp are the older ones, so the newer method would work exactly where it is
// not needed and fail where it is.
func sessionClass(conn *dbus.Conn, path dbus.ObjectPath) (string, error) {
	v, err := conn.Object(logindService, path).GetProperty(logindClassProp)
	if err != nil {
		return "", err
	}
	class, ok := v.Value().(string)
	if !ok {
		return "", fmt.Errorf("session class is %T, not a string", v.Value())
	}
	return class, nil
}
