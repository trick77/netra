package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// driveSensor is one drivetemp reading. Chip and label are the same for every
// disk on a host, which is the whole point: what tells them apart is the
// instance.
func driveSensor(instance string, at time.Time, temp float64) *netrav1.SensorSample {
	return &netrav1.SensorSample{
		TsMs:     at.UnixMilli(),
		Chip:     "drivetemp",
		Label:    "temp1",
		Kind:     "temperature",
		Instance: instance,
		Temp:     proto.Float64(temp),
		Value:    proto.Float64(temp),
	}
}

// Four disks are four sensors, and four readings per scrape.
//
// THE REGRESSION. Identity was chip + label, and the drivetemp driver names
// every chip it registers "drivetemp" with no tempN_label. Four disks arrived
// as four rows all calling themselves drivetemp/temp1, resolveSensorIDs
// mapped them onto ONE sensor row, and sensor_samples' ON CONFLICT
// (host_id, ts, sensor_id) DO NOTHING then kept the first reading of each
// scrape and discarded the other three. A four-disk NAS reported one
// temperature and nothing said so.
//
// Both halves are asserted because either alone passes while the bug is
// live in the other: keying only the sensors table gives four rows that all
// the samples still funnel into, and the sample count is what catches it.
func TestIntegrationSensorsOnOneChipNameAreToldApartByTheirBlockDevice(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "four-disk-nas")

	at := time.Now().Truncate(time.Minute)
	rows := []*netrav1.SensorSample{
		driveSensor("sda", at, 34),
		driveSensor("sdb", at, 33),
		driveSensor("sdc", at, 42),
		driveSensor("sdd", at, 35),
	}
	if _, err := s.InsertSensorSamples(ctx, id, rows); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var sensors int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM sensors WHERE host_id = $1 AND chip = 'drivetemp'`,
		id).Scan(&sensors); err != nil {
		t.Fatalf("count sensors: %v", err)
	}
	if sensors != 4 {
		t.Errorf("sensors = %d, want 4 -- one per disk", sensors)
	}

	var samples int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM sensor_samples WHERE host_id = $1`, id).Scan(&samples); err != nil {
		t.Fatalf("count samples: %v", err)
	}
	if samples != 4 {
		t.Errorf("samples = %d, want 4 -- a scrape that reads four disks must store four readings", samples)
	}

	// The readings are the disks' own, not one disk's copied four times.
	var temps []float64
	rowsOut, err := s.Pool().Query(ctx,
		`SELECT temp FROM sensor_samples WHERE host_id = $1 ORDER BY temp`, id)
	if err != nil {
		t.Fatalf("query temps: %v", err)
	}
	defer rowsOut.Close()
	for rowsOut.Next() {
		var v float64
		if err := rowsOut.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		temps = append(temps, v)
	}
	want := []float64{33, 34, 35, 42}
	if len(temps) != len(want) {
		t.Fatalf("temps = %v, want %v", temps, want)
	}
	for i := range want {
		if temps[i] != want[i] {
			t.Errorf("temps = %v, want %v", temps, want)
			break
		}
	}
}

// An agent predating the instance field keeps the sensor it has always had.
//
// It sends "", which is the column's default, so the row it resolves to is
// the row already in the table. Anything else would fork the history of every
// coretemp and every fan in the fleet at the moment the hub was upgraded --
// before a single agent had been.
func TestIntegrationSensorsWithoutAnInstanceKeepTheirExistingRow(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "old-agent")

	at := time.Now().Truncate(time.Minute)
	old := func(offset time.Duration) *netrav1.SensorSample {
		return &netrav1.SensorSample{
			TsMs:  at.Add(offset).UnixMilli(),
			Chip:  "coretemp",
			Label: "Package id 0",
			Temp:  proto.Float64(45),
			Value: proto.Float64(45),
		}
	}
	if _, err := s.InsertSensorSamples(ctx, id, []*netrav1.SensorSample{old(0)}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := s.InsertSensorSamples(ctx, id, []*netrav1.SensorSample{old(time.Minute)}); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	var sensors int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM sensors WHERE host_id = $1 AND chip = 'coretemp'`,
		id).Scan(&sensors); err != nil {
		t.Fatalf("count: %v", err)
	}
	if sensors != 1 {
		t.Errorf("sensors = %d, want 1 -- two scrapes from one chip are one series", sensors)
	}
}
