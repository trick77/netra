package collector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/trick77/netra/internal/agent/collector"
)

// fakeBus stands in for logind: a set of sessions, each with a class, and the
// option of failing either call the way a real bus does.
type fakeBus struct {
	classes  map[string]string
	listErr  error
	classErr map[string]error
}

func (f fakeBus) Paths(context.Context) ([]dbus.ObjectPath, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	ids := make([]string, 0, len(f.classes))
	for id := range f.classes {
		ids = append(ids, id)
	}
	return collector.SessionPathsOfForTest(ids), nil
}

func (f fakeBus) Class(_ context.Context, path dbus.ObjectPath) (string, error) {
	id := string(path[len("/org/freedesktop/login1/session/"):])
	if err, ok := f.classErr[id]; ok {
		return "", err
	}
	return f.classes[id], nil
}

// The session-class allowlist, class by class.
//
// Every class systemd defines appears here, including the ones that must NOT
// be counted, because a table that lists only the counted ones passes just as
// happily when the filter is deleted.
func TestLogindCountsOnlyHumanSessionClasses(t *testing.T) {
	for _, tc := range []struct {
		class string
		want  int
	}{
		{"user", 1},
		{"user-early", 1},
		{"user-light", 1},
		{"user-early-light", 1},
		// PAM has not finished; there is not yet a session to count.
		{"user-incomplete", 0},
		// The display manager's own login screen: nobody is logged in yet.
		{"greeter", 0},
		{"lock-screen", 0},
		// `systemd --user` lingering, cron, at -- not a person.
		{"background", 0},
		{"background-light", 0},
		// The per-user service manager. This is the one that matters: from
		// systemd 256 a single ssh login also gets one of these, so counting
		// it reports two people where there is one.
		{"manager", 0},
		{"manager-early", 0},
		// A class this build has never heard of is not a login. The
		// taxonomy grows, and the wrong direction to fail in is upwards.
		{"something-new", 0},
	} {
		t.Run(tc.class, func(t *testing.T) {
			if got := collector.CountHumanSessionsForTest([]string{tc.class}); got != tc.want {
				t.Errorf("count of %q = %d, want %d", tc.class, got, tc.want)
			}
		})
	}
}

// One ssh login on a systemd 256+ host is a `user` session AND a `manager`
// session, plus whatever else the machine is running. The count is 1.
func TestLogindCountsOneLoginOnce(t *testing.T) {
	sessions := []string{"user", "manager", "background", "greeter"}

	if got := collector.CountHumanSessionsForTest(sessions); got != 1 {
		t.Errorf("count = %d, want 1 -- the manager session is the same login", got)
	}
}

// Nobody logged in is 0, not an error and not an absent reading: logind
// answered, and the answer is zero.
func TestLogindCountsNoSessions(t *testing.T) {
	if got := collector.CountHumanSessionsForTest(nil); got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
}

// The whole path, against a fake bus: what logind lists, what each session's
// class is, and the number that leaves the collector.
func TestLogindCountsWhatTheBusReports(t *testing.T) {
	// One ssh login (user + its manager), a display manager greeter, and a
	// lingering user service. One person is logged in.
	bus := fakeBus{classes: map[string]string{
		"c1": "user",
		"c2": "manager",
		"c3": "greeter",
		"c4": "background",
	}}

	got, err := collector.CountLogindSessionsForTest(context.Background(), bus)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 1 {
		t.Errorf("count = %d, want 1", got)
	}
}

// A session that ended between the listing and the class read is gone, not a
// failure. Failing the whole scrape would fall back to utmp, and on a host
// with no utmp that means reporting nothing at all.
func TestLogindSkipsASessionThatVanishedMidScrape(t *testing.T) {
	bus := fakeBus{
		classes:  map[string]string{"c1": "user", "c2": "user"},
		classErr: map[string]error{"c2": errors.New("unknown object path")},
	}

	got, err := collector.CountLogindSessionsForTest(context.Background(), bus)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 1 {
		t.Errorf("count = %d, want 1 -- the vanished session is skipped, not fatal", got)
	}
}

// A bus that cannot be listed at all IS fatal: that is the "no logind here"
// case, and it has to reach the Users collector so it falls back to utmp
// rather than reporting zero sessions.
func TestLogindReportsAFailedListing(t *testing.T) {
	bus := fakeBus{listErr: errors.New("the name org.freedesktop.login1 was not provided")}

	if _, err := collector.CountLogindSessionsForTest(context.Background(), bus); err == nil {
		t.Error("a failed listing must be an error, or the collector reports zero " +
			"sessions on a host that simply has no logind")
	}
}

// Nobody logged in is a real answer -- zero, no error -- and it must not look
// like a failure, or a host with utmp would silently fall back to it.
func TestLogindCountsAnEmptyBusAsZero(t *testing.T) {
	got, err := collector.CountLogindSessionsForTest(context.Background(), fakeBus{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
}

// The class property is a variant. Anything that is not a string means the
// object is not the session this parser believes it is.
func TestLogindRefusesANonStringClass(t *testing.T) {
	if got, err := collector.ClassOfForTest("user"); err != nil || got != "user" {
		t.Errorf("classOf(\"user\") = %q, %v; want user, nil", got, err)
	}
	if _, err := collector.ClassOfForTest(uint32(7)); err == nil {
		t.Error("a non-string class must be an error rather than a counted session")
	}
}

// One session vanishing is a skip; EVERY class read failing is not. A bus that
// dropped after the listing, or a policy denying the Class property, would
// otherwise report "0 sessions, from logind" -- authoritative, wrong, and with
// no fallback to utmp while people are logged in.
func TestLogindReportsFailingEveryClassRead(t *testing.T) {
	denied := errors.New("access denied")
	bus := fakeBus{
		classes: map[string]string{"c1": "user", "c2": "user"},
		classErr: map[string]error{
			"c1": denied,
			"c2": denied,
		},
	}

	if _, err := collector.CountLogindSessionsForTest(context.Background(), bus); err == nil {
		t.Error("every class read failing must be an error, not a count of zero")
	}
}
