package collector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/trick77/netra/internal/agent/collector"
)

// fakeObject is a dbus.BusObject that answers with a canned reply. Only
// CallWithContext is ever reached by the collector; the rest of the interface
// is here because Go requires it and panics if any of it is used, which is the
// loudest way to find out that assumption stopped holding.
type fakeObject struct {
	body []any
	err  error
}

func (f fakeObject) CallWithContext(_ context.Context, _ string, _ dbus.Flags, _ ...any) *dbus.Call {
	return &dbus.Call{Body: f.body, Err: f.err}
}

func (f fakeObject) Call(string, dbus.Flags, ...any) *dbus.Call { panic("unused") }

func (f fakeObject) Go(string, dbus.Flags, chan *dbus.Call, ...any) *dbus.Call {
	panic("unused")
}

func (f fakeObject) GoWithContext(context.Context, string, dbus.Flags, chan *dbus.Call, ...any) *dbus.Call {
	panic("unused")
}

func (f fakeObject) AddMatchSignal(string, string, ...dbus.MatchOption) *dbus.Call {
	panic("unused")
}

func (f fakeObject) RemoveMatchSignal(string, string, ...dbus.MatchOption) *dbus.Call {
	panic("unused")
}

func (f fakeObject) GetProperty(string) (dbus.Variant, error) { panic("unused") }
func (f fakeObject) StoreProperty(string, any) error          { panic("unused") }
func (f fakeObject) SetProperty(string, any) error            { panic("unused") }
func (f fakeObject) Destination() string                      { return "org.freedesktop.login1" }
func (f fakeObject) Path() dbus.ObjectPath                    { return "/org/freedesktop/login1" }

// The reply signature logind actually sends: a(susso) -- session id, uid,
// user name, seat, object path. Only the path survives this decode, and the
// test asserts exactly that: the names and seats in the reply are read because
// the signature demands it and go no further.
func TestLogindDecodesTheSessionListing(t *testing.T) {
	reply := []any{[]struct {
		ID   string
		UID  uint32
		User string
		Seat string
		Path dbus.ObjectPath
	}{
		{"c1", 1000, "jan", "seat0", "/org/freedesktop/login1/session/c1"},
		{"c2", 0, "root", "", "/org/freedesktop/login1/session/c2"},
	}}

	src := collector.BusSessionsForTest(func(dbus.ObjectPath) dbus.BusObject {
		return fakeObject{body: reply}
	})

	paths, err := src.Paths(context.Background())
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	want := []dbus.ObjectPath{
		"/org/freedesktop/login1/session/c1",
		"/org/freedesktop/login1/session/c2",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i, p := range paths {
		if p != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, p, want[i])
		}
	}
}

// A bus error on the listing reaches the caller, which is what routes the
// collector to its utmp fallback instead of reporting zero sessions.
func TestLogindListingErrorIsReported(t *testing.T) {
	src := collector.BusSessionsForTest(func(dbus.ObjectPath) dbus.BusObject {
		return fakeObject{err: errors.New("the name org.freedesktop.login1 was not provided")}
	})

	if _, err := src.Paths(context.Background()); err == nil {
		t.Error("a bus error on ListSessions must reach the caller")
	}
}

// GetAll answers with the session's whole property map. Class and State are
// the only two entries this collector reads out of it.
func TestLogindReadsClassAndState(t *testing.T) {
	src := collector.BusSessionsForTest(func(dbus.ObjectPath) dbus.BusObject {
		return fakeObject{body: []any{map[string]dbus.Variant{
			"Id":     dbus.MakeVariant("c1"),
			"Class":  dbus.MakeVariant("user"),
			"State":  dbus.MakeVariant("closing"),
			"Remote": dbus.MakeVariant(true),
		}}}
	})

	got, err := src.Info(context.Background(), "/org/freedesktop/login1/session/c1")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if got.Class != "user" || got.State != "closing" {
		t.Errorf("session = %+v, want class user and state closing", got)
	}
}

// A property map missing one of the two is an error, not a zero value: an
// empty class would silently read as "not a human session" and an empty state
// as "not closing", moving the count in both directions without saying so.
func TestLogindRefusesAPropertyMapMissingWhatItNeeds(t *testing.T) {
	for _, props := range []map[string]dbus.Variant{
		{"State": dbus.MakeVariant("active")},
		{"Class": dbus.MakeVariant("user")},
	} {
		src := collector.BusSessionsForTest(func(dbus.ObjectPath) dbus.BusObject {
			return fakeObject{body: []any{props}}
		})
		if _, err := src.Info(context.Background(), "/org/freedesktop/login1/session/c1"); err == nil {
			t.Errorf("a map missing Class or State must be an error: %v", props)
		}
	}
}

// A session that ended between the listing and this read answers with an
// error rather than a property map. countLogindSessions is what decides that
// is a skip; this asserts the error gets there at all.
func TestLogindPropertyErrorIsReported(t *testing.T) {
	src := collector.BusSessionsForTest(func(dbus.ObjectPath) dbus.BusObject {
		return fakeObject{err: errors.New("unknown object path")}
	})

	if _, err := src.Info(context.Background(), "/org/freedesktop/login1/session/c1"); err == nil {
		t.Error("a bus error on the property read must reach the caller")
	}
}

// A variant holding something other than a string means the object is not the
// session this parser believes it is.
func TestLogindRefusesANonStringPropertyFromTheBus(t *testing.T) {
	src := collector.BusSessionsForTest(func(dbus.ObjectPath) dbus.BusObject {
		return fakeObject{body: []any{map[string]dbus.Variant{
			"Class": dbus.MakeVariant(uint32(7)),
			"State": dbus.MakeVariant("active"),
		}}}
	})

	if _, err := src.Info(context.Background(), "/org/freedesktop/login1/session/c1"); err == nil {
		t.Error("a non-string class must be an error rather than a counted session")
	}
}
