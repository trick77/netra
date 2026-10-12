package collector_test

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile writes a fixture file, creating parent directories as needed. It
// exists so a test that needs a one-off /proc tree does not have to be a
// checked-in fixture directory.
func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
