package collector_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// rec builds one /dev/kmsg record in the kernel's own wire format.
func rec(prio int, seq uint64, usec int64, msg string) []byte {
	return []byte(fmt.Sprintf("%d,%d,%d,-;%s", prio, seq, usec, msg))
}

// kmsgFixture drives the collector over a fixed set of records with a fixed
// clock, and returns the events one Collect produced.
func kmsgFixture(t *testing.T, records [][]byte, now time.Time, up time.Duration) (*collector.Kmsg, []*netrav1.Event) {
	t.Helper()
	testee := collector.NewKmsg("/dev/kmsg", "")
	testee.SetSourceForTest(
		func() (collector.KmsgSource, error) { return collector.NewSliceSource(records, -1), nil },
		func() (time.Duration, error) { return up, nil },
		func() time.Time { return now },
	)
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return testee, res.Events
}

func detailOf(t *testing.T, ev *netrav1.Event) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal([]byte(ev.GetDetailJson()), &d); err != nil {
		t.Fatalf("detail is not valid JSON: %v (%q)", err, ev.GetDetailJson())
	}
	return d
}

// liveSource models the real device: opened ONCE and drained on every scrape,
// with whatever the kernel has written since. A source rebuilt per Collect
// would not -- the collector holds its handle across scrapes precisely so the
// read position survives.
type liveSource struct{ pending *[][]byte }

func (l *liveSource) ReadRecord() ([]byte, error) {
	if len(*l.pending) == 0 {
		return nil, io.EOF
	}
	rec := (*l.pending)[0]
	*l.pending = (*l.pending)[1:]
	return rec, nil
}

func (l *liveSource) Close() error { return nil }

// Every line here is verbatim from a real host's ring buffer or from the kernel
// source that emits it. A classifier tested only against messages invented to
// match it proves nothing.
func TestKmsgClassifiesRealKernelMessages(t *testing.T) {
	cases := []struct {
		name    string
		msg     string
		typ     string
		subject string
		sev     string
	}{
		{
			name:    "block layer I/O error",
			msg:     "blk_update_request: I/O error, dev sdd, sector 13211246 op 0x0:(READ) flags 0x0 phys_seg 20 prio class 0",
			typ:     "disk_error",
			subject: "sdd",
			sev:     "critical",
		},
		{
			name:    "ata exception",
			msg:     "ata4.00: exception Emask 0x0 SAct 0x780381ff SErr 0x0 action 0x0",
			typ:     "ata_error",
			subject: "ata4.00",
			sev:     "critical",
		},
		{
			name:    "ata uncorrectable",
			msg:     "ata4.00: error: { UNC }",
			typ:     "ata_error",
			subject: "ata4.00",
			sev:     "critical",
		},
		{
			name:    "fat volume not unmounted",
			msg:     "FAT-fs (sdd1): Volume was not properly unmounted. Some data may be corrupt. Please run fsck.",
			typ:     "fs_error",
			subject: "sdd1",
			sev:     "critical",
		},
		{
			name:    "ext4 remount read-only",
			msg:     "EXT4-fs (dm-0): Remounting filesystem read-only",
			typ:     "fs_error",
			subject: "dm-0",
			sev:     "critical",
		},
		{
			name:    "ext4 error",
			msg:     "EXT4-fs error (device sda1): ext4_find_entry:1455: inode #2: comm ls: reading directory lblock 0",
			typ:     "fs_error",
			subject: "sda1",
			sev:     "critical",
		},
		{
			name:    "md disk failure names the disk sysfs cannot",
			msg:     "md/raid1:md3: Disk failure on sdb1, disabling device.",
			typ:     "md_fail",
			subject: "md3",
			sev:     "critical",
		},
		{
			name:    "md non-fresh member",
			msg:     "md: kicking non-fresh sdb1 from array!",
			typ:     "md_fail",
			subject: "sdb1",
			sev:     "critical",
		},
		{
			name:    "oom killer",
			msg:     "Out of memory: Killed process 4242 (postgres) total-vm:9999kB, anon-rss:8888kB",
			typ:     "oom_kill",
			subject: "postgres",
			sev:     "critical",
		},
		{
			name:    "machine check",
			msg:     "mce: [Hardware Error]: Machine check events logged",
			typ:     "hw_error",
			subject: "",
			sev:     "critical",
		},
		{
			name:    "thermal throttle",
			msg:     "CPU2: Core temperature above threshold, cpu clock throttled (total events = 1)",
			typ:     "thermal",
			subject: "CPU2",
			sev:     "warning",
		},
		{
			name:    "soft lockup",
			msg:     "watchdog: BUG: soft lockup - CPU#3 stuck for 23s! [kworker/3:1:123]",
			typ:     "kernel_fault",
			subject: "",
			sev:     "critical",
		},
		{
			name:    "nvme controller reset",
			msg:     "nvme nvme0: I/O 259 QID 3 timeout, resetting controller",
			typ:     "nvme_error",
			subject: "nvme0",
			sev:     "critical",
		},
		{
			name:    "physical link down",
			msg:     "e1000e 0000:00:1f.6 eno1: NIC Link is Down",
			typ:     "link_change",
			subject: "eno1",
			sev:     "info",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1_800_000_000, 0).UTC()
			_, events := kmsgFixture(t, [][]byte{rec(3, 1, 1_000_000, tc.msg)}, now, time.Hour)

			if len(events) != 1 {
				t.Fatalf("events = %d, want 1 for %q", len(events), tc.msg)
			}
			ev := events[0]
			if ev.GetType() != tc.typ {
				t.Errorf("type = %q, want %q", ev.GetType(), tc.typ)
			}
			if ev.GetSubject() != tc.subject {
				t.Errorf("subject = %q, want %q", ev.GetSubject(), tc.subject)
			}
			if got := detailOf(t, ev)["severity"]; got != tc.sev {
				t.Errorf("severity = %v, want %q", got, tc.sev)
			}
		})
	}
}

// The allowlist's whole purpose: what a busy host actually spams must produce
// nothing at all.
//
// These are the highest-frequency lines in the ring buffers of three real
// hosts. A priority filter would have kept some of them and dropped the md and
// link lines that matter; the allowlist keeps the question from arising.
func TestKmsgDropsTheNoiseThatFillsARealRingBuffer(t *testing.T) {
	noise := []string{
		"br-1da1d3c: port 3(veth9f3) entered disabled state",
		"br-1da1d3c: port 3(veth9f3) entered blocking state",
		"br-1da1d3c: port 3(veth9f3) entered forwarding state",
		"veth9f3: entered promiscuous mode",
		"veth9f3 (unregistering): left allmulticast mode",
		"veth9f3: renamed from eth0",
		"eth0: renamed from veth9f3",
		"docker0: port 1(veth2e5d3) entered disabled state",
		"audit: type=1400 audit(1756.1:22): apparmor=\"DENIED\" operation=\"ptrace\" class=\"ptrace\" profile=\"docker-default\" pid=1 comm=\"netra-agent\" requested_mask=\"read\" denied_mask=\"read\" peer=\"unconfined\"",
		"systemd-journald[266]: /var/log/journal/1d/system.journal: Journal header limits reached or header out-of-date, rotating.",
		"ata4.00: configured for UDMA/133",
		"perf: interrupt took too long (2503 > 2500), lowering kernel.perf_event_max_sample_rate to 79000",
		"usb 1-3: New USB device found, idVendor=1d6b, idProduct=0002, bcdDevice= 6.12",
		"ACPI Warning: SystemIO range 0x0000000000001828-0x000000000000182F conflicts with OpRegion",
		"device-mapper: core: CONFIG_IMA_DISABLE_HTABLE is disabled.",
		"IPv6: ADDRCONF(NETDEV_CHANGE): veth9f3: link becomes ready",
	}

	records := make([][]byte, 0, len(noise))
	for i, msg := range noise {
		records = append(records, rec(6, uint64(i), int64(i)*1000, msg))
	}

	_, events := kmsgFixture(t, records, time.Unix(1_800_000_000, 0).UTC(), time.Hour)
	if len(events) != 0 {
		t.Fatalf("noise produced %d events, want none: %+v", len(events), events)
	}
}

// The md scrub is left to the mdraid collector.
//
// mdraid already emits one event when sync_action leaves idle and one when it
// returns, from sysfs and with no device grant. Classifying the kernel's own
// scrub lines too would put four rows on the page for one scrub.
func TestKmsgLeavesTheScrubToTheMdraidCollector(t *testing.T) {
	records := [][]byte{
		rec(6, 1, 1_000, "md: data-check of RAID array md3"),
		rec(6, 2, 2_000, "md: md3: data-check done."),
	}
	_, events := kmsgFixture(t, records, time.Unix(1_800_000_000, 0).UTC(), time.Hour)
	if len(events) != 0 {
		t.Fatalf("scrub lines produced %d events, want none: %+v", len(events), events)
	}
}

// A burst about one device is one event with a count.
//
// A real ATA incident is a dozen sentences about one disk inside a few seconds,
// and a log that renders each of them separately buries the incident in its own
// description of itself.
func TestKmsgFoldsABurstAboutOneDeviceIntoOneEvent(t *testing.T) {
	records := [][]byte{
		rec(3, 1, 1_000_000, "blk_update_request: I/O error, dev sdd, sector 13211246 op 0x0:(READ) flags 0x0 phys_seg 20 prio class 0"),
		rec(3, 2, 2_000_000, "blk_update_request: I/O error, dev sdd, sector 13211610 op 0x0:(READ) flags 0x0 phys_seg 1 prio class 0"),
		rec(3, 3, 3_000_000, "blk_update_request: I/O error, dev sdd, sector 13377300 op 0x0:(READ) flags 0x0 phys_seg 12 prio class 0"),
		rec(3, 4, 4_000_000, "blk_update_request: I/O error, dev sdd, sector 13424407 op 0x0:(READ) flags 0x0 phys_seg 6 prio class 0"),
	}

	_, events := kmsgFixture(t, records, time.Unix(1_800_000_000, 0).UTC(), time.Hour)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 folded event", len(events))
	}
	if got := detailOf(t, events[0])["count"]; got != float64(4) {
		t.Errorf("count = %v, want 4", got)
	}
}

// Two devices in one burst stay two events.
//
// Folding is keyed on what the event is ABOUT, so an incident touching sdd and
// sde is two incidents. It also has to be deterministic: the same drain must
// emit the same order twice.
func TestKmsgKeepsSeparateDevicesApartAndOrdersThemDeterministically(t *testing.T) {
	records := [][]byte{
		rec(3, 1, 1_000_000, "blk_update_request: I/O error, dev sde, sector 1 op 0x0:(READ) flags 0x0 phys_seg 1 prio class 0"),
		rec(3, 2, 2_000_000, "blk_update_request: I/O error, dev sdd, sector 2 op 0x0:(READ) flags 0x0 phys_seg 1 prio class 0"),
	}

	_, events := kmsgFixture(t, records, time.Unix(1_800_000_000, 0).UTC(), time.Hour)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].GetSubject() != "sdd" || events[1].GetSubject() != "sde" {
		t.Errorf("subjects = %q, %q; want sdd then sde (sorted)",
			events[0].GetSubject(), events[1].GetSubject())
	}
}

// A sustained incident speaks once, then keeps count.
//
// A dying disk emits for hours. The second identical row tells an operator
// nothing the first did not, so the key goes quiet -- and what it withheld is
// reported rather than dropped, which is the difference between folding and
// hiding.
func TestKmsgSuppressesARepeatAndReportsWhatItWithheld(t *testing.T) {
	line := "blk_update_request: I/O error, dev sdd, sector 42 op 0x0:(READ) flags 0x0 phys_seg 1 prio class 0"
	start := time.Unix(1_800_000_000, 0).UTC()

	testee := collector.NewKmsg("/dev/kmsg", "")
	now := start
	var records [][]byte
	testee.SetSourceForTest(
		func() (collector.KmsgSource, error) { return &liveSource{pending: &records}, nil },
		func() (time.Duration, error) { return time.Hour, nil },
		func() time.Time { return now },
	)

	drain := func() []*netrav1.Event {
		t.Helper()
		res, err := testee.Collect(context.Background())
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		return res.Events
	}

	records = [][]byte{rec(3, 1, 1_000_000, line)}
	if got := drain(); len(got) != 1 {
		t.Fatalf("first scrape events = %d, want 1", len(got))
	}

	// A minute later, still inside the quiet window.
	now = start.Add(time.Minute)
	records = [][]byte{rec(3, 2, 2_000_000, line), rec(3, 3, 3_000_000, line)}
	if got := drain(); len(got) != 0 {
		t.Fatalf("inside the quiet window events = %d, want 0", len(got))
	}

	// Past the window: it speaks again, carrying what it held.
	now = start.Add(collector.KmsgSuppressWindowForTest + time.Minute)
	records = [][]byte{rec(3, 4, 4_000_000, line)}
	got := drain()
	if len(got) != 1 {
		t.Fatalf("after the quiet window events = %d, want 1", len(got))
	}
	if s := detailOf(t, got[0])["suppressed"]; s != float64(2) {
		t.Errorf("suppressed = %v, want the 2 records held during the window", s)
	}
}

// The tail of an incident is not lost.
//
// A disk that threw errors and then went quiet had them folded into a window
// nobody speaks for again. Without an expiry flush the first record would be
// reported and every one after it silently dropped.
func TestKmsgFlushesWhatItHeldWhenTheIncidentStops(t *testing.T) {
	line := "blk_update_request: I/O error, dev sdd, sector 42 op 0x0:(READ) flags 0x0 phys_seg 1 prio class 0"
	start := time.Unix(1_800_000_000, 0).UTC()

	testee := collector.NewKmsg("/dev/kmsg", "")
	now := start
	var records [][]byte
	testee.SetSourceForTest(
		func() (collector.KmsgSource, error) { return &liveSource{pending: &records}, nil },
		func() (time.Duration, error) { return time.Hour, nil },
		func() time.Time { return now },
	)
	drain := func() []*netrav1.Event {
		t.Helper()
		res, err := testee.Collect(context.Background())
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		return res.Events
	}

	records = [][]byte{rec(3, 1, 1_000_000, line)}
	drain()

	now = start.Add(time.Minute)
	records = [][]byte{rec(3, 2, 2_000_000, line), rec(3, 3, 3_000_000, line)}
	drain()

	// The disk goes quiet. Nothing new arrives, but the window closes holding
	// two records, and they are still owed to the operator.
	now = start.Add(collector.KmsgSuppressWindowForTest + time.Minute)
	records = nil
	got := drain()
	if len(got) != 1 {
		t.Fatalf("expiry flush events = %d, want 1", len(got))
	}
	// Reported as SUPPRESSED, not as a count: they trickled in across the
	// window rather than arriving together, and `count` means a burst.
	if s := detailOf(t, got[0])["suppressed"]; s != float64(2) {
		t.Errorf("suppressed = %v, want the 2 records held", s)
	}
	if _, ok := detailOf(t, got[0])["count"]; ok {
		t.Error("the rollup claims a burst that never happened")
	}

	// And once it has been flushed with nothing further, the key is forgotten
	// rather than emitting an empty rollup forever.
	now = now.Add(collector.KmsgSuppressWindowForTest + time.Minute)
	if got := drain(); len(got) != 0 {
		t.Fatalf("a settled key emitted %d events, want 0", len(got))
	}
}

// Records are stamped from an anchor taken at drain time.
//
// Anchoring once at startup and adding each record's monotonic stamp
// accumulates the kernel clock's drift against wall time for as long as the
// agent runs; this is the same arithmetic dmesg -T is documented as getting
// wrong. A record 30s before a drain at uptime 1h lands 30s before now.
func TestKmsgStampsARecordFromItsAgeNotFromBootTime(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	up := time.Hour
	// 30s before the anchor.
	mono := (up - 30*time.Second).Microseconds()

	_, events := kmsgFixture(t, [][]byte{
		rec(3, 1, mono, "mce: [Hardware Error]: Machine check events logged"),
	}, now, up)

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	want := now.Add(-30 * time.Second).UnixMilli()
	if got := events[0].GetTsMs(); got != want {
		t.Errorf("ts_ms = %d, want %d (30s before the drain)", got, want)
	}
}

// A record the anchor says is in the future is clamped, not trusted.
//
// The record was written a moment before the /proc/uptime read that anchors it,
// so a slight overshoot is normal. An event stamped ahead of the scrape that
// carried it sorts above rows that genuinely came later.
func TestKmsgClampsARecordFromTheFuture(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	up := time.Hour
	_, events := kmsgFixture(t, [][]byte{
		rec(3, 1, (up + time.Second).Microseconds(), "mce: [Hardware Error]: Machine check events logged"),
	}, now, up)

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if got := events[0].GetTsMs(); got != now.UnixMilli() {
		t.Errorf("ts_ms = %d, want it clamped to now (%d)", got, now.UnixMilli())
	}
}

func TestKmsgParsesTheRecordFormat(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		msg  string
		prio int
		ok   bool
	}{
		{
			name: "plain record",
			raw:  "6,2565,102258586,-;md: data-check of RAID array md3",
			msg:  "md: data-check of RAID array md3",
			prio: 6,
			ok:   true,
		},
		{
			// The kernel appends key=value metadata on continuation lines. The
			// classifier reads what it needs out of the message itself, so the
			// continuation goes with the record rather than confusing it.
			name: "record with continuation lines",
			raw:  "6,339,5140900,-;ata4.00: exception Emask 0x0\n SUBSYSTEM=scsi\n DEVICE=+scsi:4:0:0:0",
			msg:  "ata4.00: exception Emask 0x0",
			prio: 6,
			ok:   true,
		},
		{
			name: "extras in the prefix",
			raw:  "5,12,999,c,caller=T123;EXT4-fs error (device sda1): oops",
			msg:  "EXT4-fs error (device sda1): oops",
			prio: 5,
			ok:   true,
		},
		{
			// \xNN is how the kernel escapes bytes outside printable ASCII.
			name: "escaped byte",
			raw:  `4,1,1,-;test\x20value`,
			msg:  "test value",
			prio: 4,
			ok:   true,
		},
		{name: "no semicolon", raw: "6,1,2,-no message here", ok: false},
		{name: "too few fields", raw: "6,1;something", ok: false},
		{name: "non-numeric priority", raw: "x,1,2,-;something", ok: false},
		{name: "empty message", raw: "6,1,2,-;   ", ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := collector.ParseKmsgRecordForTest([]byte(tc.raw))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if got.Message != tc.msg {
				t.Errorf("message = %q, want %q", got.Message, tc.msg)
			}
			if got.Priority != tc.prio {
				t.Errorf("priority = %d, want %d", got.Priority, tc.prio)
			}
		})
	}
}

// A wrapped ring buffer is a gap, not a restart.
//
// The kernel repositions the fd itself: one read returns EPIPE and the next
// returns the oldest surviving record. Reopening and seeking would replay the
// whole buffer as fresh events.
func TestKmsgKeepsReadingAfterTheRingWrapped(t *testing.T) {
	records := [][]byte{
		rec(3, 1, 1_000_000, "mce: [Hardware Error]: Machine check events logged"),
		rec(3, 2, 2_000_000, "Out of memory: Killed process 1 (init) total-vm:1kB"),
	}

	testee := collector.NewKmsg("/dev/kmsg", "")
	testee.SetSourceForTest(
		// A gap between the two records.
		func() (collector.KmsgSource, error) { return collector.NewSliceSource(records, 1), nil },
		func() (time.Duration, error) { return time.Hour, nil },
		func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
	)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Events) != 2 {
		t.Fatalf("events = %d, want both records despite the gap", len(res.Events))
	}
}

// A host that declines the device grant is a supported deployment.
//
// Failing the collector would discard every other collector's contribution to
// the same scrape, so the reason is reported as a capability and the agent
// carries on.
func TestKmsgReportsWhyItCannotRead(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "no device node", err: fs.ErrNotExist, want: "unavailable"},
		{name: "refused", err: fs.ErrPermission, want: "denied"},
		{name: "anything else", err: errors.New("boom"), want: "error"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testee := collector.NewKmsg("/dev/kmsg", "")
			testee.SetSourceForTest(
				func() (collector.KmsgSource, error) {
					return nil, &os.PathError{Op: "open", Path: "/dev/kmsg", Err: tc.err}
				},
				func() (time.Duration, error) { return time.Hour, nil },
				time.Now,
			)

			res, err := testee.Collect(context.Background())
			if err != nil {
				t.Fatalf("Collect returned an error for an unreadable device: %v", err)
			}
			if len(res.Events) != 0 {
				t.Errorf("events = %d, want none", len(res.Events))
			}
			if got := testee.Capabilities()["kmsg"]; got != tc.want {
				t.Errorf("capability = %q, want %q", got, tc.want)
			}
		})
	}
}

// A working collector reports no capability at all.
func TestKmsgReportsNoCapabilityWhenItCanRead(t *testing.T) {
	testee, _ := kmsgFixture(t, nil, time.Unix(1_800_000_000, 0).UTC(), time.Hour)
	if caps := testee.Capabilities(); caps != nil {
		t.Errorf("capabilities = %v, want none when the device reads", caps)
	}
}

// One scrape cannot flood the agent's ring.
func TestKmsgCapsWhatOneScrapeEmits(t *testing.T) {
	var records [][]byte
	for i := range 200 {
		records = append(records, rec(3, uint64(i), int64(i)*1000,
			fmt.Sprintf("blk_update_request: I/O error, dev sd%c%c, sector 1 op 0x0:(READ) flags 0x0 phys_seg 1 prio class 0",
				'a'+byte(i/26), 'a'+byte(i%26))))
	}

	_, events := kmsgFixture(t, records, time.Unix(1_800_000_000, 0).UTC(), time.Hour)
	if len(events) > collector.KmsgMaxEventsForTest {
		t.Fatalf("events = %d, want at most %d", len(events), collector.KmsgMaxEventsForTest)
	}
}

// gapSource returns errKmsgGap forever, which is what a ring wrapping faster
// than one scrape can drain it looks like from here.
type gapSource struct{ reads int }

func (g *gapSource) ReadRecord() ([]byte, error) {
	g.reads++
	return nil, collector.ErrKmsgGapForTest
}

func (g *gapSource) Close() error { return nil }

// A drain that only ever hits gaps still ends.
//
// An EPIPE consumes no record, so counting only records against the budget
// meant the loop never advanced. That is not one wedged collector: the agent
// runs its collectors sequentially in a single goroutine, so the scrape loop
// would never come back, and it would log a warning per iteration while doing
// it.
func TestKmsgDrainEndsWhenEveryReadIsAGap(t *testing.T) {
	src := &gapSource{}
	testee := collector.NewKmsg("/dev/kmsg", "")
	testee.SetSourceForTest(
		func() (collector.KmsgSource, error) { return src, nil },
		func() (time.Duration, error) { return time.Hour, nil },
		func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
	)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := testee.Collect(context.Background()); err != nil {
			t.Errorf("Collect: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Collect did not return after %d gap reads: the drain is spinning", src.reads)
	}
}

// Past the per-scrape cap, records are HELD, not dropped.
//
// The folding is documented to hide nothing, only fold. Breaking out of the
// loop discarded the remaining keys outright -- no event and no suppression
// entry -- so a wide incident touching more devices than the cap lost records
// with nothing recording that they had existed.
func TestKmsgHoldsWhatItCannotEmitInOneScrape(t *testing.T) {
	line := func(dev string) string {
		return fmt.Sprintf(
			"blk_update_request: I/O error, dev %s, sector 1 op 0x0:(READ) flags 0x0 phys_seg 1 prio class 0",
			dev)
	}

	start := time.Unix(1_800_000_000, 0).UTC()
	now := start
	var records [][]byte

	testee := collector.NewKmsg("/dev/kmsg", "")
	testee.SetSourceForTest(
		func() (collector.KmsgSource, error) { return &liveSource{pending: &records}, nil },
		func() (time.Duration, error) { return time.Hour, nil },
		func() time.Time { return now },
	)
	drain := func() []*netrav1.Event {
		t.Helper()
		res, err := testee.Collect(context.Background())
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		return res.Events
	}

	// Comfortably more distinct devices than one scrape may emit.
	devices := make([]string, 0, collector.KmsgMaxEventsForTest+10)
	for i := range collector.KmsgMaxEventsForTest + 10 {
		devices = append(devices, fmt.Sprintf("sd%c%c", 'a'+byte(i/26), 'a'+byte(i%26)))
	}
	for _, dev := range devices {
		records = append(records, rec(3, 1, 1_000_000, line(dev)))
	}

	first := drain()
	if len(first) != collector.KmsgMaxEventsForTest {
		t.Fatalf("first scrape emitted %d events, want the cap of %d",
			len(first), collector.KmsgMaxEventsForTest)
	}

	// The ones over the cap were held, so once their windows close they are
	// reported rather than having vanished.
	now = start.Add(collector.KmsgSuppressWindowForTest + time.Minute)
	records = nil
	rest := drain()
	if len(rest) == 0 {
		t.Fatal("nothing was reported for the devices over the cap: they were dropped")
	}

	seen := make(map[string]bool, len(first)+len(rest))
	for _, ev := range append(append([]*netrav1.Event{}, first...), rest...) {
		seen[ev.GetSubject()] = true
	}
	for _, dev := range devices {
		if !seen[dev] {
			t.Errorf("device %s was never reported at all", dev)
		}
	}
}
