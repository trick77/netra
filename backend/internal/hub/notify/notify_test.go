package notify_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/notify"
)

type fakeStore struct {
	conds  []notify.Condition
	events []notify.Event
	marked []notify.Condition
	// condErr and eventErr fail the matching read.
	condErr, eventErr error
	pruned            int
	// sinceSeen is the lower bound of the last event read.
	sinceSeen time.Time
}

func (f *fakeStore) PendingConditions(context.Context, time.Time, time.Duration) ([]notify.Condition, error) {
	return f.conds, f.condErr
}

func (f *fakeStore) MarkNotified(_ context.Context, items []notify.Condition, _ time.Time) error {
	f.marked = append(f.marked, items...)
	// Marked conditions are no longer pending, as in the database.
	left := f.conds[:0:0]
	for _, c := range f.conds {
		done := false
		for _, m := range items {
			if m.ID == c.ID {
				done = true
			}
		}
		if !done {
			left = append(left, c)
		}
	}
	f.conds = left
	return nil
}

func (f *fakeStore) CriticalEventsSince(_ context.Context, _, since time.Time) ([]notify.Event, error) {
	f.sinceSeen = since
	var out []notify.Event
	for _, e := range f.events {
		if e.TS.After(since) {
			out = append(out, e)
		}
	}
	return out, f.eventErr
}

func (f *fakeStore) PruneMutes(context.Context, time.Time) error {
	f.pruned++
	return nil
}

type sent struct{ title, body string }

// fakeSender records messages. Hosts are sent concurrently, so it locks, and
// it keeps them sorted by title so a test can index them.
type fakeSender struct {
	mu   sync.Mutex
	sent []sent
	err  error
}

func (f *fakeSender) Send(title, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		var partial *notify.PartialError
		if !errors.As(f.err, &partial) {
			return f.err
		}
	}
	f.sent = append(f.sent, sent{title, body})
	sort.Slice(f.sent, func(i, j int) bool { return f.sent[i].title < f.sent[j].title })
	return f.err
}

var t0 = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func newNotifier(st *fakeStore, snd *fakeSender, now *time.Time) *notify.Notifier {
	n := notify.New(st, snd, "https://netra.example.com", 24*time.Hour)
	n.SetClockForTest(func() time.Time { return *now })
	return n
}

func disk(id, host int32, what notify.What) notify.Condition {
	return notify.Condition{
		ID: id, HostID: host, Hostname: fmt.Sprintf("web%d", host),
		Kind: "disk", Subject: "/var", Severity: "critical",
		OpenedTS: t0.Add(-time.Hour), What: what,
		Detail: map[string]any{"pct": 96.4},
	}
}

func diskError(id, host int32, ts time.Time) notify.Event {
	return notify.Event{
		ID: id, HostID: host, Hostname: fmt.Sprintf("web%d", host),
		TS: ts, Type: "disk_error", Subject: "sda", Severity: "critical",
		Detail: map[string]any{"message": "I/O error, dev sda, sector 42"},
	}
}

func TestOpenedConditionIsSentAndMarked(t *testing.T) {
	now := t0
	st := &fakeStore{conds: []notify.Condition{disk(1, 1, notify.Opened)}}
	snd := &fakeSender{}
	n := newNotifier(st, snd, &now)

	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(snd.sent))
	}
	if !strings.Contains(snd.sent[0].title, "web1") || !strings.Contains(snd.sent[0].title, "Filesystem nearly full /var") {
		t.Errorf("title = %q", snd.sent[0].title)
	}
	if !strings.Contains(snd.sent[0].body, "https://netra.example.com/hosts/1") {
		t.Errorf("body has no host link: %q", snd.sent[0].body)
	}
	if len(st.marked) != 1 || st.marked[0].ID != 1 {
		t.Errorf("marked = %+v", st.marked)
	}
	if st.pruned != 1 {
		t.Errorf("mutes pruned %d times, want 1", st.pruned)
	}

	// Marked means done: the next tick says nothing.
	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 1 {
		t.Errorf("second tick sent again: %d messages", len(snd.sent))
	}
}

func TestFailedSendStaysPending(t *testing.T) {
	now := t0
	st := &fakeStore{
		conds:  []notify.Condition{disk(1, 1, notify.Opened)},
		events: []notify.Event{},
	}
	snd := &fakeSender{err: errors.New("smtp down")}
	n := newNotifier(st, snd, &now)
	if err := n.Prime(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.events = []notify.Event{diskError(10, 1, now.Add(-time.Minute))}

	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.marked) != 0 {
		t.Fatalf("a failed send marked %+v", st.marked)
	}

	snd.err = nil
	now = now.Add(time.Minute)
	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 1 {
		t.Fatalf("retry sent %d messages, want 1", len(snd.sent))
	}
	body := snd.sent[0].body
	if !strings.Contains(body, "OPEN critical") || !strings.Contains(body, "EVENT critical: disk_error sda") {
		t.Errorf("retry lost a line: %q", body)
	}
}

func TestPartialFailureCountsAsSent(t *testing.T) {
	now := t0
	st := &fakeStore{conds: []notify.Condition{disk(1, 1, notify.Opened)}}
	snd := &fakeSender{err: &notify.PartialError{Err: errors.New("ntfy down")}}
	n := newNotifier(st, snd, &now)

	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.marked) != 1 {
		t.Errorf("a message one channel delivered was not marked: %+v", st.marked)
	}
}

func TestMutedConditions(t *testing.T) {
	now := t0
	open := disk(1, 1, notify.Opened)
	open.Muted = true
	cleared := disk(2, 1, notify.Cleared)
	cleared.Muted = true
	resolved := t0.Add(-time.Minute)
	cleared.ResolvedTS = &resolved
	st := &fakeStore{conds: []notify.Condition{open, cleared}}
	snd := &fakeSender{}
	n := newNotifier(st, snd, &now)

	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 0 {
		t.Fatalf("muted conditions sent %+v", snd.sent)
	}
	// The all-clear is dropped for good; the open one waits for the mute.
	if len(st.marked) != 1 || st.marked[0].ID != 2 {
		t.Errorf("marked = %+v, want only the muted all-clear", st.marked)
	}
}

func TestOneMessagePerHost(t *testing.T) {
	now := t0
	st := &fakeStore{conds: []notify.Condition{
		disk(1, 1, notify.Opened),
		disk(2, 2, notify.Escalated),
		disk(3, 1, notify.Reminder),
	}}
	snd := &fakeSender{}
	n := newNotifier(st, snd, &now)

	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 2 {
		t.Fatalf("sent %d messages, want one per host", len(snd.sent))
	}
	if !strings.Contains(snd.sent[0].title, "web1: 2 notifications") {
		t.Errorf("first title = %q", snd.sent[0].title)
	}
	if !strings.Contains(snd.sent[1].title, "WORSE critical") {
		t.Errorf("second title = %q", snd.sent[1].title)
	}
}

func TestEventsArePrimedThrottledAndMuted(t *testing.T) {
	now := t0
	old := diskError(1, 1, now.Add(-5*time.Minute))
	st := &fakeStore{events: []notify.Event{old}}
	snd := &fakeSender{}
	n := newNotifier(st, snd, &now)

	// What was in the window at start-up is not news.
	if err := n.Prime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 0 {
		t.Fatalf("a primed event was sent: %+v", snd.sent)
	}
	if want := now.Add(-notify.Freshness); !st.sinceSeen.Equal(want) {
		t.Errorf("events read since %v, want %v", st.sinceSeen, want)
	}

	// A new one is.
	now = now.Add(time.Minute)
	st.events = append(st.events, diskError(2, 1, now.Add(-10*time.Second)))
	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 1 {
		t.Fatalf("sent %d, want the new event", len(snd.sent))
	}

	// The same disk again inside the cooldown is not.
	now = now.Add(10 * time.Minute)
	st.events = append(st.events, diskError(3, 1, now.Add(-10*time.Second)))
	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 1 {
		t.Fatalf("cooldown let through %d messages", len(snd.sent))
	}

	// A muted host's event is not either.
	muted := diskError(4, 2, now.Add(-5*time.Second))
	muted.Muted = true
	st.events = append(st.events, muted)
	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 1 {
		t.Fatalf("a muted event was sent")
	}

	// Past the cooldown the disk speaks again.
	now = now.Add(notify.Cooldown)
	st.events = []notify.Event{diskError(5, 1, now.Add(-time.Second)), diskError(6, 1, now.Add(-time.Second))}
	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 2 {
		t.Fatalf("sent %d after the cooldown, want 2", len(snd.sent))
	}
	// Two events about one disk in one tick are one line.
	if c := strings.Count(snd.sent[1].body, "disk_error"); c != 1 {
		t.Errorf("one disk took %d lines: %q", c, snd.sent[1].body)
	}
}

func TestReadErrorsAreReturned(t *testing.T) {
	now := t0
	snd := &fakeSender{}

	st := &fakeStore{condErr: errors.New("db gone")}
	if err := newNotifier(st, snd, &now).Once(context.Background()); err == nil {
		t.Error("condition read error was swallowed")
	}
	st = &fakeStore{}
	n := newNotifier(st, snd, &now)
	if err := n.Prime(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.eventErr = errors.New("db gone")
	if err := n.Once(context.Background()); err == nil {
		t.Error("event read error was swallowed")
	}
	if err := newNotifier(st, snd, &now).Prime(context.Background()); err == nil {
		t.Error("prime read error was swallowed")
	}
}

// A prime that failed at start-up is retried by the tick, and until it
// succeeds no event goes out: an unprimed notifier would resend the previous
// process's window. Conditions are not held back by it.
func TestUnprimedNotifierHoldsEventsNotConditions(t *testing.T) {
	now := t0
	st := &fakeStore{
		conds:    []notify.Condition{disk(1, 1, notify.Opened)},
		events:   []notify.Event{diskError(1, 1, now.Add(-time.Minute))},
		eventErr: errors.New("db gone"),
	}
	snd := &fakeSender{}
	n := newNotifier(st, snd, &now)
	if err := n.Prime(context.Background()); err == nil {
		t.Fatal("prime should fail")
	}

	if err := n.Once(context.Background()); err == nil {
		t.Fatal("the failed prime was not reported")
	}
	if len(snd.sent) != 1 || strings.Contains(snd.sent[0].body, "EVENT") {
		t.Fatalf("unprimed tick sent %+v, want the condition alone", snd.sent)
	}

	// An event lands after this process started, before the database is back.
	st.events = append(st.events, diskError(2, 2, t0.Add(30*time.Second)))

	// The database is back: this tick primes. The event from before the
	// start stays quiet; the one after it is news.
	st.eventErr = nil
	now = now.Add(time.Minute)
	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := n.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 2 || !strings.Contains(snd.sent[1].title, "web2: EVENT") {
		t.Fatalf("after priming sent %+v, want only the post-start event", snd.sent)
	}
}

// A failing condition read does not hold back events, nor the other way
// round; the failure is still reported.
func TestReadsAreIndependent(t *testing.T) {
	now := t0
	st := &fakeStore{}
	snd := &fakeSender{}
	n := newNotifier(st, snd, &now)
	if err := n.Prime(context.Background()); err != nil {
		t.Fatal(err)
	}

	st.condErr = errors.New("conditions gone")
	st.events = []notify.Event{diskError(1, 1, now.Add(time.Second))}
	now = now.Add(time.Minute)
	if err := n.Once(context.Background()); err == nil {
		t.Error("the condition read error was swallowed")
	}
	if len(snd.sent) != 1 {
		t.Fatalf("the event waited for the conditions: %+v", snd.sent)
	}

	st.condErr = nil
	st.eventErr = errors.New("events gone")
	st.conds = []notify.Condition{disk(1, 2, notify.Opened)}
	if err := n.Once(context.Background()); err == nil {
		t.Error("the event read error was swallowed")
	}
	if len(snd.sent) != 2 {
		t.Fatalf("the condition waited for the events: %+v", snd.sent)
	}
}

func TestRunStopsWithContext(t *testing.T) {
	now := t0
	st := &fakeStore{}
	n := newNotifier(st, &fakeSender{}, &now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { n.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
