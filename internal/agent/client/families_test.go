package client_test

import (
	"reflect"
	"testing"

	"github.com/trick77/netra/internal/agent/buffer"
	"github.com/trick77/netra/internal/agent/client"
	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// sliceFields returns the names and indexes of every slice field on a struct,
// so the tests below can enumerate families instead of restating a list that
// would drift out of date exactly as quietly as the code it guards.
func sliceFields(t reflect.Type) []int {
	var out []int
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Type.Kind() == reflect.Slice {
			out = append(out, i)
		}
	}
	return out
}

// Every per-entity family must reach the flush bound's row count.
//
// The bound is expressed in rows because a scrape stopped being one row. A
// family added to Scrape but missed in countRows silently un-enforces the
// hub's 4 MiB body limit: nothing fails until a large host replays after an
// outage, and the resulting 413 repeats forever because the ring re-sends the
// same oversized prefix.
func TestScrapeRowCountCoversEveryFamily(t *testing.T) {
	st := reflect.TypeOf(buffer.Scrape{})
	fields := sliceFields(st)
	if len(fields) == 0 {
		t.Fatal("buffer.Scrape has no slice fields; this test is not testing anything")
	}

	for _, idx := range fields {
		f := st.Field(idx)

		// Given: a scrape carrying a host row and exactly one row of this
		// family and no other.
		s := &buffer.Scrape{Host: &netrav1.HostSample{}}
		rs := reflect.ValueOf(s).Elem().Field(idx)
		rs.Set(reflect.MakeSlice(f.Type, 1, 1))

		// When/Then: the row count sees both.
		if got := client.CountRowsForTest(s); got != 2 {
			t.Errorf("countRows with one %s row = %d, want 2 -- %s is missing from the row count",
				f.Name, got, f.Name)
		}
	}
}

// Every family a collector returns must reach the scrape.
//
// A family added to Result and Scrape but forgotten in appendFamilies is
// collected, then dropped before it ever reaches the ring. The collector's own
// tests still pass -- they assert on the Result -- so nothing anywhere reports
// that the data stops here.
func TestScrapeCarriesEveryFamilyFromAResult(t *testing.T) {
	rt := reflect.TypeOf(collector.Result{})
	st := reflect.TypeOf(buffer.Scrape{})

	for _, idx := range sliceFields(rt) {
		f := rt.Field(idx)

		// The two structs name their families identically, which is what makes
		// this check mechanical. A rename on one side only should fail here
		// rather than silently skip the family.
		sf, ok := st.FieldByName(f.Name)
		if !ok {
			t.Errorf("collector.Result has %s but buffer.Scrape does not; the family cannot be buffered", f.Name)
			continue
		}
		if sf.Type != f.Type {
			t.Errorf("%s is %s on Result and %s on Scrape", f.Name, f.Type, sf.Type)
			continue
		}

		// Given: a result carrying exactly one row of this family.
		res := &collector.Result{}
		reflect.ValueOf(res).Elem().Field(idx).Set(reflect.MakeSlice(f.Type, 1, 1))

		// When: it is appended to an empty scrape.
		s := &buffer.Scrape{}
		client.AppendFamiliesForTest(s, res)

		// Then: the row arrived.
		got := reflect.ValueOf(s).Elem().FieldByName(f.Name).Len()
		if got != 1 {
			t.Errorf("appendFamilies dropped %s: scrape has %d rows, want 1", f.Name, got)
		}
	}
}

// And the same completeness check for the request: a family that reaches the
// ring but not the request body is buffered forever and never sent.
func TestFlushSendsEveryBufferedFamily(t *testing.T) {
	st := reflect.TypeOf(buffer.Scrape{})
	rt := reflect.TypeOf(netrav1.IngestRequest{})

	// Scrape's field names and the request's differ by design -- Disks vs
	// DiskIo, Nets vs Net -- so map them explicitly. A family added to Scrape
	// without an entry here fails the count check below rather than being
	// quietly skipped.
	mapping := map[string]string{
		"Cores":         "CpuCores",
		"Disks":         "DiskIo",
		"Sensors":       "Sensors",
		"Nets":          "Net",
		"Containers":    "Containers",
		"Filesystems":   "Filesystems",
		"Smart":         "Smart",
		"Events":        "Events",
		"SystemdEvents": "SystemdEvents",
		"PackageEvents": "PackageEvents",
		"Addresses":     "Addresses",
		"Packages":      "Packages",
		"Collectors":    "Collectors",
	}

	families := sliceFields(st)
	if len(mapping) != len(families) {
		t.Fatalf("buffer.Scrape has %d families but the mapping names %d; a family was added without being mapped to a request field",
			len(families), len(mapping))
	}

	for _, idx := range families {
		name := st.Field(idx).Name
		reqField, ok := mapping[name]
		if !ok {
			t.Errorf("Scrape.%s has no request field mapped", name)
			continue
		}
		if _, ok := rt.FieldByName(reqField); !ok {
			t.Errorf("IngestRequest has no field %s, mapped from Scrape.%s", reqField, name)
		}
	}
}
