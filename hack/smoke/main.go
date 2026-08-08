// Command smoke runs every collector twice against the real /proc of the
// machine it is on and prints the resulting sample as JSON.
//
// It exists because fixture tests cannot answer "does this parse a real
// kernel's files": the fixtures are what the author believed /proc looks
// like. Run it inside a Linux container to check the collectors against an
// actual kernel, and compare the numbers against vmstat, who and nstat.
//
// Two scrapes, sixty simulated seconds apart, because every rate needs a
// baseline and the first scrape deliberately produces none.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func main() {
	procRoot := "/proc"
	if v := os.Getenv("NETRA_PROC_ROOT"); v != "" {
		procRoot = v
	}
	utmpPath := "/var/run/utmp"
	if v := os.Getenv("NETRA_UTMP_PATH"); v != "" {
		utmpPath = v
	}

	collectors := []collector.Collector{
		collector.NewCPU(procRoot, time.Minute),
		collector.NewMemory(procRoot, time.Minute),
		collector.NewLoad(procRoot, time.Minute),
		collector.NewKernelStat(procRoot, time.Minute),
		collector.NewProcs(procRoot, os.Getenv("NETRA_PID_HOST") == "1", time.Minute),
		collector.NewNetstat(procRoot, time.Minute),
		collector.NewUsers(utmpPath, time.Minute),
	}

	ctx := context.Background()

	// Prime, then scrape for real a second later. Rates over a 1s interval
	// are noisy but present, which is what this is checking.
	var baseline netrav1.HostSample
	for _, c := range collectors {
		if err := c.Collect(ctx, &baseline); err != nil {
			fmt.Fprintf(os.Stderr, "prime %s: %v\n", c.Name(), err)
		}
	}
	time.Sleep(time.Second)

	sample := &netrav1.HostSample{TsMs: time.Now().UnixMilli()}
	for _, c := range collectors {
		if err := c.Collect(ctx, sample); err != nil {
			fmt.Fprintf(os.Stderr, "collect %s: %v\n", c.Name(), err)
		}
		if r, ok := c.(collector.CapabilityReporter); ok {
			for k, v := range r.Capabilities() {
				fmt.Fprintf(os.Stderr, "capability %s=%s\n", k, v)
			}
		}
	}

	out, err := json.MarshalIndent(sample, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}
