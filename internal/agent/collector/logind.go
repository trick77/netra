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
	propertiesGet    = "org.freedesktop.DBus.Properties.Get"
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

// sessionSource is the bus, seamed.
//
// Everything this file DECIDES -- which classes count, what a session that
// vanished mid-scrape means, what an unreadable class is -- sits behind this
// interface in countLogindSessions, so it is exercised on a machine with no
// logind on it. What remains on the other side is the D-Bus call itself,
// which no test can stand in for.
type sessionSource interface {
	sessionPaths(ctx context.Context) ([]dbus.ObjectPath, error)
	sessionClass(ctx context.Context, path dbus.ObjectPath) (string, error)
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

	return countLogindSessions(ctx, busSessions{conn: conn})
}

// countLogindSessions asks the source for every session and counts the human
// ones.
func countLogindSessions(ctx context.Context, src sessionSource) (int, error) {
	paths, err := src.sessionPaths(ctx)
	if err != nil {
		return 0, err
	}

	classes := make([]string, 0, len(paths))
	var lastErr error
	for _, path := range paths {
		class, err := src.sessionClass(ctx, path)
		if err != nil {
			// A session that ended between the listing and this read is
			// gone, not an error: its object is simply no longer on the
			// bus. Skipping it costs one session out of a count the next
			// scrape recomputes from scratch, while failing the whole
			// scrape would fall back to utmp -- and on these hosts that
			// means reporting nothing at all.
			lastErr = err
			continue
		}
		classes = append(classes, class)
	}

	// EVERY read failing is a different thing from one session ending, and it
	// must not be reported as a count. A bus that dropped after the listing,
	// or a policy that denies the Class property, would otherwise leave this
	// with an empty slice and answer "0 sessions, from logind" -- authoritative,
	// wrong, and never falling back to utmp, while people are logged in.
	if len(paths) > 0 && len(classes) == 0 {
		return 0, fmt.Errorf("read the class of any of %d sessions: %w", len(paths), lastErr)
	}

	return countHumanSessions(classes), nil
}

// countHumanSessions applies the allowlist.
func countHumanSessions(classes []string) int {
	count := 0
	for _, class := range classes {
		if humanSessionClasses[class] {
			count++
		}
	}
	return count
}

// sessionPathsOf drops everything ListSessions returns except the object
// paths. The id, user name and seat are decoded because the signature demands
// it and go no further than this function.
func sessionPathsOf(sessions []logindSession) []dbus.ObjectPath {
	paths := make([]dbus.ObjectPath, 0, len(sessions))
	for _, s := range sessions {
		paths = append(paths, s.Path)
	}
	return paths
}

// classOf reads the Class property's value out of its variant.
func classOf(value any) (string, error) {
	class, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("session class is %T, not a string", value)
	}
	return class, nil
}

// busSessions is the D-Bus half: the two calls, and nothing else.
type busSessions struct {
	conn *dbus.Conn
}

func (b busSessions) sessionPaths(ctx context.Context) ([]dbus.ObjectPath, error) {
	var sessions []logindSession
	err := b.conn.Object(logindService, logindPath).
		CallWithContext(ctx, logindListMethod, 0).Store(&sessions)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return sessionPathsOf(sessions), nil
}

// Read per session rather than taken from ListSessionsEx, which returns the
// class inline but exists only from systemd 256. The hosts that still have
// utmp are the older ones, so the newer method would work exactly where it is
// not needed and fail where it is.
func (b busSessions) sessionClass(ctx context.Context, path dbus.ObjectPath) (string, error) {
	// Properties.Get through CallWithContext rather than GetProperty, which
	// takes no context: this runs once per session, and a hung logind would
	// otherwise hold the scrape past its own deadline with nothing to cancel.
	var v dbus.Variant
	err := b.conn.Object(logindService, path).
		CallWithContext(ctx, propertiesGet, 0, logindSessionIf, "Class").Store(&v)
	if err != nil {
		return "", err
	}
	return classOf(v.Value())
}
