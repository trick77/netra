package buildinfo

import (
	"runtime"
	"testing"
)

func TestDefaultsWhenNotStamped(t *testing.T) {
	if got := Version(); got != "dev" {
		t.Fatalf("Version() = %q, want %q", got, "dev")
	}
}

func TestGoVersionMatchesRuntime(t *testing.T) {
	if got := GoVersion(); got != runtime.Version() {
		t.Fatalf("GoVersion() = %q, want %q", got, runtime.Version())
	}
}
