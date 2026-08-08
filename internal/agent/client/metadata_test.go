package client_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/trick77/netra/internal/agent/client"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// A metadata block that cannot be marshalled must NOT hash to a fixed value.
//
// The old fallback returned 8 zero bytes with a comment claiming it "just
// forces a resend". It did the opposite: once the hub stored the zero hash,
// reconcileMetadata's `!bytes.Equal(stored, sent) || len(stored) == 0` was
// false on both sides, so request_metadata was never set again and no later
// change could propagate. A value that differs each time is the only thing
// that actually forces the resend.
//
// Marshal is made to fail with invalid UTF-8 in a proto3 string field, which
// protobuf-go rejects at Marshal time — the same mechanism the Fingerprint
// hex-encoding note above the function describes.
func TestHashMetadataOnMarshalFailureIsNeverAFixedValue(t *testing.T) {
	md := &netrav1.Metadata{Hostname: "\xff\xfe invalid utf-8"}

	first := client.HashMetadata(md)
	second := client.HashMetadata(md)

	if len(first) != 8 || len(second) != 8 {
		t.Fatalf("len = %d and %d, want 8 each", len(first), len(second))
	}
	if bytes.Equal(first, make([]byte, 8)) {
		t.Error("marshal failure produced a zero hash, which permanently suppresses every future resend")
	}
	if bytes.Equal(first, second) {
		t.Error("two marshal failures produced the same hash; it must differ so the hub always sees a mismatch")
	}
}

func TestFingerprintReadsTheFirstUsableMachineID(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		return p
	}

	primary := write("primary", "  1122334455667788  \n")
	empty := write("empty", "\n  \n")
	fallback := write("fallback", "99aabbccddeeff00\n")
	missing := filepath.Join(dir, "does-not-exist")

	// The ordinary case: the first path wins, and surrounding whitespace is
	// not part of the identity.
	client.SetMachineIDPaths(t, primary, fallback)
	fromPrimary := client.Fingerprint()
	if fromPrimary == "" {
		t.Fatal("Fingerprint() is empty with a readable machine-id")
	}

	client.SetMachineIDPaths(t, write("trimmed", "1122334455667788"))
	if got := client.Fingerprint(); got != fromPrimary {
		t.Error("Fingerprint() depends on surrounding whitespace; it must not")
	}

	// A MISSING first path falls through rather than giving up. This is the
	// whole point of the fallback: hosts predating systemd's location keep
	// their id at /var/lib/dbus/machine-id.
	client.SetMachineIDPaths(t, missing, fallback)
	fromFallback := client.Fingerprint()
	if fromFallback == "" {
		t.Error("Fingerprint() gave up on a missing first path instead of trying the fallback")
	}
	if fromFallback == fromPrimary {
		t.Error("two different machine-ids hashed to the same fingerprint")
	}

	// An EMPTY file is not an identity either. Hashing "" would give every
	// host with an unprovisioned machine-id the same fingerprint, which is
	// exactly the collision the hub's anti-copy check looks for.
	client.SetMachineIDPaths(t, empty, fallback)
	if got := client.Fingerprint(); got != fromFallback {
		t.Error("an empty machine-id was treated as an identity instead of falling through")
	}

	// Nothing readable at all is an empty fingerprint, not a hash of "".
	client.SetMachineIDPaths(t, missing, empty)
	if got := client.Fingerprint(); got != "" {
		t.Errorf("Fingerprint() = %q with no usable machine-id, want empty", got)
	}
}

func TestHashMetadataIsStable(t *testing.T) {
	md := &netrav1.Metadata{Hostname: "h1", AgentVersion: "0.1.0", Location: "Gravelines, FR"}

	first := client.HashMetadata(md)
	second := client.HashMetadata(md)

	if !bytes.Equal(first, second) {
		t.Fatal("HashMetadata is not deterministic for identical input")
	}
	if len(first) != 8 {
		t.Fatalf("len(hash) = %d, want 8", len(first))
	}
}

// The hash is the entire change-detection mechanism: if an edited location
// does not move it, the hub never learns about the change.
func TestHashMetadataChangesWithContent(t *testing.T) {
	a := client.HashMetadata(&netrav1.Metadata{Hostname: "h1", Location: "Gravelines, FR"})
	b := client.HashMetadata(&netrav1.Metadata{Hostname: "h1", Location: "Falkenstein, DE"})

	if bytes.Equal(a, b) {
		t.Fatal("HashMetadata returned the same value for different metadata")
	}
}
