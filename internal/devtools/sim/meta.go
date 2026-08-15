package sim

import (
	"github.com/trick77/netra/internal/agent/client"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// HashMetadata reduces a metadata block to the 8 bytes sent on every POST.
//
// It delegates to the agent's implementation rather than repeating it. The
// hub compares the hash it stored against the one on the wire and asks for a
// resend when they differ, so an implementation that hashed even slightly
// differently -- a non-deterministic marshal, a different digest length --
// would make every POST request the metadata again, forever, without ever
// failing outright.
func HashMetadata(md *netrav1.Metadata) []byte {
	return client.HashMetadata(md)
}
