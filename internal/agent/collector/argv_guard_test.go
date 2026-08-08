package collector_test

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The agent must never read /proc/PID/cmdline or /proc/PID/environ.
//
// WHY THIS IS A TEST AND NOT A CODE REVIEW NOTE. `pid: host` gives the agent
// container a readable /proc entry for every process on the box. cmdline is
// argv, and argv routinely carries credentials: `mysql -p<pass>`,
// `-Dspring.datasource.password=...`, `--token=...`. environ is worse. An
// agent that shipped either to the hub would turn netra into a credential
// exfiltration path with a database behind it — and the operator who enabled
// `pid: host` for a CPU chart would have no idea.
//
// The trap is that the DANGEROUS implementation is the NATURAL one.
// /proc/PID/stat gives a 15-byte truncated `comm`, so the obvious way to get
// readable process names — "nginx: worker process", "/usr/bin/python3
// /opt/app/main.py" — is to read cmdline. Whoever writes the processes
// collector will reach for it unless something stops them. This is that thing,
// and it exists before the collector on purpose: a guard added afterwards only
// documents a decision already made wrongly.
//
// Process identity is `comm`, from /proc/PID/comm or field 2 of /proc/PID/stat.
// The cost is real and accepted: names truncate ("postgres: chec") and
// interpreted programs collapse to "python3" rather than the script they run.
// Aggregation is by name anyway, and the alternative is shipping secrets.
//
// The threat model is a well-meaning contributor, not an attacker. Anyone who
// wants to defeat this can; the point is that they cannot do it by accident.
const (
	guardSelf = "argv_guard_test.go"

	// A line carrying this marker is exempt. It exists so the guard is never
	// the thing standing in someone's way at 2am — a genuine need becomes a
	// visible, greppable decision rather than a reason to delete the test.
	// With literal-only matching below, it should never be needed.
	allowMarker = "//netra:allow-proc-read"
)

// guardRoots is every tree the agent binary is built from. cmd/netra-agent is
// listed separately and deliberately: it is exactly where a convenience "dump
// the process list" helper would land, and a walk that silently covered only
// the first root would pass every other assertion in this file.
// Relative to this package, internal/agent/collector.
var guardRoots = []string{
	"..", // internal/agent, this package included
	filepath.Join("..", "..", "..", "cmd", "netra-agent"),
}

// forbidden matches the /proc file names inside a Go string literal.
//
// Three properties, each load-bearing:
//
//   - STRING LITERALS ONLY. A comment saying "never read cmdline" is fine, and
//     the processes collector will want to write exactly that. Only quoted and
//     backtick-quoted text is examined.
//   - WORD BOUNDARIES. `environ` is a substring of "environment", which appears
//     in config.go's package comment. A naive substring match would be red the
//     moment this file landed.
//   - CASE-SENSITIVE LOWERCASE. os.Environ() reads the agent's OWN environment,
//     which is not the risk and stays allowed. The /proc file names are
//     lowercase.
var forbidden = regexp.MustCompile(`\b(cmdline|environ)\b`)

// literals extracts the contents of interpreted and raw string literals.
//
// Deliberately simple: it does not parse Go. A go/ast walk would be more
// correct and would also fail closed in the wrong direction — this must never
// miss a violation because a file did not compile, and a regexp over the raw
// bytes cannot.
var literals = regexp.MustCompile("\"[^\"\\n]*\"|`[^`]*`")

func TestAgentNeverReadsProcessArgsOrEnviron(t *testing.T) {
	var findings []string

	for _, root := range guardRoots {
		if _, err := os.Stat(root); err != nil {
			// A root that does not exist is a bug in this test, not a pass.
			t.Fatalf("guard root %s is not readable: %v", root, err)
		}

		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// Test files hold fixtures and this guard holds the words it bans.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			found, ferr := scanFile(path)
			if ferr != nil {
				return ferr
			}
			findings = append(findings, found...)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}

	if len(findings) == 0 {
		return
	}

	t.Errorf(`the agent must never read /proc/PID/cmdline or /proc/PID/environ:

%s

cmdline is argv and argv carries credentials — mysql -p<pass>,
-Dspring.datasource.password=..., --token=... The hub stores what it is sent,
and the operator who enabled 'pid: host' did it for a CPU chart.

Process identity comes from comm: /proc/PID/comm, or field 2 of /proc/PID/stat.
Yes, it truncates at 15 bytes and turns scripts into "python3". That is the
trade, and it was made deliberately (spec 6.2).

If you genuinely need one of these files, mark the line %s
so the decision is visible rather than silent.`,
		strings.Join(findings, "\n"), allowMarker)
}

// scanFile reports every forbidden literal in one file, as "path:line: text".
func scanFile(path string) ([]string, error) {
	if filepath.Base(path) == guardSelf {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []string
	sc := bufio.NewScanner(f)
	// Generated protobuf files carry lines far longer than the default 64KiB.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if strings.Contains(text, allowMarker) {
			continue
		}
		for _, lit := range literals.FindAllString(text, -1) {
			if forbidden.MatchString(lit) {
				out = append(out, fmt.Sprintf("  %s:%d: %s", path, line, strings.TrimSpace(text)))
				break
			}
		}
	}
	return out, sc.Err()
}
