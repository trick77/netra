package sim

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"time"
)

// signal turns a (seed, series name, timestamp) triple into a value.
//
// It is deliberately STATELESS -- a hash rather than a random walk. A walk
// would make every sample depend on the one before it, so generating the
// window in a different order, resuming a partial run, or regenerating one
// host would all produce different history. A hash gives the same value for
// the same (series, timestamp) forever, which is what makes --seed mean
// anything and what lets a re-run be a genuine no-op against
// ON CONFLICT DO NOTHING rather than a silent divergence.
type signal struct {
	seed uint64
}

func newSignal(seed uint64) signal { return signal{seed: seed} }

// unit returns a deterministic value in [0,1) for this series at this instant.
func (s signal) unit(series string, ts time.Time) float64 {
	h := fnv.New64a()
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[0:8], s.seed)
	binary.LittleEndian.PutUint64(buf[8:16], uint64(ts.UnixMilli()))
	_, _ = h.Write(buf[:])
	_, _ = h.Write([]byte(series))
	// The top 53 bits are the mantissa of a float64, so this covers [0,1)
	// evenly without the modulo bias of a plain integer remainder.
	return float64(mix(h.Sum64())>>11) / float64(uint64(1)<<53)
}

// mix avalanches a hash so that series names differing only in their last
// character produce unrelated values.
//
// Without it FNV-1a leaks its own structure straight into the data. A
// difference in the final byte reaches the digest through a single multiply
// by the 40-bit FNV prime, so it perturbs only bits 40-42 and leaves the top
// bits untouched -- and unit() reads the TOP 53. In practice "core/0",
// "core/1" and "core/2" agreed to seven decimal places, which quietly made
// every core on a 32-thread host report the same utilisation, every disk the
// same I/O, and three "randomly chosen" packages resolve to the same one.
// This is the standard murmur3 fmix64 finaliser.
func mix(x uint64) uint64 {
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	return x
}

// jitter returns a deterministic value in [-1,1).
func (s signal) jitter(series string, ts time.Time) float64 {
	return s.unit(series, ts)*2 - 1
}

// daily is the workhorse: a base value shaped by a day/night cycle and
// roughened with noise. amp is the fraction of base the daily swing covers,
// noise the fraction the jitter covers.
//
// Busy hours are centred on 14:00 UTC rather than on midnight, so a chart of
// a week looks like a machine people use rather than like a sine wave someone
// forgot to phase-shift.
func (s signal) daily(series string, ts time.Time, base, amp, noise float64) float64 {
	secs := float64(ts.UTC().Hour()*3600 + ts.UTC().Minute()*60 + ts.UTC().Second())
	phase := 2 * math.Pi * (secs/86400 - 14.0/24.0)
	// A weekly component too, so weekends read lower than midweek.
	weekly := 1 - 0.18*boolToFloat(isWeekend(ts))
	v := base * (1 + amp*math.Cos(phase)) * weekly
	v += base * noise * s.jitter(series, ts)
	if v < 0 {
		return 0
	}
	return v
}

// spike adds an occasional burst: for a small fraction of samples the value
// jumps several times its baseline. Without it every percentile of every
// series is the same number and a p99 panel is indistinguishable from a mean.
func (s signal) spike(series string, ts time.Time, v, chance, magnitude float64) float64 {
	if s.unit(series+"/spike", ts) < chance {
		return v * magnitude
	}
	return v
}

// clamp bounds a value to [lo,hi]. Percentages that wander past 100 are not
// realistic data, they are a bug that looks like data.
func clamp(v, lo, hi float64) float64 {
	return math.Min(math.Max(v, lo), hi)
}

// ramp interpolates between two values across the simulated window, so a
// filesystem can be shown filling up and a drive can be shown ageing.
func ramp(from, to time.Time, ts time.Time, start, end float64) float64 {
	span := to.Sub(from)
	if span <= 0 {
		return end
	}
	f := clamp(ts.Sub(from).Seconds()/span.Seconds(), 0, 1)
	return start + (end-start)*f
}

func isWeekend(ts time.Time) bool {
	switch ts.UTC().Weekday() {
	case time.Saturday, time.Sunday:
		return true
	default:
		return false
	}
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
