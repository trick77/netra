package client

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"runtime"
	"strings"

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
		// Marshalling a struct we built ourselves cannot fail in practice;
		// a zero hash still behaves correctly, it just forces a resend.
		return make([]byte, 8)
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
func fingerprint() string {
	raw, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(string(raw))))
	return hex.EncodeToString(sum[:])
}
