package client_test

import (
	"bytes"
	"testing"

	"github.com/trick77/netra/agent/client"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

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
