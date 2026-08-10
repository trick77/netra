package collector_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func pkgRow(t *testing.T, rows []*netrav1.HostPackage, name string) *netrav1.HostPackage {
	t.Helper()
	for _, r := range rows {
		if r.GetName() == name {
			return r
		}
	}
	t.Fatalf("no package %q in %d rows", name, len(rows))
	return nil
}

func TestPackagesParsesDpkgStatus(t *testing.T) {
	testee := collector.NewPackages("testdata/packages/dpkg/status", "", time.Hour)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	bash := pkgRow(t, res.Packages, "bash")
	if got := bash.GetVersion(); got != "5.2.15-2+b7" {
		t.Errorf("bash version = %q", got)
	}
	if got := bash.GetArch(); got != "amd64" {
		t.Errorf("bash arch = %q, want amd64", got)
	}
	if got := bash.GetFormat(); got != "dpkg" {
		t.Errorf("bash format = %q, want dpkg", got)
	}
	// Installed-Size is kibibytes in dpkg; the schema stores bytes.
	if got := bash.GetSizeBytes(); got != 1740*1024 {
		t.Errorf("bash size_bytes = %d, want %d", got, 1740*1024)
	}
}

// "deinstall ok config-files" means the package was removed and only its
// config remains. Reporting it as installed would list packages the host does
// not have.
func TestPackagesExcludesRemovedButConfiguredPackages(t *testing.T) {
	testee := collector.NewPackages("testdata/packages/dpkg/status", "", time.Hour)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, r := range res.Packages {
		if r.GetName() == "removed-thing" {
			t.Error("a deinstalled package was reported as installed")
		}
	}
}

// A multiarch host carries the same package name for two architectures, with
// independent versions. Keying on name alone would silently drop one.
func TestPackagesKeepsBothArchitecturesOfOneName(t *testing.T) {
	testee := collector.NewPackages("testdata/packages/dpkg/status", "", time.Hour)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := pkgRow(t, res.Packages, "libfoo").GetArch(); got != "i386" {
		t.Errorf("libfoo arch = %q, want i386", got)
	}
}

func TestPackagesParsesApkInstalled(t *testing.T) {
	testee := collector.NewPackages("", "testdata/packages/apk/installed", time.Hour)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Packages) != 2 {
		t.Fatalf("packages = %d, want 2", len(res.Packages))
	}

	busybox := pkgRow(t, res.Packages, "busybox")
	if got := busybox.GetVersion(); got != "1.36.1-r5" {
		t.Errorf("busybox version = %q", got)
	}
	if got := busybox.GetFormat(); got != "apk" {
		t.Errorf("busybox format = %q, want apk", got)
	}
	// I (installed size), not S (compressed download size) -- S says nothing
	// about disk usage on this host.
	if got := busybox.GetSizeBytes(); got != 962560 {
		t.Errorf("busybox size_bytes = %d, want 962560 (I, not S)", got)
	}
}

// An rpm host reports an unsupported format rather than failing. A RHEL host
// is not broken, it is unsupported, and the hub cannot otherwise tell a host
// with no packages from one whose format netra does not read.
func TestPackagesReportsUnsupportedFormatWhenNoDatabaseExists(t *testing.T) {
	testee := collector.NewPackages(
		filepath.Join(t.TempDir(), "status"),
		filepath.Join(t.TempDir(), "installed"),
		time.Hour)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v, want no error for an unsupported format", err)
	}
	if len(res.Packages) != 0 {
		t.Errorf("packages = %d, want 0", len(res.Packages))
	}
	if got := testee.Capabilities()["packages"]; got != "unsupported-format" {
		t.Errorf("capability = %q, want unsupported-format", got)
	}
}

// The database is large and changes rarely, so an unchanged mtime inside the
// daily floor must not be re-parsed. Re-reading a 20 MB status file every 60s
// is pure waste.
func TestPackagesSkipsReparsingAnUnchangedDatabase(t *testing.T) {
	testee := collector.NewPackages("testdata/packages/dpkg/status", "", time.Hour)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee.SetClockForTest(func() time.Time { return base })

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	if len(res.Packages) == 0 {
		t.Fatal("first scrape reported no packages")
	}

	// Same mtime, an hour later: still inside the daily floor.
	testee.SetClockForTest(func() time.Time { return base.Add(time.Hour) })
	res, err = testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if len(res.Packages) != 0 {
		t.Errorf("packages = %d on an unchanged database, want 0 -- it must not re-parse", len(res.Packages))
	}
}

// A host that installs nothing still confirms its inventory once a day, so a
// hub that lost a row does not wait for the next apt-get to notice.
func TestPackagesReparsesAfterTheDailyFloor(t *testing.T) {
	testee := collector.NewPackages("testdata/packages/dpkg/status", "", time.Hour)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee.SetClockForTest(func() time.Time { return base })

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	testee.SetClockForTest(func() time.Time { return base.Add(25 * time.Hour) })
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect after the floor: %v", err)
	}
	if len(res.Packages) == 0 {
		t.Error("no packages after the daily floor; the inventory must be reconfirmed")
	}
}

// An inventory the ring dropped must not wait out the daily floor.
//
// The hub stores packages by replacement and returns early on an empty set, so
// for up to 24 hours it would go on serving whatever it held before -- while
// this collector, having already advanced its own re-read gate, believed it
// had reported. Re-arming clears the gate but keeps prev, so the set comes
// back WITHOUT a burst of phantom install events derived against nothing.
func TestResendInventoryReparsesWithoutInventingEvents(t *testing.T) {
	// Given: a collector that has parsed and is inside its daily floor.
	testee := collector.NewPackages("testdata/packages/dpkg/status", "", time.Hour)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee.SetClockForTest(func() time.Time { return base })

	first, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	if len(first.Packages) == 0 {
		t.Fatal("first scrape reported no packages")
	}

	testee.SetClockForTest(func() time.Time { return base.Add(time.Hour) })
	if res, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("second Collect: %v", err)
	} else if len(res.Packages) != 0 {
		t.Fatalf("packages = %d inside the floor, want 0", len(res.Packages))
	}

	// When: the agent says the reported inventory never reached the hub.
	testee.ResendInventory()

	// Then: the full set is parsed and reported again, with no events.
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect after ResendInventory: %v", err)
	}
	if len(res.Packages) != len(first.Packages) {
		t.Errorf("packages = %d after a re-arm, want %d -- the whole set",
			len(res.Packages), len(first.Packages))
	}
	if len(res.PackageEvents) != 0 {
		t.Errorf("package events = %d after a re-arm, want 0 -- nothing was installed",
			len(res.PackageEvents))
	}
}

// Events are derived from two inventories rather than from the package
// manager's logs, which differ per distribution, rotate, and are frequently
// absent in a container.
func TestPackagesDerivesInstallUpgradeAndRemoveEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status")

	write := func(body string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	write(`Package: bash
Status: install ok installed
Architecture: amd64
Version: 5.2-1

Package: oldpkg
Status: install ok installed
Architecture: amd64
Version: 1.0
`, base)

	testee := collector.NewPackages(path, "", time.Hour)
	testee.SetClockForTest(func() time.Time { return base })

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	// The first inventory produces no events: everything already installed
	// would otherwise arrive as thousands of "install" events at agent start.
	if len(res.PackageEvents) != 0 {
		t.Fatalf("events on the first inventory = %d, want 0", len(res.PackageEvents))
	}

	// bash upgraded, oldpkg removed, newpkg installed.
	later := base.Add(time.Minute)
	write(`Package: bash
Status: install ok installed
Architecture: amd64
Version: 5.3-1

Package: newpkg
Status: install ok installed
Architecture: amd64
Version: 2.0
`, later)
	testee.SetClockForTest(func() time.Time { return later })

	res, err = testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}

	got := map[string]*netrav1.PackageEvent{}
	for _, e := range res.PackageEvents {
		got[e.GetName()] = e
	}

	if e := got["bash"]; e == nil || e.GetAction() != "upgrade" {
		t.Errorf("bash event = %+v, want an upgrade", e)
	} else {
		if e.GetFromVersion() != "5.2-1" || e.GetToVersion() != "5.3-1" {
			t.Errorf("bash upgrade %s -> %s, want 5.2-1 -> 5.3-1", e.GetFromVersion(), e.GetToVersion())
		}
	}
	if e := got["oldpkg"]; e == nil || e.GetAction() != "remove" {
		t.Errorf("oldpkg event = %+v, want a remove", e)
	}
	if e := got["newpkg"]; e == nil || e.GetAction() != "install" {
		t.Errorf("newpkg event = %+v, want an install", e)
	}
}
