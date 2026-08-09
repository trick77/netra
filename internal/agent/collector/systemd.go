package collector

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"slices"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Unit is one systemd unit's state.
type Unit struct {
	Name     string
	Active   string // active, failed, inactive
	SubState string // running, exited, dead
}

// UnitLister enumerates systemd units.
//
// Injected so the collector is testable without systemd, which the machines
// running these tests do not have.
type UnitLister func(ctx context.Context) ([]Unit, error)

// SystemUnits is the production UnitLister.
//
// Shells out to systemctl rather than speaking D-Bus directly: the wire
// protocol would pull in a D-Bus library and a hand-rolled authentication
// handshake for a question systemctl already answers, and the binary is
// present on every host that has systemd at all. --plain and --no-legend make
// the output stable across versions, which `--output=json` is not.
func SystemUnits(ctx context.Context) ([]Unit, error) {
	cmd := exec.CommandContext(ctx, "systemctl",
		"list-units", "--type=service", "--all", "--plain", "--no-legend", "--no-pager")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseSystemctl(out), nil
}

// parseSystemctl reads the columns of `systemctl list-units --plain
// --no-legend`: UNIT LOAD ACTIVE SUB DESCRIPTION.
func parseSystemctl(out []byte) []Unit {
	var units []Unit

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())

		// systemd marks a failed unit with a leading bullet, separated by a
		// space -- so it arrives as its OWN field and shifts every column
		// right. Dropping it by prefix-trimming fields[0] would leave an empty
		// name and silently skip exactly the units that matter most.
		if len(fields) > 0 && (fields[0] == "●" || fields[0] == "*") {
			fields = fields[1:]
		}

		if len(fields) < 4 {
			continue
		}
		name := fields[0]
		if !strings.HasSuffix(name, ".service") {
			continue
		}
		units = append(units, Unit{Name: name, Active: fields[2], SubState: fields[3]})
	}
	return units
}

// Systemd reports unit state changes as EVENTS, plus a numeric summary on the
// host row.
//
// Events rather than samples for the same reason as mdraid: a unit's state is
// constant for days, so a 60s series saying "running" is the near-constant
// waste that keeps systemd out of spec §5.3. The two numbers an operator
// dashboards -- how many services exist and how many are failed -- ride
// host_samples, where they are cheap.
type Systemd struct {
	interval time.Duration
	lister   UnitLister

	prev        map[string]Unit
	unavailable bool
}

// NewSystemd builds a Systemd collector.
func NewSystemd(interval time.Duration, lister UnitLister) *Systemd {
	return &Systemd{interval: interval, lister: lister}
}

// SetListerForTest swaps the unit source, so a test can change what systemd
// reports between two scrapes without rebuilding the collector and losing the
// previous state the transition detection depends on.
func (s *Systemd) SetListerForTest(l UnitLister) { s.lister = l }

// ParseSystemctlForTest exposes the output parser, which is otherwise
// unreachable: the production path runs a binary the test machines do not
// have.
func ParseSystemctlForTest(out []byte) []Unit { return parseSystemctl(out) }

// Name implements Collector.
func (s *Systemd) Name() string { return "systemd" }

// Interval implements Collector.
func (s *Systemd) Interval() time.Duration { return s.interval }

// Capabilities implements CapabilityReporter.
func (s *Systemd) Capabilities() map[string]string {
	if s.unavailable {
		// A host running OpenRC or running the agent without /run/systemd is
		// not broken; it has no systemd. Distinguishing that from "zero
		// services" is the whole point of saying so.
		return map[string]string{"systemd": "unavailable"}
	}
	return nil
}

// Collect implements Collector.
func (s *Systemd) Collect(ctx context.Context) (*Result, error) {
	units, err := s.lister(ctx)
	if err != nil {
		s.unavailable = true
		return &Result{}, nil
	}
	s.unavailable = false

	cur := make(map[string]Unit, len(units))
	var failed uint32
	for _, u := range units {
		cur[u.Name] = u
		if u.Active == "failed" {
			failed++
		}
	}

	prev := s.prev
	s.prev = cur

	names := make([]string, 0, len(cur))
	for name := range cur {
		names = append(names, name)
	}
	slices.Sort(names)

	ts := time.Now().UnixMilli()
	var events []*netrav1.SystemdUnitEvent

	if prev != nil {
		for _, name := range names {
			u := cur[name]
			p, seen := prev[name]
			if seen && p.Active == u.Active && p.SubState == u.SubState {
				// Unchanged. Emitting anyway would turn the event table into
				// the 60s series this collector exists to avoid.
				continue
			}
			events = append(events, &netrav1.SystemdUnitEvent{
				TsMs:     ts,
				UnitName: name,
				State:    u.Active,
				Substate: u.SubState,
			})
		}
	}

	// The summary rides the host row, where two integers cost nothing, rather
	// than forcing a dashboard to count rows in an event table.
	return &Result{
		Host: &netrav1.HostSample{
			ServicesTotal:  ptrTo(uint32(len(units))),
			ServicesFailed: ptrTo(failed),
		},
		SystemdEvents: events,
	}, nil
}
