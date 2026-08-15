package buildinfo

import (
	"fmt"
	"io"
)

// HandleVersionFlag prints build identity and reports whether the process was
// asked only to do that. Callers return immediately when it reports true.
//
// The logic lives here rather than in each main() because an entrypoint cannot
// be unit tested: putting it in a package keeps the behaviour covered while the
// call site in main stays a single line.
func HandleVersionFlag(args []string, w io.Writer, name string) bool {
	if len(args) < 2 {
		return false
	}
	if args[1] != "--version" && args[1] != "-version" {
		return false
	}

	fmt.Fprintf(w, "%s %s (commit %s, %s)\n", name, Version(), Commit(), GoVersion())
	return true
}
