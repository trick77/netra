package config

import "testing"

func TestFsMountsParsesTheRenderedMapping(t *testing.T) {
	// Given: the value setup-agent.sh renders for a three-filesystem host.
	// When: it is parsed.
	got := fsMounts("root=/,ark=/mnt/ark,var-log=/var/log")

	// Then: each label names the host mountpoint it stands for.
	want := map[string]string{"root": "/", "ark": "/mnt/ark", "var-log": "/var/log"}
	for label, mountpoint := range want {
		if got[label] != mountpoint {
			t.Errorf("%q = %q, want %q", label, got[label], mountpoint)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d entries, want %d", len(got), len(want))
	}
}

// A mountpoint may contain the separators, so setup-agent.sh percent-encodes
// them. Decoding is what makes such a path survive the round trip instead of
// splitting into two entries that name nothing.
func TestFsMountsDecodesEncodedSeparators(t *testing.T) {
	// Given: a mountpoint containing a comma, an equals sign and a percent.
	// When: it is parsed.
	got := fsMounts("odd=/mnt/a%2Cb%3Dc%25d")

	// Then: the original path comes back.
	if got["odd"] != "/mnt/a,b=c%d" {
		t.Errorf("odd = %q, want /mnt/a,b=c%%d", got["odd"])
	}
}

// A malformed entry costs that one filesystem its mountpoint. It must not cost
// the operator every other metric on the host, which is what a fatal error
// here would do.
func TestFsMountsSkipsMalformedEntriesWithoutLosingTheRest(t *testing.T) {
	// Given: a value with a junk entry in the middle.
	// When: it is parsed.
	got := fsMounts("root=/,nonsense,=/orphan,ark=")

	// Then: only the usable entry survives, and nothing panics.
	if len(got) != 1 || got["root"] != "/" {
		t.Errorf("got %v, want just root=/", got)
	}
}

func TestFsMountsIsNilWhenUnset(t *testing.T) {
	if got := fsMounts(""); got != nil {
		t.Errorf("got %v, want nil so the collector falls back to the bare label", got)
	}
}
