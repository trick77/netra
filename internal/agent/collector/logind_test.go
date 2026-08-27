package collector_test

import (
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
)

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
