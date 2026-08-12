package client

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/agent/config"
	"github.com/trick77/netra/internal/buildinfo"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// BuildMetadata gathers the static facts about this host and agent.
//
// The four hardware facts -- kernel, cpu_model, cores, memory_total -- are
// read from procRoot rather than a syscall so that the same fixture trees
// every collector is tested against work here too, and so the function stays
// buildable off Linux.
//
// Each is left empty when it cannot be read. The hub stores NULL, which is
// the truth: the alternative of inventing a plausible default would put a
// wrong CPU model on a host page with no way to tell it from a right one.
func BuildMetadata(cfg config.Config) *netrav1.Metadata {
	hostname, _ := os.Hostname()

	md := &netrav1.Metadata{
		AgentVersion: buildinfo.Version(),
		GoVersion:    buildinfo.GoVersion(),
		BuildCommit:  buildinfo.Commit(),
		Hostname:     hostname,
		Arch:         runtime.GOARCH,
		OsName:       runtime.GOOS,
		Threads:      uint32(runtime.NumCPU()),
		Location:     cfg.Location,
		Provider:     cfg.Provider,
		Facility:     cfg.Facility,
		HostType:     cfg.HostType,
		Fingerprint:  fingerprint(),
	}

	md.Kernel = readKernelRelease(cfg.ProcRoot)
	md.CpuModel, md.Cores = readCPUInfo(cfg.ProcRoot)
	md.MemoryTotal = readMemTotal(cfg.ProcRoot)

	return md
}

// readKernelRelease returns the running kernel version, as uname -r reports
// it. /proc/sys/kernel/osrelease is the same string the syscall returns.
func readKernelRelease(procRoot string) string {
	raw, err := os.ReadFile(filepath.Join(procRoot, "sys", "kernel", "osrelease"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// readCPUInfo returns the CPU model string and the number of PHYSICAL cores.
//
// Cores is deliberately not runtime.NumCPU(): that is threads, which the
// metadata already carries separately. On a hyper-threaded host the two
// differ by a factor of two, and reporting threads as cores would make every
// per-core reading look half as loaded as it is.
//
// Physical cores are counted as distinct (physical id, core id) pairs, which
// is what makes a dual-socket machine come out right rather than counting one
// socket's cores twice. Kernels that report neither field -- most ARM -- fall
// back to the processor count, where threads and cores are the same thing.
func readCPUInfo(procRoot string) (model string, cores uint32) {
	f, err := os.Open(filepath.Join(procRoot, "cpuinfo"))
	if err != nil {
		return "", 0
	}
	defer func() { _ = f.Close() }()

	seen := make(map[string]struct{})
	processors := 0
	physicalID, coreID := "", ""

	flush := func() {
		if physicalID != "" || coreID != "" {
			seen[physicalID+"/"+coreID] = struct{}{}
		}
		physicalID, coreID = "", ""
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			// Blank line ends one processor block.
			flush()
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		switch key {
		case "processor":
			processors++
		case "model name", "Model":
			// x86 uses "model name"; some ARM kernels use "Model". First one
			// wins -- every processor block repeats the same string.
			if model == "" {
				model = value
			}
		case "physical id":
			physicalID = value
		case "core id":
			coreID = value
		}
	}
	flush()

	if len(seen) > 0 {
		return model, uint32(len(seen))
	}
	return model, uint32(processors)
}

// readMemTotal returns MemTotal from /proc/meminfo in bytes.
func readMemTotal(procRoot string) uint64 {
	f, err := os.Open(filepath.Join(procRoot, "meminfo"))
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok || key != "MemTotal" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		// meminfo reports kB.
		return v * 1024
	}
	return 0
}

// HashMetadata reduces a metadata block to the 8 bytes sent on every POST.
//
// Deterministic marshalling matters: protobuf map and field ordering is not
// guaranteed stable by default, and an unstable hash would make the agent
// resend its metadata on every scrape.
func HashMetadata(md *netrav1.Metadata) []byte {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(md)
	if err != nil {
		// Marshalling a struct we built ourselves cannot fail in practice, but
		// the fallback must still be a hash that never MATCHES, not a fixed
		// one. A zero hash did the opposite of what it claimed: once the hub
		// stored it, reconcileMetadata's
		// `!bytes.Equal(stored, sent) || len(stored) == 0` is false on both
		// sides, so request_metadata was never set again and no later change —
		// an agent upgrade, a hostname change — was ever propagated.
		//
		// A random value forces the resend the comment always promised: it
		// cannot equal what the hub stored, and it differs again next time.
		out := make([]byte, 8)
		if _, rerr := rand.Read(out); rerr != nil {
			// Even the failure path must not settle on a stable value.
			binary.BigEndian.PutUint64(out, uint64(time.Now().UnixNano()))
		}
		return out
	}

	sum := sha256.Sum256(raw)
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, binary.BigEndian.Uint64(sum[:8]))
	return out
}

// fingerprint identifies the physical machine so a token copied to a second
// host is detectable. /etc/machine-id is stable across reboots and container
// recreation.
//
// Deviation from the brief: the verbatim brief returns string(sum[:]), i.e.
// raw SHA-256 bytes cast to a Go string. Fingerprint is a proto3 string
// field, and protobuf-go rejects non-UTF-8 strings at Marshal time. That
// would make every ingest request carrying metadata fail to marshal once the
// hub asks for it (RequestMetadata=true), permanently — the agent would
// never send metadata again while buffering forever. Hex-encoding the sum
// keeps it valid UTF-8 and just as suitable as a stable, opaque identifier.
// machineIDPaths are tried in order. /var/lib/dbus/machine-id is the fallback
// on hosts that predate systemd's location, and on images that ship only the
// D-Bus one. The agent image is Alpine and has NEITHER of its own, so one of
// these has to be bind-mounted in from the host — setup-agent.sh does that —
// or every containerised agent reports the same empty fingerprint and the hub's
// token-copied-to-a-second-host check can never fire.
var machineIDPaths = []string{
	"/etc/machine-id",
	"/var/lib/dbus/machine-id",
}

func fingerprint() string {
	for _, p := range machineIDPaths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		id := strings.TrimSpace(string(raw))
		// An empty file is not an identity. Treat it like a missing one and
		// fall through, rather than hashing "" into a fingerprint every host
		// with an unprovisioned machine-id would share.
		if id == "" {
			continue
		}
		sum := sha256.Sum256([]byte(id))
		return hex.EncodeToString(sum[:])
	}
	return ""
}
