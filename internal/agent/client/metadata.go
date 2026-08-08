package client

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"runtime"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/agent/config"
	"github.com/trick77/netra/internal/buildinfo"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// BuildMetadata gathers the static facts about this host and agent.
func BuildMetadata(cfg config.Config) *netrav1.Metadata {
	hostname, _ := os.Hostname()

	return &netrav1.Metadata{
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
