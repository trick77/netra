package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Packages reports the installed package inventory, and the install, upgrade
// and remove events between scrapes.
//
// The package database is large and changes rarely, so it is parsed only when
// its mtime moves, with a daily floor so a host that never installs anything
// still confirms its inventory once a day. Re-parsing a 20 MB dpkg status file
// every 60s would be pure waste.
type Packages struct {
	dpkgPath string
	apkPath  string
	interval time.Duration

	now func() time.Time

	// lastMtime and lastParse gate the re-read.
	lastMtime time.Time
	lastParse time.Time

	// prev is the last inventory, keyed by name+arch, so events can be
	// derived from what changed rather than from the package manager's logs
	// (which differ per distribution and rotate).
	prev map[string]*netrav1.HostPackage

	format string
}

// dailyFloor is how long the collector waits before re-parsing an unchanged
// database. A host that installs nothing still confirms its inventory daily,
// so a hub that lost a row does not wait for the next apt-get to notice.
const dailyFloor = 24 * time.Hour

// NewPackages builds a Packages collector. dpkgPath and apkPath are the
// database files (normally /var/lib/dpkg/status and /lib/apk/db/installed);
// whichever exists decides the format.
func NewPackages(dpkgPath, apkPath string, interval time.Duration) *Packages {
	return &Packages{dpkgPath: dpkgPath, apkPath: apkPath, interval: interval, now: time.Now}
}

// EmitsBaseline implements BaselineEmitter, keeping this collector out of the
// agent's startup priming.
//
// Its first Collect is the inventory, and it is also the LAST one for a while:
// the parse stamps lastMtime and lastParse, so every later scrape short-
// circuits on "unchanged and recent" until the package database is written to
// or the daily floor elapses. Priming would therefore discard the only
// inventory the agent produces for up to 24 hours -- a freshly enrolled host
// would report no packages at all for that window, and an existing one would
// keep serving the inventory it had before the restart, because
// UpsertHostPackages returns early on an empty set.
func (p *Packages) EmitsBaseline() bool { return true }

// Name implements Collector.
func (p *Packages) Name() string { return "packages" }

// Interval implements Collector.
func (p *Packages) Interval() time.Duration { return p.interval }

// SetClockForTest replaces the clock used for the daily floor.
func (p *Packages) SetClockForTest(fn func() time.Time) { p.now = fn }

// Capabilities implements CapabilityReporter.
//
// An rpm host reports an unsupported format rather than a failure: a RHEL host
// is not broken, it is unsupported, and those are different facts. Without
// this the hub sees a host with no packages and cannot tell which.
func (p *Packages) Capabilities() map[string]string {
	if p.format == "" {
		return map[string]string{"packages": "unsupported-format"}
	}
	return nil
}

// ResendInventory implements InventoryResender.
//
// Only the re-read gate is cleared, not prev. Clearing the gate makes the next
// Collect parse and emit the full inventory again; keeping prev means the diff
// still compares against what was really installed last time, so a re-arm
// produces the inventory without a burst of phantom install events.
//
// Without this, an inventory the ring dropped waited out the daily floor --
// up to 24 hours during which the hub served whatever it had before, because
// it stores packages by replacement and returns early on an empty set.
func (p *Packages) ResendInventory() {
	p.lastParse, p.lastMtime = time.Time{}, time.Time{}
}

// Collect implements Collector.
func (p *Packages) Collect(_ context.Context) (*Result, error) {
	path, format := p.database()
	if path == "" {
		p.format = ""
		return &Result{}, nil
	}
	p.format = format

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}

	now := p.now()
	unchanged := info.ModTime().Equal(p.lastMtime)
	recent := now.Sub(p.lastParse) < dailyFloor
	if unchanged && recent && p.prev != nil {
		// Nothing installed, and the daily confirmation is not due.
		return &Result{}, nil
	}

	cur, err := p.parse(path, format)
	if err != nil {
		return nil, err
	}

	p.lastMtime = info.ModTime()
	p.lastParse = now

	prev := p.prev
	p.prev = cur

	names := make([]string, 0, len(cur))
	for key := range cur {
		names = append(names, key)
	}
	slices.Sort(names)

	inventory := make([]*netrav1.HostPackage, 0, len(names))
	for _, key := range names {
		inventory = append(inventory, cur[key])
	}

	return &Result{
		Packages:      inventory,
		PackageEvents: diffPackages(prev, cur, now.UnixMilli()),
	}, nil
}

// diffPackages derives install, upgrade and remove events from two
// inventories.
//
// Derived from the inventory rather than read from the package manager's logs
// on purpose: those differ per distribution, rotate, and are frequently absent
// in a container. Two inventories always tell the same story.
//
// The FIRST inventory produces no events -- everything already installed would
// otherwise arrive as thousands of "install" events the moment an agent starts,
// which is noise rather than history.
func diffPackages(prev, cur map[string]*netrav1.HostPackage, ts int64) []*netrav1.PackageEvent {
	if prev == nil {
		return nil
	}

	keys := make([]string, 0, len(cur)+len(prev))
	for k := range cur {
		keys = append(keys, k)
	}
	for k := range prev {
		if _, ok := cur[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)

	var events []*netrav1.PackageEvent
	for _, k := range keys {
		before, had := prev[k]
		after, has := cur[k]

		switch {
		case !had && has:
			events = append(events, &netrav1.PackageEvent{
				TsMs: ts, Name: after.GetName(), Action: "install",
				ToVersion: after.GetVersion(),
			})
		case had && !has:
			events = append(events, &netrav1.PackageEvent{
				TsMs: ts, Name: before.GetName(), Action: "remove",
				FromVersion: before.GetVersion(),
			})
		case before.GetVersion() != after.GetVersion():
			events = append(events, &netrav1.PackageEvent{
				TsMs: ts, Name: after.GetName(), Action: "upgrade",
				FromVersion: before.GetVersion(), ToVersion: after.GetVersion(),
			})
		}
	}
	return events
}

// database returns the package database this host uses, and its format.
func (p *Packages) database() (string, string) {
	if p.dpkgPath != "" {
		if _, err := os.Stat(p.dpkgPath); err == nil {
			return p.dpkgPath, "dpkg"
		}
	}
	if p.apkPath != "" {
		if _, err := os.Stat(p.apkPath); err == nil {
			return p.apkPath, "apk"
		}
	}
	return "", ""
}

func (p *Packages) parse(path, format string) (map[string]*netrav1.HostPackage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if format == "apk" {
		return parseApk(f)
	}
	return parseDpkg(f)
}

// pkgKey is name+arch, because a multiarch host legitimately carries the same
// package for two architectures with independent versions.
func pkgKey(name, arch string) string { return name + "\x00" + arch }

// parseDpkg reads /var/lib/dpkg/status, which is RFC822-style stanzas
// separated by blank lines.
func parseDpkg(f *os.File) (map[string]*netrav1.HostPackage, error) {
	out := make(map[string]*netrav1.HostPackage)

	var name, version, arch, status string
	var size uint64

	flush := func() {
		// "deinstall ok config-files" means removed but its config remains.
		// Reporting it as installed would show packages the host does not
		// have, which is what `dpkg -l` gets criticised for.
		if name != "" && strings.HasPrefix(status, "install ok installed") {
			pkg := &netrav1.HostPackage{
				Name: name, Version: version, Arch: arch, Format: "dpkg",
			}
			if size > 0 {
				// Installed-Size is in kibibytes.
				pkg.SizeBytes = ptrTo(size * 1024)
			}
			out[pkgKey(name, arch)] = pkg
		}
		name, version, arch, status, size = "", "", "", "", 0
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch key {
		case "Package":
			name = value
		case "Version":
			version = value
		case "Architecture":
			arch = value
		case "Status":
			status = value
		case "Installed-Size":
			size, _ = strconv.ParseUint(value, 10, 64)
		}
	}
	flush()

	return out, scanner.Err()
}

// parseApk reads /lib/apk/db/installed, whose stanzas use single-letter keys.
func parseApk(f *os.File) (map[string]*netrav1.HostPackage, error) {
	out := make(map[string]*netrav1.HostPackage)

	var name, version, arch string
	var size uint64

	flush := func() {
		if name != "" {
			pkg := &netrav1.HostPackage{
				Name: name, Version: version, Arch: arch, Format: "apk",
			}
			if size > 0 {
				pkg.SizeBytes = ptrTo(size)
			}
			out[pkgKey(name, arch)] = pkg
		}
		name, version, arch, size = "", "", "", 0
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if len(line) < 2 || line[1] != ':' {
			continue
		}
		value := line[2:]
		switch line[0] {
		case 'P':
			name = value
		case 'V':
			version = value
		case 'A':
			arch = value
		case 'I':
			// I is the installed size in bytes; S is the compressed download
			// size, which says nothing about disk usage on this host.
			size, _ = strconv.ParseUint(value, 10, 64)
		}
	}
	flush()

	return out, scanner.Err()
}
