package buildinfo_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/trick77/netra/internal/shared/buildinfo"
)

func TestHandleVersionFlagPrintsIdentity(t *testing.T) {
	for _, flag := range []string{"--version", "-version"} {
		var out bytes.Buffer

		if !buildinfo.HandleVersionFlag([]string{"netra", flag}, &out, "netra") {
			t.Fatalf("HandleVersionFlag(%q) = false, want true", flag)
		}

		got := out.String()
		for _, want := range []string{"netra", buildinfo.Version(), buildinfo.Commit(), buildinfo.GoVersion()} {
			if !strings.Contains(got, want) {
				t.Fatalf("output %q does not contain %q", got, want)
			}
		}
		if !strings.HasSuffix(got, "\n") {
			t.Fatalf("output %q is not newline-terminated", got)
		}
	}
}

// Anything that is not the version flag must fall through untouched, or a
// normal start would print the banner and exit instead of running.
func TestHandleVersionFlagIgnoresOtherArgs(t *testing.T) {
	cases := [][]string{
		{"netra"},
		{"netra", "serve"},
		{"netra", "--help"},
		{"netra", "version"},
	}

	for _, args := range cases {
		var out bytes.Buffer

		if buildinfo.HandleVersionFlag(args, &out, "netra") {
			t.Fatalf("HandleVersionFlag(%v) = true, want false", args)
		}
		if out.Len() != 0 {
			t.Fatalf("HandleVersionFlag(%v) wrote %q, want nothing", args, out.String())
		}
	}
}
